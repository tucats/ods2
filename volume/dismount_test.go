package volume

import (
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

// newWritableDismountTestVolume builds the same minimal writable volume
// shape as bitmap_test.go's newWritableTestVolume (home block + INDEXF.SYS,
// no BITMAP.SYS yet -- callers install one via installTestBitmap), but also
// returns the host path it was built on, so a test can close the container,
// reopen that same path fresh, and confirm what Dismount actually wrote to
// it -- newWritableTestVolume doesn't expose this, since none of its own
// callers need to reopen the file they just built.
func newWritableDismountTestVolume(t *testing.T) (path string, container diskimage.WritableContainer) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "dismount.dsk")
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
		MaxFiles:      testIdxMaxFiles,
		ReservedFiles: testIdxReservedFiles,
	}))

	mustWrite(testIndexBitmapLBN+testIndexBitmapSize, odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(250, testIndexBitmapLBN),
	}))

	return path, container
}

// TestDeviceBitmapCachesAcrossCalls confirms Device.Bitmap opens
// BITMAP.SYS only once per device, handing back the very same *Bitmap on
// every later call -- the property Dismount depends on to find whatever a
// session's earlier operations allocated through it.
func TestDeviceBitmapCachesAcrossCalls(t *testing.T) {
	_, container := newWritableDismountTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	dev := vol.Devices[0]

	first, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}
	second, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap (second): %v", err)
	}
	if first != second {
		t.Error("Device.Bitmap returned a different instance on the second call, want the same cached one")
	}
}

// TestDeviceIndexBitmapCachesAcrossCalls is TestDeviceBitmapCachesAcrossCalls's
// analog for the index-file header-slot bitmap.
func TestDeviceIndexBitmapCachesAcrossCalls(t *testing.T) {
	_, container := newWritableDismountTestVolume(t)

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	dev := vol.Devices[0]

	first, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap: %v", err)
	}
	second, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap (second): %v", err)
	}
	if first != second {
		t.Error("Device.IndexBitmap returned a different instance on the second call, want the same cached one")
	}
}

// TestDismountIsNoOpFlushWithoutBitmapCaches confirms Dismount is safe to
// call on a volume that never had Device.Bitmap/IndexBitmap called on it at
// all (the read-only-mount case, and a /write mount that never allocated
// anything) -- it should just close the container, not fail trying to
// flush a cache that was never opened.
func TestDismountIsNoOpFlushWithoutBitmapCaches(t *testing.T) {
	vol, _ := newTestVolume(t) // file_test.go's read-only MemContainer fixture

	if err := vol.Dismount(); err != nil {
		t.Fatalf("Dismount: %v", err)
	}
}

// TestDismountFlushesUnflushedBitmap is this subtask's primary acceptance
// test, matching docs/PHASE-02.md's own test plan for it: mount, allocate
// through Device.Bitmap() WITHOUT an intervening Flush (so this genuinely
// exercises Dismount's own flush, not something already flushed by the
// allocation itself), Dismount, then reopen the same underlying host file
// completely fresh and confirm the allocation actually reached disk.
func TestDismountFlushesUnflushedBitmap(t *testing.T) {
	path, container := newWritableDismountTestVolume(t)
	installTestBitmap(t, container, testFreeClusters)

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	dev := vol.Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}
	extent, err := bm.FindFree(1)
	if err != nil {
		t.Fatalf("FindFree: %v", err)
	}
	if err := bm.MarkAllocated(extent); err != nil {
		t.Fatalf("MarkAllocated: %v", err)
	}

	if err := vol.Dismount(); err != nil {
		t.Fatalf("Dismount: %v", err)
	}

	// Reopen the same host file completely fresh -- a brand-new container,
	// a brand-new Mount, a brand-new OpenBitmap -- so nothing here could
	// possibly be sharing in-memory state with bm above; if the allocation
	// shows up now, it's because Dismount actually wrote it to disk.
	reopened, err := diskimage.OpenWritable(path)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	vol2, err := Mount(reopened)
	if err != nil {
		t.Fatalf("Mount (reopened): %v", err)
	}
	bm2, err := OpenBitmap(vol2.Devices[0])
	if err != nil {
		t.Fatalf("OpenBitmap (reopened): %v", err)
	}

	start, count, err := bm2.clusterRange(extent)
	if err != nil {
		t.Fatalf("clusterRange: %v", err)
	}
	for c := start; c < start+count; c++ {
		if ondisk.BitmapTest(bm2.bits, c) {
			t.Errorf("cluster %d still reads as free on disk after Dismount, want allocated", c)
		}
	}
}

// TestDismountClosesContainers confirms Dismount actually closes each
// device's container, not just flushes bitmap caches -- a write to it
// afterward should fail the same way it would after calling Close directly.
func TestDismountClosesContainers(t *testing.T) {
	_, container := newWritableDismountTestVolume(t)

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	if err := vol.Dismount(); err != nil {
		t.Fatalf("Dismount: %v", err)
	}

	buf := make([]byte, ondisk.BlockSize)
	if err := container.WriteBlock(1, buf); err == nil {
		t.Error("WriteBlock after Dismount: want error (container should be closed), got nil")
	}
}
