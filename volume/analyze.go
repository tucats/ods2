package volume

import (
	"fmt"

	"github.com/tucats/ods2/ondisk"
)

// BitmapDiscrepancy is one cluster where AnalyzeDisk's computed-from-file-
// headers allocation state disagrees with what BITMAP.SYS actually records
// on disk. See AnalyzeDisk's own doc comment for how "computed" is
// derived and why a mismatch in either direction matters.
type BitmapDiscrepancy struct {
	// Cluster is the 0-based storage-bitmap cluster number this
	// discrepancy is about (see Bitmap's own doc comment on why space is
	// tracked per cluster, not per block).
	Cluster uint32

	// StartLBN and Blocks describe the same cluster in block terms --
	// StartLBN is its first logical block, Blocks its width (the volume's
	// cluster size) -- since most of this project's own vocabulary
	// (ondisk.Extent, MarkAllocated/MarkFree) works in blocks rather than
	// cluster numbers.
	StartLBN uint32
	Blocks   uint32

	// ShouldBeAllocated is what the volume's actual file headers say this
	// cluster's state ought to be. When true, BITMAP.SYS incorrectly marks
	// it free -- a corruption risk, since a future allocation could hand
	// this same space out again while a file still legitimately occupies
	// it. When false, BITMAP.SYS incorrectly marks it allocated -- merely
	// reclaimable space, not dangerous on its own.
	ShouldBeAllocated bool

	// ClaimedByFile is the file number whose header's retrieval pointers
	// claim this cluster, when ShouldBeAllocated is true (0 otherwise). If
	// more than one file's extents claim the same cluster -- itself a
	// distinct kind of corruption (double allocation) that this pass
	// doesn't separately detect -- this is simply whichever one was seen
	// first while scanning file numbers in order.
	ClaimedByFile uint32
}

// String renders one human-readable line describing the discrepancy, used
// by both ANALYZE/DISK's own command output and this package's tests.
func (d BitmapDiscrepancy) String() string {
	if d.ShouldBeAllocated {
		return fmt.Sprintf(
			"cluster %d (LBN %d-%d) is marked free but is used by file %d",
			d.Cluster, d.StartLBN, d.StartLBN+d.Blocks-1, d.ClaimedByFile)
	}
	return fmt.Sprintf(
		"cluster %d (LBN %d-%d) is marked allocated but is not used by any file",
		d.Cluster, d.StartLBN, d.StartLBN+d.Blocks-1)
}

// DiskReport is AnalyzeDisk's (and RepairDisk's) result: every discrepancy
// found between BITMAP.SYS's actual on-disk bits and what the volume's own
// file headers imply, in ascending cluster order.
type DiskReport struct {
	TotalClusters uint32
	Discrepancies []BitmapDiscrepancy
}

// Clean reports whether the analysis found no discrepancies at all.
func (r *DiskReport) Clean() bool {
	return len(r.Discrepancies) == 0
}

// AnalyzeDisk reviews dev's storage bitmap (BITMAP.SYS) for consistency
// with the volume's actual file allocations, without changing anything on
// disk -- the read-only half of the ANALYZE/DISK command (analyze.go,
// package session). Unlike OpenBitmap, this works against a read-only
// mounted device too; only RepairDisk needs write access.
//
// Background on the approach: every block a file legitimately occupies is
// described somewhere in that file's own on-disk retrieval pointers (see
// ondisk.FileHeader.RetrievalPointers) -- including a "file" that's really
// an extension-header segment rather than a primary header, since a
// segment occupies its own header slot (its own file number) with its own
// retrieval-pointer map, exactly like any other file (see writeheader.go's
// linkNewExtensionSegment). So walking every file number from 1 to
// HomeBlock.MaxFiles, decoding whichever slots currently hold a genuinely
// in-use header, and collecting each one's own retrieval pointers -- with
// no need to separately chase any file's ExtensionFid chain -- visits
// every block any file (primary or extension segment alike) actually
// claims, exactly once each. Comparing the resulting "should be allocated"
// picture against BITMAP.SYS's real bits (ondisk.BitmapTest) is then a
// direct, cluster-by-cluster comparison.
//
// One region needs accounting for separately: the boot block (LBN 0) and
// the volume's home block (HomeBlock.HomeLBN) exist before any file does,
// so no file's retrieval pointers ever claim them -- but a correctly
// initialized volume's bitmap still (correctly) marks them allocated (see
// Initialize's own reservedExtent). Without treating this region as
// unconditionally reserved, AnalyzeDisk would misreport it as reclaimable
// on every single volume. A real volume's alternate/backup home block and
// index file copies (HomeBlock.AlternateHomeLBN/AlternateIndexLBN), where
// present, are NOT currently accounted for the same way -- this project's
// own Initialize never writes them, so no test fixture exercises that gap,
// but it's a known limitation worth revisiting if ANALYZE/DISK is ever run
// against a real volume that has them.
func AnalyzeDisk(dev *Device) (*DiskReport, error) {
	if dev.IndexFile == nil {
		return nil, fmt.Errorf("volume: ANALYZE/DISK: device has not been mounted")
	}

	bm, err := loadBitmap(dev)
	if err != nil {
		return nil, fmt.Errorf("volume: ANALYZE/DISK: %w", err)
	}

	computed := make([]bool, bm.totalClusters)
	claimedBy := make([]uint32, bm.totalClusters)

	markReserved(computed, bm.clusterSize, bm.totalClusters, 0, dev.Home.HomeLBN, 0, claimedBy)

	buf := make([]byte, ondisk.BlockSize)
	for fileNumber := uint32(1); fileNumber <= dev.Home.MaxFiles; fileNumber++ {
		vbn := fileHeaderVBN(dev.Home, fileNumber)
		if err := dev.IndexFile.ReadBlock(vbn, buf); err != nil {
			return nil, fmt.Errorf("volume: ANALYZE/DISK: reading header slot for file %d: %w", fileNumber, err)
		}

		header, err := ondisk.DecodeFileHeader(buf)
		if err != nil || header.Fid.Number() != fileNumber {
			// Not a genuinely in-use header for this slot -- either
			// genuinely free (an all-zero slot decodes without error, but
			// its Fid.Number() is 0, never matching a real fileNumber), or
			// a checksum mismatch, which readFileHeaderViaIndex's own
			// stale-Fid safeguard exists for elsewhere but isn't this
			// pass's concern: a header slot that doesn't validate as file
			// fileNumber simply contributes no claimed blocks.
			continue
		}

		extents, err := header.RetrievalPointers()
		if err != nil {
			return nil, fmt.Errorf("volume: ANALYZE/DISK: decoding retrieval pointers for file %d: %w", fileNumber, err)
		}
		for _, e := range extents {
			if e.Count == 0 {
				continue
			}
			markReserved(computed, bm.clusterSize, bm.totalClusters, e.StartLBN, e.StartLBN+e.Count-1, fileNumber, claimedBy)
		}
	}

	report := &DiskReport{TotalClusters: bm.totalClusters}
	for cluster := uint32(0); cluster < bm.totalClusters; cluster++ {
		shouldBeAllocated := computed[cluster]
		isAllocated := !ondisk.BitmapTest(bm.bits, cluster) // BitmapTest: set bit means FREE
		if shouldBeAllocated == isAllocated {
			continue
		}

		report.Discrepancies = append(report.Discrepancies, BitmapDiscrepancy{
			Cluster:           cluster,
			StartLBN:          cluster * bm.clusterSize,
			Blocks:            bm.clusterSize,
			ShouldBeAllocated: shouldBeAllocated,
			ClaimedByFile:     claimedBy[cluster],
		})
	}

	return report, nil
}

// markReserved marks every cluster covering LBNs startLBN..endLBN
// (inclusive) as allocated in computed, recording fileNumber as the first
// claimant of each such cluster in claimedBy (left alone if already
// non-zero, so the first file seen wins). Clusters at or beyond
// totalClusters are silently ignored, the same tolerance OpenBitmap itself
// applies to the bitmap's own trailing padding bits.
func markReserved(computed []bool, clusterSize, totalClusters, startLBN, endLBN, fileNumber uint32, claimedBy []uint32) {
	startCluster := startLBN / clusterSize
	endCluster := endLBN / clusterSize

	for c := startCluster; c <= endCluster && c < totalClusters; c++ {
		computed[c] = true
		if claimedBy[c] == 0 {
			claimedBy[c] = fileNumber
		}
	}
}

// RepairDisk runs the same analysis AnalyzeDisk does, then -- if it found
// any discrepancies -- rewrites BITMAP.SYS to match the computed-correct
// state and flushes it (Bitmap.Flush), via dev.Bitmap() (the same cached
// Bitmap instance any other write-path operation on dev would see).
// Requires dev to be open for write; dev.Bitmap() (and so RepairDisk)
// fails with a clear error otherwise, the same way every other write-path
// entry point in this package does.
//
// The returned report describes what was found (and, since every
// discrepancy found is one this function corrects, what was fixed) --
// mirroring AnalyzeDisk's own report exactly, so a caller can print
// identical diagnostic output regardless of whether /REPAIR was given.
func RepairDisk(dev *Device) (*DiskReport, error) {
	report, err := AnalyzeDisk(dev)
	if err != nil {
		return nil, err
	}
	if report.Clean() {
		return report, nil
	}

	bm, err := dev.Bitmap()
	if err != nil {
		return nil, fmt.Errorf("volume: ANALYZE/DISK /REPAIR: %w", err)
	}

	for _, d := range report.Discrepancies {
		e := ondisk.Extent{StartLBN: d.StartLBN, Count: d.Blocks}
		if d.ShouldBeAllocated {
			err = bm.MarkAllocated(e)
		} else {
			err = bm.MarkFree(e)
		}
		if err != nil {
			return nil, fmt.Errorf("volume: ANALYZE/DISK /REPAIR: correcting cluster %d: %w", d.Cluster, err)
		}
	}

	if err := bm.Flush(); err != nil {
		return nil, fmt.Errorf("volume: ANALYZE/DISK /REPAIR: %w", err)
	}

	return report, nil
}
