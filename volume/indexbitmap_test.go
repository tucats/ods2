package volume

import (
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

// Index-bitmap tests reuse file_test.go's home-block/index-file layout
// constants (testIndexBitmapLBN etc.) and fileHeaderLBN helper, adding
// MaxFiles/ReservedFiles to the home block -- the two fields FindFreeSlot
// needs that this package's other fixtures never had reason to set.
const (
	testIdxMaxFiles      = 20
	testIdxReservedFiles = 3
)

// newWritableIndexBitmapTestVolume builds and mounts a minimal writable
// volume shaped for index-bitmap testing: the same home-block/INDEXF.SYS
// layout as bitmap_test.go's newWritableTestVolume (a writable container,
// via diskimage.Create, rather than the read-only odstest.MemContainer
// used elsewhere), but with MaxFiles/ReservedFiles set.
func newWritableIndexBitmapTestVolume(t *testing.T) (*Device, diskimage.WritableContainer) {
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

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return vol.Devices[0], container
}

// setIndexBitmapBits writes the index-file bitmap region's bits directly
// (bypassing IndexBitmap itself), marking each of usedFileNumbers' slots
// "in use" (set bit) and leaving every other slot "free" (clear bit) --
// see IndexBitmap's own doc comment for why this polarity is the reverse
// of BITMAP.SYS's.
func setIndexBitmapBits(t *testing.T, container diskimage.WritableContainer, usedFileNumbers []uint32) {
	t.Helper()

	bits := make([]byte, ondisk.BlockSize*testIndexBitmapSize)
	for _, n := range usedFileNumbers {
		ondisk.BitmapSet(bits, n-1)
	}
	for i := 0; i < testIndexBitmapSize; i++ {
		lbn := uint32(testIndexBitmapLBN + i)
		if err := container.WriteBlock(lbn, bits[i*ondisk.BlockSize:(i+1)*ondisk.BlockSize]); err != nil {
			t.Fatalf("WriteBlock(index bitmap %d): %v", lbn, err)
		}
	}
}

// writeTestHeaderSlot installs a header that looks genuinely in use (a
// real, non-zero Fid -- which, via BuildFileHeaderBytes, gets a nonzero
// checksum too) at fid.Num's slot, independent of whatever the bitmap bit
// covering that slot says -- used to simulate a bitmap/reality mismatch
// for FindFreeSlot's consistency check.
func writeTestHeaderSlot(t *testing.T, container diskimage.WritableContainer, fid ondisk.Fid) {
	t.Helper()
	header := odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: fid})
	if err := container.WriteBlock(fileHeaderLBN(fid.Num), header); err != nil {
		t.Fatalf("WriteBlock(header %d): %v", fid.Num, err)
	}
}

func TestOpenIndexBitmapRejectsReadOnlyDevice(t *testing.T) {
	// newTestVolume (file_test.go) mounts an odstest.MemContainer, which
	// does not implement diskimage.WritableContainer.
	vol, _ := newTestVolume(t)

	if _, err := OpenIndexBitmap(vol.Devices[0]); err == nil {
		t.Fatal("OpenIndexBitmap on a read-only device: want error, got nil")
	}
}

func TestOpenIndexBitmapRejectsZeroMaxFiles(t *testing.T) {
	// newWritableTestVolume (bitmap_test.go) is writable but leaves
	// MaxFiles at its zero default.
	dev, _ := newWritableTestVolume(t)

	if _, err := OpenIndexBitmap(dev); err == nil {
		t.Fatal("OpenIndexBitmap with MaxFiles == 0: want error, got nil")
	}
}

func TestIndexBitmapFindFreeSlotSkipsReservedAndInUseSlots(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	// File 1 is INDEXF.SYS itself (always "in use" regardless of the
	// bitmap bit, since its header was written above); reserve slots 1-3
	// and additionally mark file 4 in use, leaving 5 as the first free
	// slot the bitmap actually has to fall through to.
	setIndexBitmapBits(t, container, []uint32{1, 2, 3, 4})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	got, err := ib.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot: %v", err)
	}
	if got != 5 {
		t.Errorf("FindFreeSlot = %d, want 5", got)
	}
}

func TestIndexBitmapFindFreeSlotDetectsInconsistency(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	// Slots 1-5 are marked in use (so FindFreeSlot's scan reaches slot 6
	// without stopping earlier); slot 6's bit says free, but its on-disk
	// header looks genuinely in use (nonzero Fid/checksum) -- a
	// bitmap/reality mismatch FindFreeSlot must catch rather than
	// silently allocating over.
	setIndexBitmapBits(t, container, []uint32{1, 2, 3, 4, 5})
	writeTestHeaderSlot(t, container, ondisk.Fid{Num: 6, Seq: 1})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	if _, err := ib.FindFreeSlot(); err == nil {
		t.Fatal("FindFreeSlot with a bitmap/header inconsistency: want error, got nil")
	}
}

func TestIndexBitmapMarkAllocatedRemovesSlotFromFutureSearches(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	if err := ib.MarkAllocated(4); err != nil {
		t.Fatalf("MarkAllocated(4): %v", err)
	}

	got, err := ib.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot after MarkAllocated(4): %v", err)
	}
	if got != 5 {
		t.Errorf("FindFreeSlot after MarkAllocated(4) = %d, want 5", got)
	}
}

func TestIndexBitmapMarkFreeAddsSlotToFutureSearches(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3, 4, 5})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	if err := ib.MarkFree(4); err != nil {
		t.Fatalf("MarkFree(4): %v", err)
	}

	got, err := ib.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot after MarkFree(4): %v", err)
	}
	if got != 4 {
		t.Errorf("FindFreeSlot after MarkFree(4) = %d, want 4", got)
	}
}

func TestIndexBitmapMarkAllocatedRejectsFileNumberZero(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	if err := ib.MarkAllocated(0); err == nil {
		t.Fatal("MarkAllocated(0): want error, got nil")
	}
}

func TestIndexBitmapMarkAllocatedRejectsOutOfRangeFileNumber(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	if err := ib.MarkAllocated(testIdxMaxFiles + 1); err == nil {
		t.Fatal("MarkAllocated beyond MaxFiles: want error, got nil")
	}
}

func TestIndexBitmapFlushIsDeferredUntilCalled(t *testing.T) {
	dev, container := newWritableIndexBitmapTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	if err := ib.MarkAllocated(4); err != nil {
		t.Fatalf("MarkAllocated(4): %v", err)
	}

	// A second, independently-opened IndexBitmap reads the SAME on-disk
	// bytes; since ib hasn't been flushed, it must still see slot 4 free.
	other, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap (second): %v", err)
	}
	got, err := other.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot on unflushed disk state: %v", err)
	}
	if got != 4 {
		t.Errorf("unflushed mutation leaked to disk: FindFreeSlot = %d, want 4", got)
	}

	if err := ib.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Now a freshly-opened IndexBitmap must see the flushed mutation:
	// slot 4 is gone, so the next free slot is 5.
	reread, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap (after flush): %v", err)
	}
	got, err = reread.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot after Flush: %v", err)
	}
	if got != 5 {
		t.Errorf("FindFreeSlot after Flush = %d, want 5", got)
	}
}
