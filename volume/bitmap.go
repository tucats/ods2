package volume

import (
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// Bitmap is an in-memory cache of one device's storage (free-space)
// bitmap, BITMAP.SYS — see docs/PHASE-02.md's "Two bitmaps, not one" for
// why this is one of exactly two bitmaps an ODS-2 volume has, and why this
// one (as opposed to the index-file header-slot bitmap) is the one that
// tracks free *data* space.
//
// Background for readers new to this on-disk format: VMS never allocates
// disk space one block at a time. Instead, every device has a fixed
// "cluster size" (HomeBlock.ClusterSize, in blocks), and space is always
// allocated in whole clusters — so BITMAP.SYS records free/allocated
// status per *cluster*, not per block. A cluster size of 1 makes clusters
// and blocks the same thing; larger cluster sizes (common on real
// hardware, to keep the bitmap itself small on large volumes) mean each
// bit covers several consecutive blocks at once.
//
// BITMAP.SYS itself is an ordinary file (reserved file number 2,
// ondisk.BitmapFileFid) like any other: its first block is a
// StorageControlBlock (see ondisk/scb.go) recording the volume's total
// size and cluster size, and the blocks after that hold the actual
// free/allocated bits, packed by ondisk.BitmapTest/BitmapSet/BitmapClear
// (one bit per cluster, cluster 0 first).
//
// OpenBitmap reads all of this into memory once. FindFree/MarkAllocated/
// MarkFree then operate purely in memory — nothing is written back to the
// device until Flush is called. This matches this project's whole-phase
// caching strategy (see docs/PHASE-02.md's "Caching strategy" section):
// unlike the reference implementation's implicit, LRU-eviction-driven
// write-back, every mutating operation in this project flushes its own
// dirty bitmap state before returning, so nothing is ever left dirty in
// memory hoping a later operation picks it up.
type Bitmap struct {
	dev       *Device
	file      *File
	container diskimage.WritableContainer

	scb         ondisk.StorageControlBlock
	clusterSize uint32

	// totalClusters is how many clusters the volume actually has
	// (ceil(scb.VolumeSize / clusterSize)) — FindFree/MarkAllocated/
	// MarkFree never consider a cluster at or beyond this, even though
	// bits (below) is sized to a whole number of 512-byte blocks and so
	// may have a few extra, meaningless trailing bits as padding.
	totalClusters uint32

	// bits holds every bitmap block read from BITMAP.SYS, concatenated in
	// virtual-block order (VBN 2 first, immediately after the SCB at VBN
	// 1). Mutated in place by MarkAllocated/MarkFree.
	bits []byte

	// dirty is set by MarkAllocated/MarkFree and cleared by Flush. This
	// package doesn't track dirtiness any more finely than "the whole
	// bitmap" — Phase 2's design deliberately keeps this cache simple
	// (see the type doc comment above), and a volume's bitmap is small
	// enough that rewriting all of it on Flush is cheap.
	dirty bool
}

// OpenBitmap reads dev's storage bitmap (BITMAP.SYS) into memory, ready
// for allocation. dev must already have been mounted (so dev.IndexFile is
// populated — see Mount/bootstrapIndexFile) and opened for write: dev's
// underlying container must implement diskimage.WritableContainer, or
// OpenBitmap fails immediately rather than letting a later Flush fail
// confusingly.
func OpenBitmap(dev *Device) (*Bitmap, error) {
	container, ok := dev.Container.(diskimage.WritableContainer)
	if !ok {
		return nil, fmt.Errorf("volume: opening storage bitmap: device is not open for write")
	}

	header, err := readFileHeaderViaIndex(dev, dev.IndexFile.Extents, ondisk.BitmapFileFid)
	if err != nil {
		return nil, fmt.Errorf("volume: opening storage bitmap: %w", err)
	}

	file, err := buildFile(dev, header)
	if err != nil {
		return nil, fmt.Errorf("volume: opening storage bitmap: %w", err)
	}

	scbBuf := make([]byte, ondisk.BlockSize)
	if err := file.ReadBlock(1, scbBuf); err != nil {
		return nil, fmt.Errorf("volume: reading storage control block: %w", err)
	}
	scb, err := ondisk.DecodeStorageControlBlock(scbBuf)
	if err != nil {
		return nil, fmt.Errorf("volume: decoding storage control block: %w", err)
	}

	clusterSize := uint32(dev.Home.ClusterSize)
	if clusterSize == 0 {
		return nil, fmt.Errorf("volume: home block cluster size is zero")
	}
	if uint32(scb.ClusterSize) != clusterSize {
		return nil, fmt.Errorf(
			"volume: storage control block cluster size (%d) does not match home block's (%d)",
			scb.ClusterSize, clusterSize)
	}

	// One bit per cluster, packed 8 to a byte (see ondisk.BitmapTest),
	// rounded up to whole 512-byte blocks -- BITMAP.SYS, like every ODS-2
	// file, is only ever allocated in whole blocks, so the bits for the
	// volume's last few clusters typically share a block with some
	// trailing, meaningless padding bits.
	totalClusters := (scb.VolumeSize + clusterSize - 1) / clusterSize
	bitmapBytes := (totalClusters + 7) / 8
	bitmapBlocks := (bitmapBytes + ondisk.BlockSize - 1) / ondisk.BlockSize

	bits := make([]byte, bitmapBlocks*ondisk.BlockSize)
	buf := make([]byte, ondisk.BlockSize)
	for i := uint32(0); i < bitmapBlocks; i++ {
		vbn := 2 + i // VBN 1 is the SCB; the bitmap bits start at VBN 2.
		if err := file.ReadBlock(vbn, buf); err != nil {
			return nil, fmt.Errorf("volume: reading storage bitmap block (VBN %d): %w", vbn, err)
		}
		copy(bits[i*ondisk.BlockSize:], buf)
	}

	return &Bitmap{
		dev:           dev,
		file:          file,
		container:     container,
		scb:           scb,
		clusterSize:   clusterSize,
		totalClusters: totalClusters,
		bits:          bits,
	}, nil
}

// FindFree returns one contiguous extent of exactly clusters free
// clusters, or an error if the volume has no free run that long.
//
// This is a single deterministic left-to-right scan of the whole bitmap
// (first-fit), not the reference implementation's hint-based "start near
// the caller's last allocation" search -- see docs/PHASE-02.md's "what
// we're deliberately not porting" table for why: the reference's approach
// doesn't wrap around, so it can spuriously miss free space that exists
// earlier on the volume, and with no concurrent writers here there's no
// locality benefit worth that risk. A caller needing more space than the
// largest available free run is expected to call FindFree again for a
// smaller amount and accept fragmentation (see the type doc comment);
// this method never tries to satisfy a request from more than one run.
func (bm *Bitmap) FindFree(clusters uint32) (ondisk.Extent, error) {
	if clusters == 0 {
		return ondisk.Extent{}, fmt.Errorf("volume: FindFree requires at least one cluster, got 0")
	}

	var run uint32
	for c := uint32(0); c < bm.totalClusters; c++ {
		if ondisk.BitmapTest(bm.bits, c) {
			run++
			if run == clusters {
				start := c + 1 - clusters
				return ondisk.Extent{
					Count:    clusters * bm.clusterSize,
					StartLBN: start * bm.clusterSize,
				}, nil
			}
		} else {
			run = 0
		}
	}

	return ondisk.Extent{}, fmt.Errorf("volume: no free run of %d cluster(s) found", clusters)
}

// clusterRange converts an extent expressed in blocks (as every other
// package in this project works with them) into the cluster range it
// covers, validating that it's genuinely cluster-aligned -- space is only
// ever allocated a whole cluster at a time, so an extent that isn't a
// whole, aligned run of clusters cannot have come from this bitmap's own
// FindFree, and almost certainly indicates a caller bug.
func (bm *Bitmap) clusterRange(e ondisk.Extent) (start, count uint32, err error) {
	if e.Count == 0 {
		return 0, 0, fmt.Errorf("extent has a zero block count")
	}
	if e.StartLBN%bm.clusterSize != 0 {
		return 0, 0, fmt.Errorf("extent start LBN %d is not aligned to the volume's cluster size (%d)", e.StartLBN, bm.clusterSize)
	}
	if e.Count%bm.clusterSize != 0 {
		return 0, 0, fmt.Errorf("extent block count %d is not a whole number of clusters (cluster size %d)", e.Count, bm.clusterSize)
	}

	start = e.StartLBN / bm.clusterSize
	count = e.Count / bm.clusterSize
	if start+count > bm.totalClusters {
		return 0, 0, fmt.Errorf("extent covers clusters %d-%d, beyond the volume's %d cluster(s)", start, start+count-1, bm.totalClusters)
	}
	return start, count, nil
}

// MarkAllocated marks every cluster e covers as allocated (in memory
// only -- see Flush) and marks the bitmap dirty.
func (bm *Bitmap) MarkAllocated(e ondisk.Extent) error {
	start, count, err := bm.clusterRange(e)
	if err != nil {
		return fmt.Errorf("volume: MarkAllocated: %w", err)
	}

	for c := start; c < start+count; c++ {
		ondisk.BitmapClear(bm.bits, c)
	}
	bm.dirty = true
	return nil
}

// MarkFree marks every cluster e covers as free (in memory only -- see
// Flush) and marks the bitmap dirty.
func (bm *Bitmap) MarkFree(e ondisk.Extent) error {
	start, count, err := bm.clusterRange(e)
	if err != nil {
		return fmt.Errorf("volume: MarkFree: %w", err)
	}

	for c := start; c < start+count; c++ {
		ondisk.BitmapSet(bm.bits, c)
	}
	bm.dirty = true
	return nil
}

// Flush writes every in-memory bitmap block back to BITMAP.SYS if the
// bitmap has been mutated since the last Flush (or since OpenBitmap, if
// Flush has never been called), and clears the dirty flag. Calling Flush
// on a clean bitmap is a cheap no-op -- it does not re-write anything.
//
// The StorageControlBlock (VBN 1) is never rewritten here: this package's
// decoded ondisk.StorageControlBlock doesn't model a running free-cluster
// count (the reference implementation's update_freecount() derives one on
// demand rather than storing it persistently), so there is nothing in it
// that a bitmap mutation would ever need to update.
func (bm *Bitmap) Flush() error {
	if !bm.dirty {
		return nil
	}

	blocks := uint32(len(bm.bits)) / ondisk.BlockSize
	buf := make([]byte, ondisk.BlockSize)
	for i := uint32(0); i < blocks; i++ {
		vbn := 2 + i // VBN 1 is the SCB; the bitmap bits start at VBN 2.
		lbn, err := resolveExtentLBN(bm.file.Extents, vbn)
		if err != nil {
			return fmt.Errorf("volume: flushing storage bitmap: locating VBN %d: %w", vbn, err)
		}

		copy(buf, bm.bits[i*ondisk.BlockSize:(i+1)*ondisk.BlockSize])
		if err := bm.container.WriteBlock(lbn, buf); err != nil {
			return fmt.Errorf("volume: flushing storage bitmap: writing LBN %d: %w", lbn, err)
		}
	}

	bm.dirty = false
	return nil
}
