package volume

import (
	"bytes"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

// freeFileStorage's tests reuse writeheader_test.go's writable fixture
// (newWritableHeaderTestVolume/installWideTestBitmap) — the same one
// TestExtend* already builds multi-segment files against — since
// exercising deallocation needs exactly the same shape of file those
// tests build to exercise allocation.

// wideFreeClusters is the total number of free clusters
// installWideTestBitmap's fixture starts with -- the number a fully,
// correctly reclaimed file's worth of allocations should let a fresh
// Bitmap find again as one contiguous run, proving every extent that was
// ever handed out has been returned.
const wideFreeClusters = whFreeClustersEnd - whFreeClustersStart

func TestFreeFileStorageReclaimsSingleSegmentFile(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "DEL.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := Extend(f, bm, ib, 5); err != nil {
		t.Fatalf("Extend(5): %v", err)
	}
	fileNumber := f.Header.Fid.Number()

	if err := freeFileStorage(dev, f.Header, bm, ib); err != nil {
		t.Fatalf("freeFileStorage: %v", err)
	}
	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	// Every cluster installWideTestBitmap started with must be free again,
	// as one contiguous run -- proving the 5 blocks Extend allocated were
	// actually returned, not merely forgotten about in memory.
	ib2, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap (fresh): %v", err)
	}
	bm2, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap (fresh): %v", err)
	}
	if _, err := bm2.FindFree(wideFreeClusters); err != nil {
		t.Errorf("FindFree(%d) after freeing: %v (extents were not fully reclaimed)", wideFreeClusters, err)
	}

	// The freed header slot must be reusable, and must be the file's own
	// former slot again: FindFreeSlot always returns the lowest-numbered
	// free slot, and nothing else was allocated in this test.
	got, err := ib2.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot after freeing: %v", err)
	}
	if got != fileNumber {
		t.Errorf("FindFreeSlot after freeing = %d, want the file's own former slot %d", got, fileNumber)
	}

	assertHeaderSlotIsZeroed(t, container, uint16(fileNumber))
}

func TestFreeFileStorageReclaimsMultiSegmentFile(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "SEG.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	// Same "extend one block at a time" trick TestExtendAllocatesExtension
	// HeaderSegmentWhenMapIsFull (writeheader_test.go) uses to force the
	// primary header's map to fill up and a second header segment to be
	// allocated -- freeFileStorage needs a real multi-segment file to prove
	// it walks the WHOLE chain, not just the primary.
	const calls = 150
	for i := 0; i < calls; i++ {
		if err := Extend(f, bm, ib, 1); err != nil {
			t.Fatalf("Extend #%d: %v", i, err)
		}
	}
	if f.Header.ExtensionFid.IsZero() {
		t.Fatal("test setup: primary header has no extension segment after enough Extend calls to fill its map area")
	}

	primaryFileNumber := f.Header.Fid.Number()
	extensionFid := f.Header.ExtensionFid

	if err := freeFileStorage(dev, f.Header, bm, ib); err != nil {
		t.Fatalf("freeFileStorage: %v", err)
	}
	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	bm2, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap (fresh): %v", err)
	}
	if _, err := bm2.FindFree(wideFreeClusters); err != nil {
		t.Errorf("FindFree(%d) after freeing: %v (extents across both segments were not fully reclaimed)", wideFreeClusters, err)
	}

	assertHeaderSlotIsZeroed(t, container, uint16(primaryFileNumber))
	assertHeaderSlotIsZeroed(t, container, uint16(extensionFid.Number()))

	// Both the primary's and the extension segment's slots must now be
	// free -- checked directly against the bitmap rather than only via
	// FindFreeSlot, since FindFreeSlot only ever returns ONE slot per call
	// and would not, by itself, prove the second slot was freed too.
	ib2, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap (fresh): %v", err)
	}
	first, err := ib2.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot (1st): %v", err)
	}
	if err := ib2.MarkAllocated(first); err != nil {
		t.Fatalf("MarkAllocated(%d): %v", first, err)
	}
	second, err := ib2.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot (2nd): %v", err)
	}
	gotSlots := map[uint32]bool{first: true, second: true}
	if !gotSlots[primaryFileNumber] || !gotSlots[extensionFid.Number()] {
		t.Errorf("free slots after freeing = {%d, %d}, want both %d (primary) and %d (extension)",
			first, second, primaryFileNumber, extensionFid.Number())
	}
}

func TestFreeFileStorageRejectsReadOnlyDevice(t *testing.T) {
	// newTestVolume (file_test.go) mounts an odstest.MemContainer, which
	// does not implement diskimage.WritableContainer.
	vol, _ := newTestVolume(t)

	err := freeFileStorage(vol.Devices[0], ondisk.FileHeader{}, nil, nil)
	if err == nil {
		t.Fatal("freeFileStorage on a read-only device: want error, got nil")
	}
}

// assertHeaderSlotIsZeroed confirms fileNum's on-disk header slot is
// genuinely all-zero bytes -- not merely that it decodes with a zero
// checksum and zero file number (which an all-zero block would share with,
// say, a header IndexBitmap.FindFreeSlot's consistency check would also
// accept), but that freeFileStorage actually wrote zeros rather than, say,
// leaving stale bytes behind that only coincidentally still checksum to 0.
func assertHeaderSlotIsZeroed(t *testing.T, container interface {
	ReadBlock(lbn uint32, buf []byte) error
}, fileNum uint16) {
	t.Helper()

	buf := make([]byte, ondisk.BlockSize)
	if err := container.ReadBlock(fileHeaderLBN(fileNum), buf); err != nil {
		t.Fatalf("ReadBlock(header slot for file %d): %v", fileNum, err)
	}
	if !bytes.Equal(buf, make([]byte, ondisk.BlockSize)) {
		t.Errorf("header slot for file %d is not all-zero after freeing", fileNum)
	}
}
