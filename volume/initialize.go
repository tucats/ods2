package volume

import (
	"fmt"
	"time"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
)

// defaultFileProtection is the protection mask a freshly initialized
// volume's HomeBlock.FileProtection (and therefore every reserved file's
// own FileProtection, copied from it -- see writeReservedFile) gets when
// InitializeOptions.FileProtection is left zero. 0xFA00 is the value this
// project's own real-volume ground truth (testdata/rq0-ra92.dsk) shows
// most of ITS reserved files actually carrying.
const defaultFileProtection = 0xFA00

// InitializeOptions bundles the small set of caller-chosen parameters
// Initialize needs to build a fresh volume; every field is optional (its
// zero value selects a documented default), matching how little real
// VMS's own INITIALIZE command actually asks for.
type InitializeOptions struct {
	// Label becomes the volume's HomeBlock.VolumeName. Defaults to
	// "NONAME" if empty. At most 12 bytes (HomeBlock's fixed on-disk
	// field width) -- a longer label is reported as an error by
	// EncodeHomeBlock, not silently truncated.
	Label string

	// Owner becomes HomeBlock.VolumeOwner, the UIC every reserved file's
	// own Owner field is copied from -- the same home-block-derived
	// convention CreateHeader (writeheader.go) uses for files created
	// after Initialize. Defaults to [1,1] if left as the zero Uic{}.
	Owner ondisk.Uic

	// FileProtection becomes HomeBlock.FileProtection, the default
	// protection mask new files inherit. Defaults to defaultFileProtection
	// if left zero.
	FileProtection uint16

	// ClusterSize is the volume's allocation unit, in blocks (see
	// Bitmap's own doc comment on why space is always allocated in whole
	// clusters, never single blocks). Defaults to 1 if zero, which keeps
	// every LBN/cluster computation in Initialize trivial (a cluster IS a
	// block) -- a fine default for the small, synthetic-or-modest volumes
	// this project's own INITIALIZE is actually expected to build.
	ClusterSize uint16

	// MaxFiles is the volume's total file-header-slot capacity (INDEXF.
	// SYS's own size, in header slots), reserved-file slots included.
	// Defaults to a value scaled from the container's size if zero -- see
	// defaultMaxFiles.
	MaxFiles uint32
}

// reservedFile describes one entry of the fixed, nine-file reserved-file
// table every ODS-2 volume's master file directory lists -- see
// ondisk.ReservedFileCount's own doc comment for how this table's exact
// shape (which names go at which file numbers) was confirmed against a
// real, actively-used OpenVMS volume rather than guessed from the C
// reference implementation, which never names any of these files at all.
type reservedFile struct {
	fid    ondisk.Fid
	name   string
	chars  uint32
	format ondisk.RecordFormat
}

// reservedFiles is the complete reserved-file table, in file-number order.
// FileCharacteristics and RecordFormat values for the three files that
// have any (INDEXF.SYS, BITMAP.SYS, 000000.DIR) match exactly what
// testdata/rq0-ra92.dsk's own real headers carry, confirmed the same way
// as the rest of this table -- not this project's own invention.
var reservedFiles = []reservedFile{
	{ondisk.IndexFileFid, "INDEXF.SYS", ondisk.FchContigB, ondisk.RecordFormatFixed},
	{ondisk.BitmapFileFid, "BITMAP.SYS", ondisk.FchContig, ondisk.RecordFormatFixed},
	{ondisk.BadBlockFileFid, "BADBLK.SYS", 0, ondisk.RecordFormatFixed},
	{ondisk.MasterFileDirectoryFid, "000000.DIR", ondisk.FchDirectory, ondisk.RecordFormatVariable},
	{ondisk.CoreImageFileFid, "CORIMG.SYS", 0, ondisk.RecordFormatFixed},
	{ondisk.VolumeSetFileFid, "VOLSET.SYS", 0, ondisk.RecordFormatFixed},
	{ondisk.ContinuationFileFid, "CONTIN.SYS", 0, ondisk.RecordFormatFixed},
	{ondisk.BackupFileFid, "BACKUP.SYS", 0, ondisk.RecordFormatFixed},
	{ondisk.BadBlockLogFileFid, "BADLOG.SYS", 0, ondisk.RecordFormatFixed},
}

// reservedFileSpec returns fid's entry in reservedFiles. It panics if fid
// isn't one of the nine reserved files, which would be a bug in Initialize
// itself (every caller below passes a fixed, known-reserved Fid), not
// something a caller of Initialize could ever trigger.
func reservedFileSpec(fid ondisk.Fid) reservedFile {
	for _, spec := range reservedFiles {
		if spec.fid.Number() == fid.Number() {
			return spec
		}
	}
	panic(fmt.Sprintf("volume: no reserved-file table entry for file number %d", fid.Number()))
}

// defaultMaxFiles picks a MaxFiles value scaled from the volume's size
// when InitializeOptions.MaxFiles is left zero: one header slot per 8
// blocks, with a floor that always leaves a little room for user files
// beyond the 9 reserved ones, even on a very small volume. This is a
// simple heuristic, not a reproduction of any real VMS sizing formula --
// docs/PHASE-02.md subtask 12 explicitly scopes the exact numbers here as
// "TBD", to be refined later against real-volume experience if this
// default ever proves too small or too wasteful in practice.
func defaultMaxFiles(blocks uint32) uint32 {
	const minimum = ondisk.ReservedFileCount + 8
	if n := blocks / 8; n > minimum {
		return n
	}
	return minimum
}

// initLayout is every LBN/block-count Initialize's brute-force layout
// step computes up front, before anything is written -- see
// computeLayout.
type initLayout struct {
	indexBitmapLBN  uint32 // where INDEXF.SYS's own header-slot bitmap starts
	indexBitmapSize uint16 // its size, in blocks
	headerAreaLBN   uint32 // where INDEXF.SYS's file-header slots start (file 1's own slot)
	indexFileBlocks uint32 // INDEXF.SYS's total size: index bitmap + header area

	bitmapSCBLBN  uint32 // BITMAP.SYS's StorageControlBlock (VBN 1)
	bitmapBlocks  uint32 // number of blocks holding BITMAP.SYS's actual free/allocated bits
	bitmapDataLBN uint32 // where those bits start (VBN 2)

	mfdLBN uint32 // 000000.DIR's single data block

	// reservedClusters is how many clusters, starting from cluster 0 (LBN
	// 0, the boot block), the whole reserved system area above occupies --
	// rounded up to a whole number of clusters so it can be marked
	// allocated in the storage bitmap with a single MarkAllocated call
	// (see Bitmap.MarkAllocated's cluster-alignment requirement).
	reservedClusters uint32
}

// computeLayout lays out every fixed structure a freshly initialized
// volume needs -- home block, INDEXF.SYS's own bitmap and header area,
// BITMAP.SYS's data, and 000000.DIR's one data block -- contiguously from
// the start of the device, entirely by direct arithmetic rather than
// through any allocator (there is nothing to allocate FROM yet; see
// Initialize's own doc comment). This mirrors how the reference
// implementation's own analogues locate INDEXF.SYS's fixed structures,
// generalized to also decide where those structures go in the first
// place, which nothing in the reference implementation ever needs to do
// (it assumes a volume was already formatted by real VMS -- see
// docs/PHASE-02.md subtask 12's own introduction).
func computeLayout(blocks uint32, clusterSize uint16, maxFiles uint32) (initLayout, error) {
	var l initLayout

	// LBN 0 is the boot block (left alone, but still reserved); LBN 1 is
	// the home block; INDEXF.SYS's own data starts right after both.
	l.indexBitmapLBN = 2

	indexBitmapBits := maxFiles
	indexBitmapBytes := (indexBitmapBits + 7) / 8
	indexBitmapBlocks := (indexBitmapBytes + ondisk.BlockSize - 1) / ondisk.BlockSize
	if indexBitmapBlocks == 0 {
		indexBitmapBlocks = 1
	}
	if indexBitmapBlocks > 0xFFFF {
		return initLayout{}, fmt.Errorf(
			"MaxFiles %d needs a %d-block index bitmap, too large for HomeBlock.IndexBitmapSize's 16-bit field",
			maxFiles, indexBitmapBlocks)
	}
	l.indexBitmapSize = uint16(indexBitmapBlocks)

	// This is exactly bootstrapIndexFile's own read-side formula
	// (IndexBitmapLBN + IndexBitmapSize) -- see Initialize's doc comment
	// on why matching it is what makes a freshly initialized volume
	// mountable at all.
	l.headerAreaLBN = l.indexBitmapLBN + uint32(l.indexBitmapSize)
	l.indexFileBlocks = uint32(l.indexBitmapSize) + maxFiles

	l.bitmapSCBLBN = l.headerAreaLBN + maxFiles

	// Independent of the layout above: how big BITMAP.SYS's own bits
	// region needs to be depends only on the volume's total size and
	// cluster size, via exactly the same arithmetic OpenBitmap (bitmap.go)
	// uses when it later reads BITMAP.SYS back -- computing it any other
	// way here would risk the two disagreeing about how many blocks
	// BITMAP.SYS's data actually occupies.
	totalClusters := (blocks + uint32(clusterSize) - 1) / uint32(clusterSize)
	bitmapBytes := (totalClusters + 7) / 8
	bitmapBlocks := (bitmapBytes + ondisk.BlockSize - 1) / ondisk.BlockSize
	if bitmapBlocks == 0 {
		bitmapBlocks = 1
	}
	l.bitmapBlocks = bitmapBlocks
	l.bitmapDataLBN = l.bitmapSCBLBN + 1

	l.mfdLBN = l.bitmapDataLBN + l.bitmapBlocks

	reservedBlocksTotal := l.mfdLBN + 1 // LBNs 0..mfdLBN inclusive
	if reservedBlocksTotal > blocks {
		return initLayout{}, fmt.Errorf(
			"volume has %d block(s), but the minimal reserved layout needs %d (MaxFiles=%d, ClusterSize=%d); use a larger volume or a smaller MaxFiles",
			blocks, reservedBlocksTotal, maxFiles, clusterSize)
	}
	l.reservedClusters = (reservedBlocksTotal + uint32(clusterSize) - 1) / uint32(clusterSize)

	return l, nil
}

// writeReservedFile encodes and writes one reserved file's header, via the
// same private writeHeader (writeheader.go) CreateHeader/Extend build on --
// reused here rather than duplicated, since by the time this is called for
// anything other than INDEXF.SYS itself, dev.IndexFile is already resolved
// (see Initialize) and writeHeader's usual header-slot lookup works
// exactly as it does for any other write-path operation.
//
// extent is nil for a genuinely empty reserved file (no data blocks
// allocated at all -- every reserved file except INDEXF.SYS, BITMAP.SYS,
// and 000000.DIR, each of which Initialize builds directly instead of
// through this helper's single-extent shape).
func writeReservedFile(dev *Device, container diskimage.WritableContainer, spec reservedFile, extent *ondisk.Extent, owner ondisk.Uic, protection uint16, now vmstime.VMSTime) error {
	var mapBytes []byte
	var blocks uint32
	if extent != nil {
		var err error
		mapBytes, err = ondisk.EncodeRetrievalPointers([]ondisk.Extent{*extent})
		if err != nil {
			return fmt.Errorf("encoding %s retrieval pointers: %w", spec.name, err)
		}
		blocks = extent.Count
	}

	// EndOfFileBlock = HighestBlock+1, FirstFreeByte = 0, HighWaterMark =
	// HighestBlock+1 for every reserved file, empty ones included --
	// exactly the "whole allocation counts as used content" convention
	// this codebase already established for a fully-written directory
	// block (Directory.recordUsedBlocks) and confirmed here against
	// testdata/rq0-ra92.dsk's own reserved files, every one of which
	// (including its genuinely empty ones, HighestBlock 0) follows this
	// same EndOfFileBlock/HighWaterMark-equals-HighestBlock-plus-one
	// pattern.
	h := ondisk.FileHeader{
		StructureLevel: ondisk.FileHeaderStructureLevel,
		Fid:            spec.fid,
		RecordAttributes: ondisk.RecAttr{
			Format:         spec.format,
			HighestBlock:   blocks,
			EndOfFileBlock: blocks + 1,
		},
		FileCharacteristics: spec.chars,
		Owner:               owner,
		FileProtection:      protection,
		// Every reserved file's parent is the master file directory --
		// including 000000.DIR itself, whose Backlink is self-referential
		// on a real volume (confirmed against testdata/rq0-ra92.dsk).
		Backlink:      ondisk.MasterFileDirectoryFid,
		HighWaterMark: blocks + 1,
	}
	areas := ondisk.FileHeaderAreas{
		Ident: &ondisk.Ident{
			Filename:     spec.name,
			Revision:     1,
			CreationDate: now,
			RevisionDate: now,
		},
		MapBytes: mapBytes,
	}

	_, err := writeHeader(dev, container, spec.fid.Number(), h, areas)
	if err != nil {
		return fmt.Errorf("writing %s header: %w", spec.name, err)
	}
	return nil
}

// Initialize builds a minimal but valid, freshly formatted ODS-2 volume on
// c: a home block, INDEXF.SYS (with a storage bitmap and header-slot
// bitmap of its own), BITMAP.SYS, and the rest of the nine-file reserved
// set (docs/PHASE-02.md subtask 12's table, confirmed against
// testdata/rq0-ra92.dsk -- see ondisk.ReservedFileCount), all listed by
// name in a freshly built master file directory (000000.DIR). c is
// typically freshly created via diskimage.Create, zero-filled and sized to
// however many blocks the new volume should have.
//
// Nothing in Phase 1 or the C reference implementation this project is
// based on does this: the reference assumes a volume was already
// formatted by real VMS (see the C-reference report cited in
// docs/PHASE-02.md), and Phase 1 only ever reads an already-valid volume.
// Building one from nothing has the same chicken-and-egg problem
// bootstrapIndexFile (volume.go) solves on the read side -- nothing can
// look a file's header up through INDEXF.SYS before INDEXF.SYS's own
// header has been found some other way -- broken the same way here, in
// reverse: every fixed structure's location is computed directly
// (computeLayout) rather than allocated through Bitmap/IndexBitmap (which
// don't exist yet either), and INDEXF.SYS's own primary header is written
// straight to the fixed, computable LBN bootstrapIndexFile always reads it
// from, rather than through the ordinary header-slot allocator. Only once
// that's done -- and dev.IndexFile can be resolved from it exactly the way
// Mount's own bootstrapIndexFile does -- do the rest of the reserved
// files' headers get written through this package's ordinary write-path
// machinery (writeHeader, the same private helper CreateHeader/Extend use).
//
// The home block itself is written last, once everything it points at
// (INDEXF.SYS, BITMAP.SYS, 000000.DIR, and both bitmaps' own accounting of
// the space all of that consumes) is already valid and self-consistent on
// disk -- so a caller that fails or is interrupted partway through never
// leaves behind a container whose home block looks valid but points at an
// incomplete volume.
func Initialize(c diskimage.WritableContainer, opts InitializeOptions) error {
	blocks := c.Blocks()

	clusterSize := opts.ClusterSize
	if clusterSize == 0 {
		clusterSize = 1
	}

	maxFiles := opts.MaxFiles
	if maxFiles == 0 {
		maxFiles = defaultMaxFiles(blocks)
	}
	if maxFiles < ondisk.ReservedFileCount {
		return fmt.Errorf("volume: Initialize: MaxFiles (%d) is smaller than the %d reserved file slots every volume needs",
			maxFiles, ondisk.ReservedFileCount)
	}

	owner := opts.Owner
	if owner == (ondisk.Uic{}) {
		owner = ondisk.Uic{Group: 1, Member: 1}
	}

	protection := opts.FileProtection
	if protection == 0 {
		protection = defaultFileProtection
	}

	label := opts.Label
	if label == "" {
		label = "NONAME"
	}

	layout, err := computeLayout(blocks, clusterSize, maxFiles)
	if err != nil {
		return fmt.Errorf("volume: Initialize: %w", err)
	}

	now := vmstime.FromTime(time.Now())

	// The home block is built entirely in memory here, and not written to
	// c until the very end (see this function's own doc comment) -- dev
	// below carries it purely as in-memory state every step needs to
	// consult (cluster size, where things live, ...), not as a claim that
	// it's already on disk.
	home := ondisk.HomeBlock{
		HomeLBN:                 1,
		StructureLevel:          ondisk.FileHeaderStructureLevel,
		ClusterSize:             clusterSize,
		IndexBitmapVBN:          1,
		IndexBitmapLBN:          layout.indexBitmapLBN,
		MaxFiles:                maxFiles,
		IndexBitmapSize:         layout.indexBitmapSize,
		ReservedFiles:           ondisk.ReservedFileCount,
		RelativeVolumeNumber:    1,
		VolumeOwner:             owner,
		FileProtection:          protection,
		CreationDate:            now,
		RevisionDate:            now,
		DefaultExtendSize:       5,
		WindowSize:              7,
		DirectoryPreAccessLimit: 3,
		StructureName:           ondisk.HomeBlockFormatID,
		VolumeName:              label,
		Format:                  ondisk.HomeBlockFormatID,
	}

	dev := &Device{Container: c, Home: home, Rvn: 1}

	// Step 2 (docs/PHASE-02.md subtask 12): write INDEXF.SYS's own primary
	// header directly, mirroring bootstrapIndexFile's read-side lookup
	// (IndexBitmapLBN + IndexBitmapSize) in reverse -- see this function's
	// own doc comment. INDEXF.SYS's single retrieval-pointer extent covers
	// its ENTIRE reserved area (the index bitmap and the header slots
	// alike), matching the convention this package's own test fixtures
	// already use (see writeheader_test.go's newWritableHeaderTestVolume).
	indexExtent := ondisk.Extent{Count: layout.indexFileBlocks, StartLBN: layout.indexBitmapLBN}
	indexMapBytes, err := ondisk.EncodeRetrievalPointers([]ondisk.Extent{indexExtent})
	if err != nil {
		return fmt.Errorf("volume: Initialize: encoding INDEXF.SYS retrieval pointers: %w", err)
	}
	indexSpec := reservedFileSpec(ondisk.IndexFileFid)
	indexHeader := ondisk.FileHeader{
		StructureLevel: ondisk.FileHeaderStructureLevel,
		Fid:            ondisk.IndexFileFid,
		RecordAttributes: ondisk.RecAttr{
			Format:         indexSpec.format,
			HighestBlock:   layout.indexFileBlocks,
			EndOfFileBlock: layout.indexFileBlocks + 1,
		},
		FileCharacteristics: indexSpec.chars,
		Owner:               owner,
		FileProtection:      protection,
		Backlink:            ondisk.MasterFileDirectoryFid,
		HighWaterMark:       layout.indexFileBlocks + 1,
	}
	indexHeaderBytes, err := ondisk.EncodeFileHeader(indexHeader, ondisk.FileHeaderAreas{
		Ident:    &ondisk.Ident{Filename: indexSpec.name, Revision: 1, CreationDate: now, RevisionDate: now},
		MapBytes: indexMapBytes,
	})
	if err != nil {
		return fmt.Errorf("volume: Initialize: encoding INDEXF.SYS header: %w", err)
	}
	if err := c.WriteBlock(layout.headerAreaLBN, indexHeaderBytes); err != nil {
		return fmt.Errorf("volume: Initialize: writing INDEXF.SYS header: %w", err)
	}

	decodedIndexHeader, err := ondisk.DecodeFileHeader(indexHeaderBytes)
	if err != nil {
		// Unreachable: EncodeFileHeader always produces a checksum that
		// validates against its own output.
		return fmt.Errorf("volume: Initialize: decoding just-written INDEXF.SYS header: %w", err)
	}
	indexFile, err := buildFile(dev, decodedIndexHeader)
	if err != nil {
		return fmt.Errorf("volume: Initialize: resolving INDEXF.SYS's own extents: %w", err)
	}
	dev.IndexFile = indexFile

	// From here on, dev.IndexFile is populated exactly as it would be
	// after an ordinary Mount, so every remaining reserved file's header
	// can be written through this package's ordinary writeHeader helper.

	// Step 3: the five reserved files that are genuinely empty on a fresh
	// volume (docs/PHASE-02.md subtask 12's table). INDEXF.SYS (above) and
	// BITMAP.SYS/000000.DIR (below, which need real data content) are
	// handled separately.
	for _, spec := range reservedFiles {
		switch spec.fid.Number() {
		case ondisk.IndexFileFid.Number(), ondisk.BitmapFileFid.Number(), ondisk.MasterFileDirectoryFid.Number():
			continue
		}
		if err := writeReservedFile(dev, c, spec, nil, owner, protection, now); err != nil {
			return fmt.Errorf("volume: Initialize: %w", err)
		}
	}

	// BITMAP.SYS: write its data (a StorageControlBlock plus an
	// all-clusters-free bitmap) before its header -- both need to already
	// be on disk before dev.Bitmap() (below) can open it.
	scb, err := ondisk.EncodeStorageControlBlock(ondisk.StorageControlBlock{
		ClusterSize: clusterSize,
		BlockSize:   ondisk.BlockSize,
		VolumeSize:  blocks,
	})
	if err != nil {
		return fmt.Errorf("volume: Initialize: encoding storage control block: %w", err)
	}
	if err := c.WriteBlock(layout.bitmapSCBLBN, scb); err != nil {
		return fmt.Errorf("volume: Initialize: writing storage control block: %w", err)
	}
	freeBits := make([]byte, ondisk.BlockSize)
	for i := range freeBits {
		freeBits[i] = 0xFF // every cluster free -- MarkAllocated (step 5, below) carves out the reserved prefix.
	}
	for i := uint32(0); i < layout.bitmapBlocks; i++ {
		if err := c.WriteBlock(layout.bitmapDataLBN+i, freeBits); err != nil {
			return fmt.Errorf("volume: Initialize: writing storage bitmap block %d: %w", i, err)
		}
	}
	bitmapExtent := ondisk.Extent{Count: 1 + layout.bitmapBlocks, StartLBN: layout.bitmapSCBLBN}
	if err := writeReservedFile(dev, c, reservedFileSpec(ondisk.BitmapFileFid), &bitmapExtent, owner, protection, now); err != nil {
		return fmt.Errorf("volume: Initialize: %w", err)
	}

	// 000000.DIR: write its one data block -- listing every reserved file
	// by name, via subtask 5's ondisk.EncodeDirectoryBlock (step 4) --
	// before its header.
	mfdEntries := make([]ondisk.DirEntry, 0, len(reservedFiles))
	for _, spec := range reservedFiles {
		mfdEntries = append(mfdEntries, ondisk.DirEntry{Name: spec.name, Version: 1, Fid: spec.fid})
	}
	mfdBlock, err := ondisk.EncodeDirectoryBlock(mfdEntries)
	if err != nil {
		return fmt.Errorf("volume: Initialize: encoding master file directory: %w", err)
	}
	if err := c.WriteBlock(layout.mfdLBN, mfdBlock); err != nil {
		return fmt.Errorf("volume: Initialize: writing master file directory: %w", err)
	}
	mfdExtent := ondisk.Extent{Count: 1, StartLBN: layout.mfdLBN}
	if err := writeReservedFile(dev, c, reservedFileSpec(ondisk.MasterFileDirectoryFid), &mfdExtent, owner, protection, now); err != nil {
		return fmt.Errorf("volume: Initialize: %w", err)
	}

	// Step 5: mark the reserved regions allocated in both bitmaps before
	// flushing them -- via Device.Bitmap/IndexBitmap (dismount.go) rather
	// than OpenBitmap/OpenIndexBitmap directly, so the same cached
	// instances are what a caller's own subsequent Device.Bitmap/
	// IndexBitmap calls (e.g. to create a file right after Initialize)
	// see, consistent with how every other write-path operation in this
	// package is expected to obtain them.
	bm, err := dev.Bitmap()
	if err != nil {
		return fmt.Errorf("volume: Initialize: opening storage bitmap: %w", err)
	}
	reservedExtent := ondisk.Extent{StartLBN: 0, Count: layout.reservedClusters * uint32(clusterSize)}
	if err := bm.MarkAllocated(reservedExtent); err != nil {
		return fmt.Errorf("volume: Initialize: marking reserved space allocated: %w", err)
	}
	if err := bm.Flush(); err != nil {
		return fmt.Errorf("volume: Initialize: flushing storage bitmap: %w", err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		return fmt.Errorf("volume: Initialize: opening index-file bitmap: %w", err)
	}
	for fileNumber := uint32(1); fileNumber <= ondisk.ReservedFileCount; fileNumber++ {
		if err := ib.MarkAllocated(fileNumber); err != nil {
			return fmt.Errorf("volume: Initialize: marking header slot %d allocated: %w", fileNumber, err)
		}
	}
	if err := ib.Flush(); err != nil {
		return fmt.Errorf("volume: Initialize: flushing index-file bitmap: %w", err)
	}

	// Step 6: write the home block last, once everything it points at is
	// already valid on disk.
	homeBytes, err := ondisk.EncodeHomeBlock(home)
	if err != nil {
		return fmt.Errorf("volume: Initialize: encoding home block: %w", err)
	}
	if err := c.WriteBlock(home.HomeLBN, homeBytes); err != nil {
		return fmt.Errorf("volume: Initialize: writing home block: %w", err)
	}

	return nil
}
