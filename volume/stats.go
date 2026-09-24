package volume

import (
	"fmt"

	"github.com/tucats/ods2/ondisk"
)

// VolumeStats summarizes a mounted device's live space and file-count
// usage -- the numbers a display command like govax's SHOW DEVICE/FULL
// wants (docs/PHASE-22.md's "SHOW DEVICE/FULL" section, sibling project),
// not something any other part of this package currently needs to track
// on its own.
type VolumeStats struct {
	// ClusterSize and MaxFiles are read straight off dev.Home -- no I/O
	// beyond what Mount already did.
	ClusterSize uint16
	MaxFiles    uint32

	// TotalBlocks and FreeBlocks come from the storage bitmap (BITMAP.SYS).
	TotalBlocks uint32
	FreeBlocks  uint32

	// FileCount is how many of INDEXF.SYS's non-reserved header slots
	// currently hold a genuinely in-use file.
	FileCount uint32
}

// Stats computes VolumeStats for dev by scanning its storage bitmap and
// index file header slots -- entirely read-only, so (unlike OpenBitmap/
// IndexBitmap, which exist to support mutation and so require dev to be
// open for write) this works just as well against a device mounted
// read-only. It reuses the same read-only-safe building blocks
// AnalyzeDisk already relies on for exactly this reason (see loadBitmap's
// own doc comment).
func Stats(dev *Device) (VolumeStats, error) {
	if dev.IndexFile == nil {
		return VolumeStats{}, fmt.Errorf("volume: Stats: device has not been mounted")
	}

	// Prefer dev's already-open bitmap cache (dev.bitmap, populated by an
	// earlier dev.Bitmap() call from the write path -- create.go, delete.go,
	// ...) over a fresh read straight off disk. A write-path caller's own
	// allocations/frees are only visible on disk once something actually
	// Flushes them (today, only delete/purge do so explicitly, plus
	// Dismount) -- see Bitmap's own doc comment on this package's
	// caching strategy -- so reading fresh from disk here would report
	// stale free-space numbers for the whole rest of the mount session
	// after every CREATE. Falling back to loadBitmap (itself read-only
	// safe, unlike OpenBitmap) covers the read-only-mounted case, where
	// dev.bitmap is always nil because nothing can ever open it for write.
	bm := dev.bitmap
	if bm == nil {
		var err error

		bm, err = loadBitmap(dev)
		if err != nil {
			return VolumeStats{}, fmt.Errorf("volume: Stats: %w", err)
		}
	}

	var freeClusters uint32
	for c := uint32(0); c < bm.totalClusters; c++ {
		if ondisk.BitmapTest(bm.bits, c) {
			freeClusters++
		}
	}

	fileCount, err := countFiles(dev)
	if err != nil {
		return VolumeStats{}, fmt.Errorf("volume: Stats: %w", err)
	}

	return VolumeStats{
		ClusterSize: dev.Home.ClusterSize,
		MaxFiles:    dev.Home.MaxFiles,
		TotalBlocks: bm.scb.VolumeSize,
		FreeBlocks:  freeClusters * bm.clusterSize,
		FileCount:   fileCount,
	}, nil
}

// countFiles walks dev's index file header slots from just past the
// volume's reserved bookkeeping files (HomeBlock.ReservedFiles --
// INDEXF.SYS, BITMAP.SYS, and the rest of the volume's own fixed files,
// none of which are "a file" from an operator's point of view) through
// HomeBlock.MaxFiles, counting only slots that decode to a header whose
// own Fid genuinely matches the slot -- the same in-use test AnalyzeDisk's
// own header-walk uses (analyze.go), since a free slot's all-zero bytes
// decode without error but carry Fid.Number() == 0, never matching.
func countFiles(dev *Device) (uint32, error) {
	var count uint32

	buf := make([]byte, ondisk.BlockSize)
	for fileNumber := uint32(dev.Home.ReservedFiles) + 1; fileNumber <= dev.Home.MaxFiles; fileNumber++ {
		vbn := fileHeaderVBN(dev.Home, fileNumber)
		if err := dev.IndexFile.ReadBlock(vbn, buf); err != nil {
			return 0, fmt.Errorf("reading header slot for file %d: %w", fileNumber, err)
		}

		header, err := ondisk.DecodeFileHeader(buf)
		if err != nil || header.Fid.Number() != fileNumber {
			continue
		}

		count++
	}

	return count, nil
}
