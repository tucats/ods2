package volume

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
)

// Initialize builds a fresh ODS-2 volume the way VMS INITIALIZE does, so
// that VMS can mount and use it.
//
// This follows VMS 7.3 INIT's own source (the INIT facility's INIDSK,
// INIALL, and ININDX modules): the same defaults, the same allocation of
// the volume's fixed structures, and the same contents for them. It was
// checked structure by structure against volumes VMS INITIALIZE made on a
// simulated VAX (an RX33, an RD51, and an RD54, covering a small disk, a
// cluster factor of 1, and a cluster factor of 3). Only things VMS itself
// varies from volume to volume differ: timestamps, and the boot block,
// which INIT fills with a small PDP-11 program that prints "<label> is not
// a system disk" and which this package leaves zeroed (VMS doesn't need it
// to mount or use the volume).
//
// A volume is laid out like this:
//
//   - LBN 0 is the boot block, and LBN 1 the primary home block. The home
//     block describes the volume and says where everything else is.
//   - A secondary home block is placed on the "home block search
//     sequence", LBN 1 + n*delta (see homeBlockDelta), so the volume can
//     still be found if LBN 1 goes bad.
//   - The master file directory (000000.DIR), the storage bitmap
//     (BITMAP.SYS), the index file's bitmap and its initial file headers,
//     and SECURITY.SYS are placed together, in that order, starting in the
//     middle of the volume (at its start for a small disk), where the
//     disk heads spend most of their time.
//   - A backup copy of the index file's own header is placed delta blocks
//     after the index file.
//
// INDEXF.SYS maps all of these fixed structures -- boot block, home
// blocks, backup header, index bitmap, headers -- as its virtual blocks,
// in that order, which is why the home block's VBN fields are simple
// multiples of the cluster factor.
//
// c is typically freshly created by diskimage.Create, sized to the volume.
// Initialize doesn't mount the volume, matching VMS.
func Initialize(c diskimage.WritableContainer, opts InitializeOptions) error {
	p, err := resolveInitParams(c.Blocks(), opts)
	if err != nil {
		return fmt.Errorf("volume: Initialize: %w", err)
	}

	a, err := allocateStructures(p)
	if err != nil {
		return fmt.Errorf("volume: Initialize: %w", err)
	}

	w := initWriter{c: c, p: p, a: a, now: vmstime.FromTime(time.Now())}

	for _, step := range []func() error{
		w.zeroFixedStructures,
		w.writeHomeBlocks,
		w.writeIndexBitmap,
		w.writeHeaders,
		w.writeStorageBitmap,
		w.writeMFD,
		w.writeSecurity,
	} {
		if err := step(); err != nil {
			return fmt.Errorf("volume: Initialize: %w", err)
		}
	}

	return nil
}

// Defaults VMS INITIALIZE uses (INIT's INITIMG and INIDSK modules).
const (
	defaultFileProtection   = 0xFA00 // S:RWED,O:RWED,G:RE,W:
	defaultRecordProtection = 0xFE00
	defaultExtendSize       = 5
	defaultWindowSize       = 7
	defaultAccessedDirs     = 3
	defaultHeaders          = 16 // INITIALIZE/HEADERS, and its minimum
	defaultDirectories      = 16 // INITIALIZE/DIRECTORIES: MFD entries to allocate for

	// smallDiskBlocks is the largest "small" disk, whose index file INIT
	// places at the start of the volume rather than the middle.
	smallDiskBlocks = 4096

	// minSecurityBlocks is SECURITY.SYS's least size.
	minSecurityBlocks = 6

	// Home block search deltas (HM2$C_GEOM_INDEPEND_DELTA and
	// HM2$C_FIXED_CONTIG_DELTA), and HM2$C_LIMITED_SEARCH_LENGTH: a delta
	// longer than a tenth of the volume becomes 1.
	fixedHomeBlockDelta      = 1033
	contiguousHomeBlockDelta = 1
	limitedSearchLength      = 10

	// defaultOwner is the volume owner when none is given: [1,4], the
	// SYSTEM account's UIC, which is what INIT records when run by SYSTEM.
	defaultOwnerGroup, defaultOwnerMember = 1, 4
)

// IndexPosition says where Initialize places the index file and the other
// structures that go with it (INITIALIZE/INDEX).
type IndexPosition int

const (
	// IndexMiddle places them in the middle of the volume, or at its
	// start for a disk of 4096 blocks or fewer: VMS's default.
	IndexMiddle IndexPosition = iota
	// IndexBeginning places them at the start of the volume.
	IndexBeginning
	// IndexEnd places them at the end, allocated backward.
	IndexEnd
	// IndexAtLBN places them at InitializeOptions.IndexLBN.
	IndexAtLBN
)

// HomeBlockPlacement is how Initialize picks the home block search delta
// (INITIALIZE/HOMEBLOCKS).
type HomeBlockPlacement int

const (
	// HomeBlocksFixed uses the geometry-independent delta, 1033: VMS's
	// default, and what a VMS-initialized simh disk has.
	HomeBlocksFixed HomeBlockPlacement = iota
	// HomeBlocksGeometry derives the delta from the disk's geometry.
	HomeBlocksGeometry
	// HomeBlocksContiguous uses a delta of 1.
	HomeBlocksContiguous
)

// Geometry is a disk's physical shape, recorded in the storage control
// block and used by HomeBlocksGeometry.
type Geometry struct {
	Sectors   uint32 // sectors per track
	Tracks    uint32 // tracks per cylinder
	Cylinders uint32
}

// knownGeometries are the shapes of DEC's MSCP disks, as simh presents
// them (and as VMS recorded them in the storage control blocks of volumes
// it initialized on simh), keyed by size in blocks. Initialize uses one
// when the volume's size matches and no Geometry is given.
var knownGeometries = map[uint32]Geometry{
	800:    {Sectors: 10, Tracks: 1, Cylinders: 80},    // RX50
	2400:   {Sectors: 15, Tracks: 2, Cylinders: 80},    // RX33
	21600:  {Sectors: 18, Tracks: 4, Cylinders: 300},   // RD51
	311200: {Sectors: 17, Tracks: 15, Cylinders: 1221}, // RD54
}

// InitializeOptions are the choices VMS INITIALIZE's qualifiers make. Every
// field is optional: its zero value selects INIT's default.
type InitializeOptions struct {
	// Label is the volume label, at most 12 characters. Defaults to
	// "NONAME".
	Label string

	// Owner is the volume owner's UIC (INITIALIZE/OWNER_UIC), which every
	// reserved file is owned by too. Defaults to [1,4].
	Owner ondisk.Uic

	// OwnerName is the home block's owner name, at most 12 characters.
	// Defaults to blank.
	OwnerName string

	// VolumeProtection is the volume's protection (INITIALIZE/PROTECTION),
	// also recorded in SECURITY.SYS. Defaults to 0, no restriction.
	VolumeProtection uint16

	// FileProtection is the default protection of files created on the
	// volume (INITIALIZE/FILE_PROTECTION). Defaults to 0xFA00.
	FileProtection uint16

	// ClusterSize is the allocation unit in blocks (INITIALIZE/CLUSTER_
	// SIZE). Defaults as INIT does: 1 for a disk of up to 50000 blocks;
	// otherwise the larger of 3 and what keeps the storage bitmap within
	// 255 blocks.
	ClusterSize uint16

	// MaxFiles is how many files the volume can ever hold
	// (INITIALIZE/MAXIMUM_FILES). Defaults to blocks/((cluster+1)*2), and
	// can't exceed blocks/(cluster+1).
	MaxFiles uint32

	// Headers is how many file headers the index file gets up front
	// (INITIALIZE/HEADERS), at least 16. The index file grows by itself
	// when more are needed (see ensureHeaderSlot). Defaults to 16.
	Headers uint32

	// Directories is how many entries the master file directory is sized
	// for (INITIALIZE/DIRECTORIES): it gets Directories/16+1 blocks.
	// Defaults to 16.
	Directories uint32

	// Index and IndexLBN place the index file (INITIALIZE/INDEX).
	Index    IndexPosition
	IndexLBN uint32

	// HomeBlocks picks the home block search delta.
	HomeBlocks HomeBlockPlacement

	// Geometry is the disk's shape. Defaults to the shape of the DEC disk
	// the volume's size matches (see knownGeometries), or to one sector,
	// one track, and a cylinder per block.
	Geometry Geometry

	// Blocks is the volume's size, when it differs from the container's:
	// a simh disk image, for one, may carry a trailing block of simh's own
	// metadata. Defaults to the container's size.
	Blocks uint32

	// SerialNumber is the pack serial number. Defaults to 0.
	SerialNumber uint32
}

// initParams are InitializeOptions with every default applied.
type initParams struct {
	label, ownerName string
	owner            ondisk.Uic
	volumeProt       uint16
	fileProt         uint16
	serial           uint32

	maxBlock    uint32 // the device's size (DIB$L_MAXBLOCK)
	cluster     uint32
	volumeSize  uint32 // maxBlock rounded up to a whole cluster
	maxFiles    uint32
	headers     uint32
	directories uint32
	indexLBN    uint32
	indexEnd    bool
	delta       uint32
	geometry    Geometry
}

// resolveInitParams applies INIT's defaults and limits (INIDSK) to opts for
// a container of containerBlocks blocks.
func resolveInitParams(containerBlocks uint32, opts InitializeOptions) (initParams, error) {
	p := initParams{
		label:       opts.Label,
		ownerName:   opts.OwnerName,
		owner:       opts.Owner,
		volumeProt:  opts.VolumeProtection,
		fileProt:    opts.FileProtection,
		serial:      opts.SerialNumber,
		maxBlock:    opts.Blocks,
		cluster:     uint32(opts.ClusterSize),
		maxFiles:    opts.MaxFiles,
		headers:     opts.Headers,
		directories: opts.Directories,
		geometry:    opts.Geometry,
	}

	if p.label == "" {
		p.label = "NONAME"
	}

	if p.owner == (ondisk.Uic{}) {
		p.owner = ondisk.Uic{Group: defaultOwnerGroup, Member: defaultOwnerMember}
	}

	if p.fileProt == 0 {
		p.fileProt = defaultFileProtection
	}

	if p.maxBlock == 0 {
		p.maxBlock = containerBlocks
	}

	if p.maxBlock > containerBlocks {
		return p, fmt.Errorf("volume size %d is larger than the container's %d blocks", p.maxBlock, containerBlocks)
	}

	if p.geometry == (Geometry{}) {
		if g, ok := knownGeometries[p.maxBlock]; ok {
			p.geometry = g
		} else {
			p.geometry = Geometry{Sectors: 1, Tracks: 1, Cylinders: p.maxBlock}
		}
	}

	if p.cluster == 0 {
		if p.maxBlock <= 50000 {
			p.cluster = 1
		} else {
			p.cluster = max(3, ((p.maxBlock+4095)/4096+254)/255)
		}
	}

	p.volumeSize = (p.maxBlock + p.cluster - 1) / p.cluster * p.cluster

	if p.volumeSize/p.cluster < 50 {
		return p, fmt.Errorf("a %d-block volume with cluster size %d has fewer than the 50 clusters VMS requires", p.maxBlock, p.cluster)
	}

	if p.maxFiles == 0 {
		p.maxFiles = p.maxBlock / ((p.cluster + 1) * 2)
	}

	p.maxFiles = min(p.maxFiles, p.volumeSize/(p.cluster+1), 1<<24-1<<16-1)
	if p.maxFiles < ondisk.ReservedFileCount {
		return p, fmt.Errorf("maximum files %d is fewer than the %d reserved files every volume has", p.maxFiles, ondisk.ReservedFileCount)
	}

	if p.headers == 0 {
		p.headers = defaultHeaders
	}

	p.headers = min(max(p.headers, defaultHeaders), p.maxFiles)

	if p.directories == 0 {
		p.directories = defaultDirectories
	}

	switch opts.Index {
	case IndexBeginning:
		p.indexLBN = 0
	case IndexEnd:
		p.indexLBN, p.indexEnd = p.maxBlock-1, true
	case IndexAtLBN:
		if opts.IndexLBN >= p.maxBlock {
			return p, fmt.Errorf("index file LBN %d is past the end of the volume", opts.IndexLBN)
		}

		p.indexLBN = opts.IndexLBN
	default:
		if p.maxBlock > smallDiskBlocks {
			p.indexLBN = p.maxBlock / 2
		}
	}

	p.delta = homeBlockDelta(opts.HomeBlocks, p.maxBlock, p.geometry)

	return p, nil
}

// homeBlockDelta is the spacing of the home block search sequence
// (GET_DELTA): where VMS MOUNT looks for a home block if LBN 1's is bad,
// and so where INIT puts the secondary home block.
func homeBlockDelta(placement HomeBlockPlacement, maxBlock uint32, g Geometry) uint32 {
	var delta uint32

	switch placement {
	case HomeBlocksGeometry:
		blockFactor := max(1, g.Sectors*g.Tracks*g.Cylinders/maxBlock)
		delta = 1

		if g.Cylinders > 1 && g.Tracks > 1 {
			delta += g.Tracks
		}

		if g.Sectors > 1 && (g.Cylinders > 1 || g.Tracks > 1) {
			delta = (delta*g.Sectors + blockFactor) / blockFactor
		}
	case HomeBlocksContiguous:
		delta = contiguousHomeBlockDelta
	default:
		delta = fixedHomeBlockDelta
	}

	if delta == 0 || delta > maxBlock/limitedSearchLength {
		delta = 1
	}

	return delta
}

// The allocation table's entries (INIT's ALLOC_TABLE), in the order
// INDEXF.SYS maps the first five.
const (
	allocBoot = iota
	allocHome1
	allocHome2
	allocAltIndex // the backup copy of INDEXF.SYS's header
	allocIndex    // INDEXF.SYS's bitmap and initial headers
	allocBitmap
	allocMFD
	allocSecurity
	allocBadBlock // a partial last cluster, past the end of the device
	allocVolEnd   // bitmap bits past the end of the volume
	allocCount
)

// allocation is where INIT's allocation table puts each structure.
type allocation struct {
	lbn    [allocCount]uint32
	count  [allocCount]uint32
	placed [allocCount]bool

	// realHome is the secondary home block's LBN (REAL_HOMEBLOCK), which
	// may lie inside its cluster rather than at its start.
	realHome uint32

	indexBitmapBlocks uint32
	storageMapBlocks  uint32 // BITMAP.SYS's bitmap blocks, after its SCB
}

// allocateStructures places every fixed structure, as INIT_ALLOCATE does.
func allocateStructures(p initParams) (*allocation, error) {
	a := &allocation{
		indexBitmapBlocks: (p.maxFiles + 4095) / 4096,
		storageMapBlocks:  (p.volumeSize/p.cluster + 4095) / 4096,
	}

	c := p.cluster

	// The bitmap's last block covers clusters past the end of the volume;
	// an entry for them keeps anything from being allocated there and
	// marks them in use.
	a.lbn[allocVolEnd] = p.maxBlock / c * c
	a.count[allocVolEnd] = (4096 - (p.maxBlock/c)%4096) * c
	a.placed[allocVolEnd] = true

	// A last cluster that runs past the end of the device is recorded as
	// bad, so it's never allocated (INIT's factory bad block handling).
	if p.maxBlock%c != 0 {
		a.lbn[allocBadBlock] = p.maxBlock / c * c
		a.count[allocBadBlock] = c
		a.placed[allocBadBlock] = true
	}

	a.count[allocBoot] = 1
	if err := a.allocate(allocBoot, 0, false, p); err != nil {
		return nil, err
	}

	// With the boot block at LBN 0 and clusters larger than a block, the
	// primary home block (LBN 1) is inside the boot cluster, and the
	// "home block 1" cluster is a dummy after it, still filled with home
	// block copies.
	a.count[allocHome1] = 1
	if a.lbn[allocBoot] == 0 && c > 1 {
		if err := a.allocate(allocHome1, 0, false, p); err != nil {
			return nil, err
		}
	} else if err := a.allocateHome(allocHome1, p); err != nil {
		return nil, err
	}

	a.count[allocHome2] = 1
	if err := a.allocateHome(allocHome2, p); err != nil {
		return nil, err
	}

	a.count[allocMFD] = p.directories/16 + 1
	a.count[allocBitmap] = a.storageMapBlocks + 1
	a.count[allocIndex] = p.headers + a.indexBitmapBlocks

	order := []int{allocMFD, allocBitmap, allocIndex}
	if p.indexEnd {
		order = []int{allocIndex, allocBitmap, allocMFD}
	}

	for _, e := range order {
		if err := a.allocate(e, p.indexLBN, p.indexEnd, p); err != nil {
			return nil, err
		}
	}

	a.count[allocAltIndex] = 1

	altStart := a.lbn[allocIndex] + p.delta
	if p.indexEnd {
		altStart = a.lbn[allocIndex] - p.delta
	}

	if err := a.allocate(allocAltIndex, altStart, p.indexEnd, p); err != nil {
		return nil, err
	}

	a.count[allocSecurity] = minSecurityBlocks
	if err := a.allocate(allocSecurity, p.indexLBN, p.indexEnd, p); err != nil {
		return nil, err
	}

	return a, nil
}

// allocate places entry e at the first free position from start, searching
// forward or backward past each conflicting entry (INIT's ALLOCATE), with
// its position and size rounded to whole clusters.
func (a *allocation) allocate(e int, start uint32, reverse bool, p initParams) error {
	c := p.cluster
	a.lbn[e] = start / c * c
	a.count[e] = (a.count[e] + c - 1) / c * c

	for {
		if a.lbn[e] >= p.volumeSize {
			return fmt.Errorf("no room on the volume for its fixed structures")
		}

		conflict := a.conflict(e)
		if conflict < 0 {
			a.placed[e] = true

			return nil
		}

		if reverse {
			if a.lbn[conflict] < a.count[e] {
				return fmt.Errorf("no room on the volume for its fixed structures")
			}

			a.lbn[e] = a.lbn[conflict] - a.count[e]
		} else {
			a.lbn[e] = a.lbn[conflict] + a.count[conflict]
		}
	}
}

// allocateHome places a home block cluster at the first free position on
// the home block search sequence, LBN 1, 1+delta, 1+2*delta, ... (INIT's
// ALLOCATE_HOME), recording the home block's own LBN in realHome.
func (a *allocation) allocateHome(e int, p initParams) error {
	c := p.cluster
	a.count[e] = (a.count[e] + c - 1) / c * c

	for lbn := uint32(1); ; lbn += p.delta {
		if lbn >= p.volumeSize {
			return fmt.Errorf("no room on the volume for a home block")
		}

		a.lbn[e] = lbn / c * c
		if a.conflict(e) < 0 {
			a.placed[e] = true
			a.realHome = lbn

			return nil
		}
	}
}

// conflict returns the first placed entry overlapping entry e, or -1.
func (a *allocation) conflict(e int) int {
	for i := range allocCount {
		if i == e || !a.placed[i] || a.count[i] == 0 {
			continue
		}

		if a.lbn[e] < a.lbn[i]+a.count[i] && a.lbn[i] < a.lbn[e]+a.count[e] {
			return i
		}
	}

	return -1
}

// indexFileExtents is INDEXF.SYS's map: the boot block, home block 1, home
// block 2, backup index header, and index file entries in turn, with
// physically adjacent entries merged, as INIT_INDEX builds it.
func (a *allocation) indexFileExtents() []ondisk.Extent {
	extents := []ondisk.Extent{{Count: a.count[allocBoot], StartLBN: a.lbn[allocBoot]}}

	for _, e := range []int{allocHome1, allocHome2, allocAltIndex, allocIndex} {
		last := &extents[len(extents)-1]
		if last.StartLBN+last.Count == a.lbn[e] {
			last.Count += a.count[e]
		} else {
			extents = append(extents, ondisk.Extent{Count: a.count[e], StartLBN: a.lbn[e]})
		}
	}

	return extents
}

// reservedFile describes one of the ten files every volume VMS 7.3
// initializes starts with, in file number order: the order INIT's own
// headers are numbered in.
type reservedFile struct {
	fid        ondisk.Fid
	name       string
	recordSize uint16
}

var reservedFiles = []reservedFile{
	{ondisk.IndexFileFid, "INDEXF.SYS", 512},
	{ondisk.BitmapFileFid, "BITMAP.SYS", 512},
	{ondisk.BadBlockFileFid, "BADBLK.SYS", 512},
	{ondisk.MasterFileDirectoryFid, "000000.DIR", 512},
	{ondisk.CoreImageFileFid, "CORIMG.SYS", 512},
	{ondisk.VolumeSetFileFid, "VOLSET.SYS", 64},
	{ondisk.ContinuationFileFid, "CONTIN.SYS", 512},
	{ondisk.BackupFileFid, "BACKUP.SYS", 64},
	{ondisk.BadBlockLogFileFid, "BADLOG.SYS", 16},
	{ondisk.SecurityFileFid, "SECURITY.SYS", 512},
}

// initWriter writes a volume's structures once they are placed.
type initWriter struct {
	c   diskimage.WritableContainer
	p   initParams
	a   *allocation
	now vmstime.VMSTime
}

func (w *initWriter) write(lbn uint32, b []byte) error {
	if err := w.c.WriteBlock(lbn, b); err != nil {
		return fmt.Errorf("writing LBN %d: %w", lbn, err)
	}

	return nil
}

// zeroFixedStructures clears every block the fixed structures occupy, so
// nothing a container held before shows through: unused header slots,
// directory blocks, and the boot block must all read as zeros.
func (w *initWriter) zeroFixedStructures() error {
	zero := make([]byte, ondisk.BlockSize)

	for e := range allocVolEnd {
		if !w.a.placed[e] || e == allocBadBlock {
			continue
		}

		for i := range w.a.count[e] {
			if lbn := w.a.lbn[e] + i; lbn < w.p.maxBlock {
				if err := w.write(lbn, zero); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// homeBlock is the home block as INIT_INDEX builds it, before each copy's
// own LBN and VBN are filled in.
func (w *initWriter) homeBlock() ondisk.HomeBlock {
	p, a := w.p, w.a
	c := p.cluster

	return ondisk.HomeBlock{
		AlternateHomeLBN:        a.realHome,
		AlternateIndexLBN:       a.lbn[allocAltIndex],
		StructureLevel:          ondisk.FileHeaderStructureLevel,
		ClusterSize:             uint16(c),
		AlternateHomeVBN:        uint16(a.realHome - a.lbn[allocHome2] + c*2 + 1),
		AlternateIndexVBN:       uint16(c*3 + 1),
		IndexBitmapVBN:          uint16(c*4 + 1),
		IndexBitmapLBN:          a.lbn[allocIndex],
		MaxFiles:                p.maxFiles,
		IndexBitmapSize:         uint16(a.indexBitmapBlocks),
		ReservedFiles:           ondisk.ReservedFileCount,
		VolumeOwner:             p.owner,
		Protection:              p.volumeProt,
		FileProtection:          p.fileProt,
		RecordProtection:        defaultRecordProtection,
		CreationDate:            w.now,
		WindowSize:              defaultWindowSize,
		DirectoryPreAccessLimit: defaultAccessedDirs,
		DefaultExtendSize:       defaultExtendSize,
		SerialNumber:            p.serial,
		VolumeName:              p.label,
		OwnerName:               p.ownerName,
		Format:                  ondisk.HomeBlockFormatID,
	}
}

// writeHomeBlocks writes the home block into the rest of the boot block's
// cluster, then all of home block 1's cluster, then all of home block 2's,
// each copy recording its own LBN and INDEXF.SYS VBN (INIT's
// WRITE_HOMEBLOCK loop).
func (w *initWriter) writeHomeBlocks() error {
	h := w.homeBlock()
	h.HomeVBN = 2

	put := func(lbn uint32) error {
		h.HomeLBN = lbn

		b, err := ondisk.EncodeHomeBlock(h)
		if err != nil {
			return err
		}

		h.HomeVBN++

		return w.write(lbn, b)
	}

	c := w.p.cluster

	for i := uint32(1); i < c; i++ {
		if err := put(w.a.lbn[allocBoot] + i); err != nil {
			return err
		}
	}

	for _, e := range []int{allocHome1, allocHome2} {
		for i := range c {
			if err := put(w.a.lbn[e] + i); err != nil {
				return err
			}
		}
	}

	return nil
}

// writeIndexBitmap marks the reserved files' header slots in use.
func (w *initWriter) writeIndexBitmap() error {
	b := make([]byte, ondisk.BlockSize)
	binary.LittleEndian.PutUint32(b, 1<<ondisk.ReservedFileCount-1)

	return w.write(w.a.lbn[allocIndex], b)
}

// headerLBN is where file n's header goes: right after the index bitmap.
func (w *initWriter) headerLBN(n uint32) uint32 {
	return w.a.lbn[allocIndex] + w.a.indexBitmapBlocks + n - 1
}

// writeHeaders writes the ten reserved files' headers, as INIT_INDEX
// builds them from one template, and the backup copy of INDEXF.SYS's.
func (w *initWriter) writeHeaders() error {
	p, a := w.p, w.a
	c := p.cluster

	for _, f := range reservedFiles {
		h := ondisk.FileHeader{
			StructureLevel: ondisk.FileHeaderStructureLevel,
			Fid:            f.fid,
			RecordAttributes: ondisk.RecAttr{
				Format:         ondisk.RecordFormatFixed,
				RecordSize:     f.recordSize,
				MaxRecordSize:  f.recordSize,
				EndOfFileBlock: 1,
			},
			RecordProtection: defaultRecordProtection,
			Owner:            p.owner,
			FileProtection:   p.fileProt,
			Backlink:         ondisk.MasterFileDirectoryFid,
			HighWaterMark:    1,
		}

		var extents []ondisk.Extent

		ra := &h.RecordAttributes

		switch f.fid.Number() {
		case ondisk.IndexFileFid.Number():
			extents = a.indexFileExtents()
			ra.HighestBlock = c*4 + a.count[allocIndex]
			ra.EndOfFileBlock = c*4 + a.indexBitmapBlocks + ondisk.ReservedFileCount + 1
			h.HighWaterMark = ra.HighestBlock + 1

		case ondisk.BitmapFileFid.Number():
			extents = []ondisk.Extent{{Count: a.count[allocBitmap], StartLBN: a.lbn[allocBitmap]}}
			h.FileCharacteristics = ondisk.FchContig
			ra.HighestBlock = a.count[allocBitmap]
			ra.EndOfFileBlock = a.storageMapBlocks + 2
			h.HighWaterMark = ra.EndOfFileBlock

		case ondisk.BadBlockFileFid.Number():
			if a.placed[allocBadBlock] {
				extents = []ondisk.Extent{{Count: a.count[allocBadBlock], StartLBN: a.lbn[allocBadBlock]}}
				ra.HighestBlock = a.count[allocBadBlock]
				ra.EndOfFileBlock = ra.HighestBlock + 1
				h.HighWaterMark = ra.EndOfFileBlock
			}

		case ondisk.MasterFileDirectoryFid.Number():
			extents = []ondisk.Extent{{Count: a.count[allocMFD], StartLBN: a.lbn[allocMFD]}}
			h.FileCharacteristics = ondisk.FchDirectory | ondisk.FchContig
			h.FileProtection = p.fileProt &^ 0x4444 // no delete access
			ra.Format = ondisk.RecordFormatVariable
			ra.Attributes = recordAttrNoSpan
			ra.HighestBlock = a.count[allocMFD]
			ra.EndOfFileBlock = 2
			h.HighWaterMark = 2

		case ondisk.SecurityFileFid.Number():
			extents = []ondisk.Extent{{Count: a.count[allocSecurity], StartLBN: a.lbn[allocSecurity]}}
			h.FileCharacteristics = ondisk.FchContig
			ra.HighestBlock = a.count[allocSecurity]
			ra.EndOfFileBlock = 2 // one block of security profile, written below
			h.HighWaterMark = ra.HighestBlock + 1
		}

		mapBytes, err := ondisk.EncodeRetrievalPointers(extents)
		if err != nil {
			return fmt.Errorf("encoding %s's map: %w", f.name, err)
		}

		filename, extension := ondisk.IdentName(f.name, 1)

		b, err := ondisk.EncodeFileHeader(h, ondisk.FileHeaderAreas{
			Ident: &ondisk.Ident{
				Filename:          filename,
				FilenameExtension: extension,
				Revision:          1,
				CreationDate:      w.now,
				RevisionDate:      w.now,
			},
			MapBytes: mapBytes,
		})
		if err != nil {
			return fmt.Errorf("encoding %s's header: %w", f.name, err)
		}

		if err := w.write(w.headerLBN(uint32(f.fid.Number())), b); err != nil {
			return err
		}

		// Every block of the backup index header's cluster holds a copy
		// of INDEXF.SYS's header.
		if f.fid.Number() == ondisk.IndexFileFid.Number() {
			for i := range c {
				if err := w.write(a.lbn[allocAltIndex]+i, b); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// recordAttrNoSpan is FAT$M_NOSPAN: records don't cross block boundaries,
// as a directory's don't.
const recordAttrNoSpan = 0x08

// writeStorageBitmap writes BITMAP.SYS: its storage control block, then one
// bit per cluster, set for a free cluster, with every fixed structure's
// clusters (and those past the end of the volume) in use.
func (w *initWriter) writeStorageBitmap() error {
	p, a := w.p, w.a

	scb, err := ondisk.EncodeStorageControlBlock(ondisk.StorageControlBlock{
		StructureLevel: ondisk.FileHeaderStructureLevel,
		ClusterSize:    uint16(p.cluster),
		VolumeSize:     p.maxBlock,
		BlockSize:      1, // in 512-byte blocks
		Sectors:        p.geometry.Sectors,
		Tracks:         p.geometry.Tracks,
		Cylinders:      p.geometry.Cylinders,
	})
	if err != nil {
		return err
	}

	if err := w.write(a.lbn[allocBitmap], scb); err != nil {
		return err
	}

	bits := make([]byte, a.storageMapBlocks*ondisk.BlockSize)
	for i := range bits {
		bits[i] = 0xFF
	}

	for e := range allocCount {
		if !a.placed[e] {
			continue
		}

		for cl := a.lbn[e] / p.cluster; cl < (a.lbn[e]+a.count[e])/p.cluster && cl/8 < uint32(len(bits)); cl++ {
			bits[cl/8] &^= 1 << (cl % 8)
		}
	}

	for i := range a.storageMapBlocks {
		if err := w.write(a.lbn[allocBitmap]+1+i, bits[i*ondisk.BlockSize:(i+1)*ondisk.BlockSize]); err != nil {
			return err
		}
	}

	return nil
}

// writeMFD writes the master file directory's first block, listing the
// reserved files, each with a version limit of 1.
func (w *initWriter) writeMFD() error {
	entries := make([]ondisk.DirEntry, 0, len(reservedFiles))
	for _, f := range reservedFiles {
		entries = append(entries, ondisk.DirEntry{Name: f.name, Version: 1, Fid: f.fid, VersionLimit: 1})
	}

	b, err := ondisk.EncodeDirectoryBlock(entries)
	if err != nil {
		return err
	}

	return w.write(w.a.lbn[allocMFD], b)
}

// writeSecurity writes SECURITY.SYS's first block: the volume's security
// profile, which VMS reads when it mounts the volume.
func (w *initWriter) writeSecurity() error {
	b := make([]byte, ondisk.BlockSize)
	copy(b, securityProfile(w.p.owner, w.p.volumeProt, w.p.label))

	return w.write(w.a.lbn[allocSecurity], b)
}

// securityProfile builds the volume security profile INIT writes to
// SECURITY.SYS: an object rights block (ORB) in VMS's packed
// type-length-value form (made by the kernel routine EXE$ORB_TO_TLV, which
// isn't in the source archive). Its layout was read from three
// VMS-initialized volumes, which differ only in their labels and in the
// first longword -- the XOR of every whole longword after it:
//
//	0:  longword  XOR check of bytes 4 to the last whole longword
//	4:  longword  0
//	8:  longword  total length
//	12: 12 bytes  06 00 02 00, then zeros
//	24: items, each a word type, a word length (including these four
//	    bytes), and the value:
//	    0x0C  the owner UIC
//	    0x02  02 08
//	    0x08  8 zero bytes
//	    0x09  system, owner, group, and world protection longwords
//	    0x17  4 zero bytes
//	    0x14  the volume label
//
// The protection longwords are the four fields of the volume protection
// with the unused bits set, exactly as INIT's ALLOC_SECURITY sets them.
func securityProfile(owner ondisk.Uic, protection uint16, label string) []byte {
	b := make([]byte, 24, 128)
	binary.LittleEndian.PutUint16(b[12:], 6)
	binary.LittleEndian.PutUint16(b[14:], 2)

	item := func(typ uint16, value []byte) {
		b = binary.LittleEndian.AppendUint16(b, typ)
		b = binary.LittleEndian.AppendUint16(b, uint16(4+len(value)))
		b = append(b, value...)
	}

	item(0x0C, ondisk.EncodeUic(owner))
	item(0x02, []byte{0x02, 0x08})
	item(0x08, make([]byte, 8))

	prot := make([]byte, 16)
	binary.LittleEndian.PutUint32(prot[0:], uint32(protection)&0xF|0xFFFFFFE0)
	binary.LittleEndian.PutUint32(prot[4:], uint32(protection>>4)&0xF|0xFFFFFFE0)
	binary.LittleEndian.PutUint32(prot[8:], uint32(protection>>8)&0xF|0xFFFFFFF0)
	binary.LittleEndian.PutUint32(prot[12:], uint32(protection>>12)&0xF|0xFFFFFFF0)
	item(0x09, prot)

	item(0x17, make([]byte, 4))
	item(0x14, []byte(strings.TrimRight(label, " ")))

	binary.LittleEndian.PutUint32(b[8:], uint32(len(b)))

	var check uint32
	for off := 4; off+4 <= len(b); off += 4 {
		check ^= binary.LittleEndian.Uint32(b[off:])
	}

	binary.LittleEndian.PutUint32(b[0:], check)

	return b
}
