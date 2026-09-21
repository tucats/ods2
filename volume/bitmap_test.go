package volume

import (
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

// This file's tests build a small writable volume on top of a real,
// temporary host file (via diskimage.Create), rather than the read-only
// in-memory odstest.MemContainer this package's other tests use — see
// docs/PHASE-02.md's own test plan for subtask 6, which specifically calls
// for a "synthetic diskimage.Create'd volume". Layout reuses file_test.go's
// existing home-block/index-file constants (testIndexBitmapLBN etc.) and
// its fileHeaderLBN helper, adding a BITMAP.SYS file (reserved file number
// 2) whose data — a StorageControlBlock followed by one block of bitmap
// bits — lives well outside INDEXF.SYS's own declared extent so the two
// can't collide.
const (
	testClusterSize   = 2  // blocks per allocation cluster
	testVolumeBlocks  = 20 // StorageControlBlock.VolumeSize -- 10 clusters at ClusterSize 2
	testBitmapSCBLBN  = 260
	testBitmapBitsLBN = 261
)

// newWritableTestVolume builds and mounts a minimal writable volume:  a
// home block and INDEXF.SYS, but (deliberately) no BITMAP.SYS yet --
// individual tests install one via installTestBitmap, since most of them
// want to control its starting free/allocated pattern.
func newWritableTestVolume(t *testing.T) (*Device, diskimage.WritableContainer) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.dsk")
	container, err := diskimage.Create(path, 300)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })

	mustWrite := func(lbn uint32, b []byte) {
		t.Helper()
		if err := container.WriteBlock(lbn, b); err != nil {
			t.Fatalf("WriteBlock(%d): %v", lbn, err)
		}
	}

	mustWrite(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:       1,
		Rvn:           1,
		ClusterSize:   testClusterSize,
		IdxBitmapVBN:  testIndexBitmapVBN,
		IdxBitmapLBN:  testIndexBitmapLBN,
		IdxBitmapSize: testIndexBitmapSize,
	}))

	mustWrite(testIndexBitmapLBN+testIndexBitmapSize, odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(250, testIndexBitmapLBN),
	}))

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return vol.Devices[0], container
}

// installTestBitmap writes a BITMAP.SYS onto container (already mounted as
// dev) describing a testVolumeBlocks/testClusterSize-sized volume whose
// free clusters are exactly freeClusters (every other cluster starts, and
// stays, allocated).
func installTestBitmap(t *testing.T, container diskimage.WritableContainer, freeClusters []uint32) {
	t.Helper()

	scb, err := ondisk.EncodeStorageControlBlock(ondisk.StorageControlBlock{
		ClusterSize: testClusterSize,
		VolumeSize:  testVolumeBlocks,
	})
	if err != nil {
		t.Fatalf("EncodeStorageControlBlock: %v", err)
	}
	if err := container.WriteBlock(testBitmapSCBLBN, scb); err != nil {
		t.Fatalf("WriteBlock(SCB): %v", err)
	}

	bits := make([]byte, ondisk.BlockSize)
	for _, c := range freeClusters {
		ondisk.BitmapSet(bits, c)
	}
	if err := container.WriteBlock(testBitmapBitsLBN, bits); err != nil {
		t.Fatalf("WriteBlock(bitmap bits): %v", err)
	}

	header := odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.BitmapFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(2, testBitmapSCBLBN),
	})
	if err := container.WriteBlock(fileHeaderLBN(ondisk.BitmapFileFid.Num), header); err != nil {
		t.Fatalf("WriteBlock(bitmap header): %v", err)
	}
}

// testFreeClusters is shared by several tests below: clusters 2,3,4 and
// 6,7,8,9 free; clusters 0,1,5 allocated -- giving two free runs of
// different lengths (3 and 4) to distinguish "first fit" from "best fit".
var testFreeClusters = []uint32{2, 3, 4, 6, 7, 8, 9}

func TestOpenBitmapRejectsReadOnlyDevice(t *testing.T) {
	// newTestVolume (file_test.go) mounts an odstest.MemContainer, which
	// does not implement diskimage.WritableContainer.
	vol, _ := newTestVolume(t)

	if _, err := OpenBitmap(vol.Devices[0]); err == nil {
		t.Fatal("OpenBitmap on a read-only device: want error, got nil")
	}
}

func TestOpenBitmapRejectsClusterSizeMismatch(t *testing.T) {
	dev, container := newWritableTestVolume(t)

	scb, err := ondisk.EncodeStorageControlBlock(ondisk.StorageControlBlock{
		ClusterSize: testClusterSize + 1, // deliberately mismatched
		VolumeSize:  testVolumeBlocks,
	})
	if err != nil {
		t.Fatalf("EncodeStorageControlBlock: %v", err)
	}
	if err := container.WriteBlock(testBitmapSCBLBN, scb); err != nil {
		t.Fatalf("WriteBlock(SCB): %v", err)
	}
	header := odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.BitmapFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(2, testBitmapSCBLBN),
	})
	if err := container.WriteBlock(fileHeaderLBN(ondisk.BitmapFileFid.Num), header); err != nil {
		t.Fatalf("WriteBlock(bitmap header): %v", err)
	}

	if _, err := OpenBitmap(dev); err == nil {
		t.Fatal("OpenBitmap with mismatched cluster sizes: want error, got nil")
	}
}

func TestBitmapFindFreePicksFirstFitRun(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	// The first run long enough for 3 clusters is clusters 2-4.
	got, err := bm.FindFree(3)
	if err != nil {
		t.Fatalf("FindFree(3): %v", err)
	}
	want := ondisk.Extent{Count: 3 * testClusterSize, StartLBN: 2 * testClusterSize}
	if got != want {
		t.Errorf("FindFree(3) = %+v, want %+v", got, want)
	}

	// The first run long enough for 2 clusters is still clusters 2-3 --
	// first fit, not best fit (that would pick 6-9, the tightest fit for
	// nothing this small, or 8-9, the smallest run that still fits).
	got, err = bm.FindFree(2)
	if err != nil {
		t.Fatalf("FindFree(2): %v", err)
	}
	want = ondisk.Extent{Count: 2 * testClusterSize, StartLBN: 2 * testClusterSize}
	if got != want {
		t.Errorf("FindFree(2) = %+v, want %+v", got, want)
	}
}

func TestBitmapFindFreeNoRunLongEnough(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	if _, err := bm.FindFree(5); err == nil {
		t.Fatal("FindFree(5) with no run that long: want error, got nil")
	}
}

func TestBitmapFindFreeRejectsZero(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	if _, err := bm.FindFree(0); err == nil {
		t.Fatal("FindFree(0): want error, got nil")
	}
}

func TestBitmapMarkAllocatedRemovesRunFromFutureSearches(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	// Consume the clusters 2-4 run entirely.
	if err := bm.MarkAllocated(ondisk.Extent{Count: 3 * testClusterSize, StartLBN: 2 * testClusterSize}); err != nil {
		t.Fatalf("MarkAllocated: %v", err)
	}

	got, err := bm.FindFree(2)
	if err != nil {
		t.Fatalf("FindFree(2) after MarkAllocated: %v", err)
	}
	want := ondisk.Extent{Count: 2 * testClusterSize, StartLBN: 6 * testClusterSize}
	if got != want {
		t.Errorf("FindFree(2) after consuming clusters 2-4 = %+v, want %+v (clusters 6-9)", got, want)
	}
}

func TestBitmapMarkFreeAddsRunToFutureSearches(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	// Start with only clusters 6-9 free.
	installTestBitmap(t, container, []uint32{6, 7, 8, 9})

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	if err := bm.MarkFree(ondisk.Extent{Count: 2 * testClusterSize, StartLBN: 0}); err != nil {
		t.Fatalf("MarkFree: %v", err)
	}

	got, err := bm.FindFree(2)
	if err != nil {
		t.Fatalf("FindFree(2) after MarkFree: %v", err)
	}
	want := ondisk.Extent{Count: 2 * testClusterSize, StartLBN: 0}
	if got != want {
		t.Errorf("FindFree(2) after freeing clusters 0-1 = %+v, want %+v", got, want)
	}
}

func TestBitmapMarkAllocatedRejectsMisalignedExtent(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	if err := bm.MarkAllocated(ondisk.Extent{Count: 1, StartLBN: 5}); err == nil {
		t.Fatal("MarkAllocated with a non-cluster-aligned extent: want error, got nil")
	}
}

func TestBitmapMarkAllocatedRejectsOutOfRangeExtent(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	// The volume only has 10 clusters (0-9); this extent runs off the end.
	if err := bm.MarkAllocated(ondisk.Extent{Count: 4 * testClusterSize, StartLBN: 8 * testClusterSize}); err == nil {
		t.Fatal("MarkAllocated beyond the volume's cluster count: want error, got nil")
	}
}

func TestBitmapFlushIsDeferredUntilCalled(t *testing.T) {
	dev, container := newWritableTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}
	if err := bm.MarkAllocated(ondisk.Extent{Count: 3 * testClusterSize, StartLBN: 2 * testClusterSize}); err != nil {
		t.Fatalf("MarkAllocated: %v", err)
	}

	// A second, independently-opened Bitmap reads the SAME on-disk bytes;
	// since bm hasn't been flushed, it must still see clusters 2-4 free.
	other, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap (second): %v", err)
	}
	got, err := other.FindFree(3)
	if err != nil {
		t.Fatalf("FindFree(3) on unflushed disk state: %v", err)
	}
	want := ondisk.Extent{Count: 3 * testClusterSize, StartLBN: 2 * testClusterSize}
	if got != want {
		t.Errorf("unflushed mutation leaked to disk: FindFree(3) = %+v, want %+v", got, want)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Now a freshly-opened Bitmap must see the flushed mutation: clusters
	// 2-4 are gone, leaving clusters 6-9 (a run of 4) as the only free
	// space -- enough to satisfy a request for 4, but not 5.
	reread, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap (after flush): %v", err)
	}
	if _, err := reread.FindFree(5); err == nil {
		t.Fatal("FindFree(5) after Flush: want error (only 4 free clusters remain), got a match")
	}
	got, err = reread.FindFree(4)
	if err != nil {
		t.Fatalf("FindFree(4) after Flush: %v", err)
	}
	want = ondisk.Extent{Count: 4 * testClusterSize, StartLBN: 6 * testClusterSize}
	if got != want {
		t.Errorf("FindFree(4) after Flush = %+v, want %+v (clusters 6-9 should be the only free run left)", got, want)
	}
}
