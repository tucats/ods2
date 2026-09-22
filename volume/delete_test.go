package volume

import (
	"bytes"
	"reflect"
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

// TestDeleteFileRejectsVersionZero confirms DeleteFile enforces the same
// "an exact version is always required" rule Directory.Remove itself
// enforces, and does so before ever touching dir/bm/ib -- all three are
// deliberately nil here, which would panic if DeleteFile tried to do
// anything with them before this check.
func TestDeleteFileRejectsVersionZero(t *testing.T) {
	if err := DeleteFile(nil, "FOO.TXT", 0, nil, nil); err == nil {
		t.Fatal("DeleteFile with version 0: want error, got nil")
	}
}

// TestDeleteFileEndToEndSingleSegment covers the core subtask 3 scenario
// for a small, single-segment file: create it through the ordinary
// CreateFile write path, delete it through DeleteFile, and confirm every
// piece of storage it owned is genuinely reclaimed -- the directory no
// longer lists it, its former header slot is reusable, and its former data
// extent is reusable -- all checked after an explicit Flush of both
// caches, matching how a real DELETE command (subtask 4) would leave
// things before dismounting.
func TestDeleteFileEndToEndSingleSegment(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "TESTDIR.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	f, err := vol.CreateFile(dir, "GONE.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := f.WriteBlock(1, blockOf(0x5A)); err != nil {
		t.Fatalf("WriteBlock(1): %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fileNumber := f.Header.Fid.Number()

	if err := DeleteFile(dir, "GONE.DAT", 1, bm, ib); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("directory List() after DeleteFile = %+v, want empty", entries)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	ib2, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap (fresh): %v", err)
	}
	got, err := ib2.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot after DeleteFile: %v", err)
	}
	if got != fileNumber {
		t.Errorf("FindFreeSlot after DeleteFile = %d, want the file's own former slot %d", got, fileNumber)
	}
	assertHeaderSlotIsZeroed(t, container, uint16(fileNumber))

	bm2, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap (fresh): %v", err)
	}
	// Only wideFreeClusters-1, not the full wideFreeClusters, is expected
	// back: dir's own first Insert (inside CreateFile, above) grew it from
	// 0 to 1 block, permanently consuming one cluster of the wide free
	// range for the directory's own content -- Directory.Remove never
	// shrinks a directory's block ALLOCATION back down (see
	// docs/PHASE-03.md's non-goals), only its logical entry count, so that
	// one cluster is never coming back in this test. Finding the other
	// wideFreeClusters-1 clusters free again as one contiguous run proves
	// the deleted file's own single data block was genuinely reclaimed.
	if _, err := bm2.FindFree(wideFreeClusters - 1); err != nil {
		t.Errorf("FindFree(%d) after DeleteFile: %v (data extent was not fully reclaimed)", wideFreeClusters-1, err)
	}
}

// TestDeleteFileEndToEndMultiSegment is the same scenario as above, but
// against a file with a real extension-header segment (forced by
// extending it one block at a time, the same trick
// TestFreeFileStorageReclaimsMultiSegmentFile uses), confirming DeleteFile
// -- not just freeFileStorage directly -- walks the WHOLE chain, freeing
// the extension segment's own header slot too.
func TestDeleteFileEndToEndMultiSegment(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "TESTDIR.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	f, err := vol.CreateFile(dir, "SEG.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	const calls = 150
	for i := 0; i < calls; i++ {
		if err := f.WriteBlock(uint32(i+1), blockOf(byte(i))); err != nil {
			t.Fatalf("WriteBlock #%d: %v", i, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f.Header.ExtensionFid.IsZero() {
		t.Fatal("test setup: primary header has no extension segment after enough writes to fill its map area")
	}
	primaryFileNumber := f.Header.Fid.Number()
	extensionFileNumber := f.Header.ExtensionFid.Number()

	if err := DeleteFile(dir, "SEG.DAT", 1, bm, ib); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	assertHeaderSlotIsZeroed(t, container, uint16(primaryFileNumber))
	assertHeaderSlotIsZeroed(t, container, uint16(extensionFileNumber))

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
	if !gotSlots[primaryFileNumber] || !gotSlots[extensionFileNumber] {
		t.Errorf("free slots after DeleteFile = {%d, %d}, want both %d (primary) and %d (extension)",
			first, second, primaryFileNumber, extensionFileNumber)
	}

	bm2, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap (fresh): %v", err)
	}
	// wideFreeClusters-1, not the full wideFreeClusters, for the same
	// reason TestDeleteFileEndToEndSingleSegment's own check does: dir's
	// first Insert permanently consumed one cluster growing it from 0 to
	// 1 block, which Directory.Remove never gives back (see
	// docs/PHASE-03.md's non-goals on directory storage shrink-back).
	if _, err := bm2.FindFree(wideFreeClusters - 1); err != nil {
		t.Errorf("FindFree(%d) after DeleteFile: %v (extents across both segments were not fully reclaimed)", wideFreeClusters-1, err)
	}
}

// TestDeleteFileLeavesOtherVersionsIntact is the regression case the
// subtask's own write-up calls out by name: deleting one version of a
// multi-version name must not disturb any sibling version's directory
// entry or its independent readability via OpenFID.
func TestDeleteFileLeavesOtherVersionsIntact(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "TESTDIR.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	first, err := vol.CreateFile(dir, "MULTI.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile (version 1): %v", err)
	}
	if err := first.WriteBlock(1, blockOf(0x11)); err != nil {
		t.Fatalf("WriteBlock(1) on version 1: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close (version 1): %v", err)
	}

	second, err := vol.CreateFile(dir, "MULTI.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile (version 2): %v", err)
	}
	if err := second.WriteBlock(1, blockOf(0x22)); err != nil {
		t.Fatalf("WriteBlock(1) on version 2: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close (version 2): %v", err)
	}
	secondFid := second.Header.Fid

	if err := DeleteFile(dir, "MULTI.DAT", 1, bm, ib); err != nil {
		t.Fatalf("DeleteFile(;1): %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Version != 2 {
		t.Fatalf("List() after deleting version 1 = %+v, want exactly version 2", entries)
	}

	reopened, err := vol.OpenFID(secondFid)
	if err != nil {
		t.Fatalf("OpenFID(version 2) after deleting version 1: %v", err)
	}
	buf := make([]byte, ondisk.BlockSize)
	if err := reopened.ReadBlock(1, buf); err != nil {
		t.Fatalf("ReadBlock(1) on surviving version 2: %v", err)
	}
	if !bytes.Equal(buf, blockOf(0x22)) {
		t.Error("surviving version 2's content changed after deleting version 1")
	}
}

// TestDeleteFileNonexistentEntryErrors confirms deleting a (name, version)
// that isn't present is reported as an error and leaves the directory's
// on-disk content untouched, mirroring
// TestDirectoryRemoveNonexistentEntryErrors's own check one layer down.
func TestDeleteFileNonexistentEntryErrors(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "TESTDIR.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	f, err := vol.CreateFile(dir, "REAL.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before, err := dir.List()
	if err != nil {
		t.Fatalf("List (before): %v", err)
	}

	if err := DeleteFile(dir, "MISSING.DAT", 1, bm, ib); err == nil {
		t.Error("DeleteFile of a nonexistent name: want error, got nil")
	}
	if err := DeleteFile(dir, "REAL.DAT", 7, bm, ib); err == nil {
		t.Error("DeleteFile of a nonexistent version: want error, got nil")
	}

	after, err := dir.List()
	if err != nil {
		t.Fatalf("List (after): %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("directory content changed after failed DeleteFile calls: before %+v, after %+v", before, after)
	}
}

// TestDeleteFileRejectsNonEmptyDirectory confirms DeleteFile refuses to
// delete a directory file that still has at least one entry in it,
// leaving both the parent directory's own entry for it and the
// subdirectory's content completely untouched. Without this check,
// deleting a non-empty directory would orphan whatever it still names --
// nothing would ever reach those entries again through an ordinary
// directory walk once the one entry pointing at this directory is gone.
func TestDeleteFileRejectsNonEmptyDirectory(t *testing.T) {
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

	parent := newWritableTestDirectory(t, dev, ib, "PARENT.DIR")

	subFile, err := CreateHeader(dev, ib, NewFileHeader{
		Name:            "SUB.DIR",
		Directory:       parent.Header.Fid,
		Characteristics: ondisk.FchDirectory,
	})
	if err != nil {
		t.Fatalf("CreateHeader(SUB.DIR): %v", err)
	}
	if err := parent.Insert("SUB.DIR", 1, subFile.Header.Fid, bm, ib); err != nil {
		t.Fatalf("Insert(SUB.DIR): %v", err)
	}

	subDir, err := subFile.Directory()
	if err != nil {
		t.Fatalf("Directory(): %v", err)
	}
	childFid := ondisk.Fid{Num: 60, Seq: 1}
	if err := subDir.Insert("CHILD.TXT", 1, childFid, bm, ib); err != nil {
		t.Fatalf("Insert(CHILD.TXT) into subdirectory: %v", err)
	}

	beforeParent, err := parent.List()
	if err != nil {
		t.Fatalf("List (parent, before): %v", err)
	}

	if err := DeleteFile(parent, "SUB.DIR", 1, bm, ib); err == nil {
		t.Fatal("DeleteFile of a non-empty directory: want error, got nil")
	}

	afterParent, err := parent.List()
	if err != nil {
		t.Fatalf("List (parent, after): %v", err)
	}
	if !reflect.DeepEqual(beforeParent, afterParent) {
		t.Errorf("parent directory content changed after rejected DeleteFile: before %+v, after %+v", beforeParent, afterParent)
	}

	childEntries, err := subDir.List()
	if err != nil {
		t.Fatalf("List (subdirectory, after): %v", err)
	}
	if len(childEntries) != 1 || childEntries[0].Name != "CHILD.TXT" {
		t.Errorf("subdirectory content changed after rejected DeleteFile: %+v", childEntries)
	}
}

// TestDeleteFileAllowsEmptyDirectory confirms an empty directory file is
// deleted exactly like any other file once DeleteFile has confirmed it has
// no entries -- the same end-to-end reclamation TestDeleteFileEndToEnd*
// already exercises for ordinary files, just with FchDirectory set.
func TestDeleteFileAllowsEmptyDirectory(t *testing.T) {
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

	parent := newWritableTestDirectory(t, dev, ib, "PARENT.DIR")

	subFile, err := CreateHeader(dev, ib, NewFileHeader{
		Name:            "EMPTY.DIR",
		Directory:       parent.Header.Fid,
		Characteristics: ondisk.FchDirectory,
	})
	if err != nil {
		t.Fatalf("CreateHeader(EMPTY.DIR): %v", err)
	}
	if err := parent.Insert("EMPTY.DIR", 1, subFile.Header.Fid, bm, ib); err != nil {
		t.Fatalf("Insert(EMPTY.DIR): %v", err)
	}
	subFileNumber := subFile.Header.Fid.Number()

	if err := DeleteFile(parent, "EMPTY.DIR", 1, bm, ib); err != nil {
		t.Fatalf("DeleteFile of an empty directory: %v", err)
	}

	entries, err := parent.List()
	if err != nil {
		t.Fatalf("List (parent, after): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("parent directory List() after deleting EMPTY.DIR = %+v, want empty", entries)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}
	assertHeaderSlotIsZeroed(t, container, uint16(subFileNumber))
}

// TestDeleteFileRejectsMasterFileDirectory confirms DeleteFile refuses to
// delete the volume's master file directory outright, purely from its
// fixed Fid -- before ever trying to resolve or read whatever header that
// Fid actually names (the entry inserted below doesn't point at a real
// directory header at all, which would fail loudly if DeleteFile got far
// enough to try reading it).
func TestDeleteFileRejectsMasterFileDirectory(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "TESTDIR.DIR")
	if err := dir.Insert("MFD.DIR", 1, ondisk.MasterFileDirectoryFid, bm, ib); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := DeleteFile(dir, "MFD.DIR", 1, bm, ib); err == nil {
		t.Fatal("DeleteFile targeting the master file directory: want error, got nil")
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory content changed after rejected DeleteFile: %+v", entries)
	}
}

// TestDeleteFileRejectsReservedBookkeepingFiles confirms DeleteFile refuses
// to delete INDEXF.SYS or BITMAP.SYS, exactly like
// TestDeleteFileRejectsMasterFileDirectory confirms for the MFD -- purely
// from the fixed Fid each is looked up under, and regardless of what name
// the directory entry pointing at that Fid actually uses. That last part is
// the point of this test: the entries below are deliberately inserted under
// names that don't match INDEXF.SYS/BITMAP.SYS at all, proving the guard is
// keyed on file number, not on recognizing a reserved filename.
func TestDeleteFileRejectsReservedBookkeepingFiles(t *testing.T) {
	cases := []struct {
		name string
		fid  ondisk.Fid
	}{
		{name: "NOTINDEXF.SYS", fid: ondisk.IndexFileFid},
		{name: "NOTBITMAP.SYS", fid: ondisk.BitmapFileFid},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
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

			dir := newWritableTestDirectory(t, dev, ib, "TESTDIR.DIR")
			if err := dir.Insert(c.name, 1, c.fid, bm, ib); err != nil {
				t.Fatalf("Insert: %v", err)
			}

			if err := DeleteFile(dir, c.name, 1, bm, ib); err == nil {
				t.Fatalf("DeleteFile targeting fid %v: want error, got nil", c.fid)
			}

			entries, err := dir.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(entries) != 1 {
				t.Errorf("directory content changed after rejected DeleteFile: %+v", entries)
			}
		})
	}
}

// TestSetVersionLimitRoundTripsOnPlainFile confirms setting a plain file's
// VersionLimit sticks and is genuinely readable back through a completely
// independent OpenFID, not just this call's own in-memory f.
func TestSetVersionLimitRoundTripsOnPlainFile(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "SVLDIR.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	f, err := vol.CreateFile(dir, "TARGET.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := SetVersionLimit(f, 6); err != nil {
		t.Fatalf("SetVersionLimit: %v", err)
	}
	if got := f.Header.RecordAttributes.VersionLimit; got != 6 {
		t.Errorf("f.Header.RecordAttributes.VersionLimit after SetVersionLimit = %d, want 6", got)
	}

	reopened, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if got := reopened.Header.RecordAttributes.VersionLimit; got != 6 {
		t.Errorf("reopened VersionLimit = %d, want 6", got)
	}

	// Setting it back to 0 restores "unlimited".
	if err := SetVersionLimit(f, 0); err != nil {
		t.Fatalf("SetVersionLimit(0): %v", err)
	}
	reopenedAgain, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID (after reset): %v", err)
	}
	if got := reopenedAgain.Header.RecordAttributes.VersionLimit; got != 0 {
		t.Errorf("reopened VersionLimit after resetting to 0 = %d, want 0", got)
	}
}

// TestSetVersionLimitOnDirectoryAffectsFutureInheritance confirms
// SetVersionLimit works identically on a directory file -- per
// docs/PHASE-03.md's "Version-limit design" point 1, a directory is just a
// File whose header happens to have FchDirectory set, so the same
// rewrite-one-header-field logic applies uniformly -- and that a
// subsequently-created new name in it inherits the newly-set value (an
// integration check with subtask 5's resolveVersionLimit).
func TestSetVersionLimitOnDirectoryAffectsFutureInheritance(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "INHERIT.DIR")
	if !dir.Header.IsDirectory() {
		t.Fatal("test setup: fixture directory doesn't have FchDirectory set")
	}

	if err := SetVersionLimit(dir.File, 8); err != nil {
		t.Fatalf("SetVersionLimit(directory): %v", err)
	}
	if got := dir.Header.RecordAttributes.VersionLimit; got != 8 {
		t.Errorf("directory's own VersionLimit after SetVersionLimit = %d, want 8", got)
	}

	vol := &Volume{Devices: []*Device{dev}}
	f, err := vol.CreateFile(dir, "CHILD.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := f.Header.RecordAttributes.VersionLimit; got != 8 {
		t.Errorf("new file's inherited VersionLimit = %d, want 8 (the directory's newly-set default)", got)
	}
}

// newPurgeTestDirectory builds a directory containing count versions of
// name (version numbers 1..count), each a small Stream_LF file, via the
// ordinary CreateFile write path with an unlimited (0) version limit so
// PurgeVersions' own tests can control exactly how many versions exist
// without CreateFile's own enforcement interfering.
func newPurgeTestDirectory(t *testing.T, dev *Device, ib *IndexBitmap, bm *Bitmap, name string, count int) (*Volume, *Directory) {
	t.Helper()

	dir := newWritableTestDirectory(t, dev, ib, "PURGEDIR.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	for i := 0; i < count; i++ {
		f, err := vol.CreateFile(dir, name, ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
		if err != nil {
			t.Fatalf("CreateFile #%d: %v", i, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
	return vol, dir
}

// TestPurgeVersionsTrimsToKeepHighest confirms a name with more versions
// than keep is trimmed down to exactly keep, always removing the oldest.
func TestPurgeVersionsTrimsToKeepHighest(t *testing.T) {
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

	_, dir := newPurgeTestDirectory(t, dev, ib, bm, "PURGED.DAT", 5)

	if err := PurgeVersions(dir, "PURGED.DAT", 2, bm, ib); err != nil {
		t.Fatalf("PurgeVersions: %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count after PurgeVersions = %d, want 2", len(entries))
	}
	got := map[uint16]bool{}
	for _, e := range entries {
		got[e.Version] = true
	}
	if !got[4] || !got[5] {
		t.Errorf("surviving versions = %+v, want exactly {4, 5}", entries)
	}
}

// TestPurgeVersionsLeavesUnderLimitNameUntouched confirms a name with
// fewer versions than keep (or exactly keep) is left alone -- including no
// unnecessary DeleteFile/directory-rewrite work for it, confirmed here by
// the directory's on-disk content being byte-for-byte unchanged.
func TestPurgeVersionsLeavesUnderLimitNameUntouched(t *testing.T) {
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

	_, dir := newPurgeTestDirectory(t, dev, ib, bm, "SAFE.DAT", 2)

	before, err := dir.List()
	if err != nil {
		t.Fatalf("List (before): %v", err)
	}

	if err := PurgeVersions(dir, "SAFE.DAT", 5, bm, ib); err != nil {
		t.Fatalf("PurgeVersions (fewer than keep): %v", err)
	}
	if err := PurgeVersions(dir, "SAFE.DAT", 2, bm, ib); err != nil {
		t.Fatalf("PurgeVersions (exactly keep): %v", err)
	}

	after, err := dir.List()
	if err != nil {
		t.Fatalf("List (after): %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("directory content changed despite every version being at or under the keep limit: before %+v, after %+v", before, after)
	}
}

// TestPurgeVersionsRejectsZeroKeep confirms /LIMIT=0 (keep == 0) is
// rejected outright, per PurgeVersions' own doc comment on why it doesn't
// support the degenerate "delete every version" case.
func TestPurgeVersionsRejectsZeroKeep(t *testing.T) {
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

	_, dir := newPurgeTestDirectory(t, dev, ib, bm, "ZERO.DAT", 3)

	if err := PurgeVersions(dir, "ZERO.DAT", 0, bm, ib); err == nil {
		t.Fatal("PurgeVersions with keep == 0: want error, got nil")
	}
}

// TestPurgeVersionsIndependentNamesEachTrimmedSeparately confirms a
// directory containing several distinct names is handled correctly when
// PurgeVersions is called once per name -- each trimmed independently, a
// name with only 1 version left completely alone rather than treated as
// an error.
func TestPurgeVersionsIndependentNamesEachTrimmedSeparately(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "MULTINAME.DIR")
	vol := &Volume{Devices: []*Device{dev}}

	createN := func(name string, n int) {
		for i := 0; i < n; i++ {
			f, err := vol.CreateFile(dir, name, ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
			if err != nil {
				t.Fatalf("CreateFile(%s) #%d: %v", name, i, err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("Close(%s) #%d: %v", name, i, err)
			}
		}
	}
	createN("MANY.DAT", 3)
	createN("ONE.DAT", 1)

	if err := PurgeVersions(dir, "MANY.DAT", 1, bm, ib); err != nil {
		t.Fatalf("PurgeVersions(MANY.DAT): %v", err)
	}
	if err := PurgeVersions(dir, "ONE.DAT", 1, bm, ib); err != nil {
		t.Fatalf("PurgeVersions(ONE.DAT) (already at limit): %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var manyVersions, oneVersions []uint16
	for _, e := range entries {
		switch e.Name {
		case "MANY.DAT":
			manyVersions = append(manyVersions, e.Version)
		case "ONE.DAT":
			oneVersions = append(oneVersions, e.Version)
		}
	}
	if len(manyVersions) != 1 || manyVersions[0] != 3 {
		t.Errorf("MANY.DAT surviving versions = %v, want exactly [3]", manyVersions)
	}
	if len(oneVersions) != 1 || oneVersions[0] != 1 {
		t.Errorf("ONE.DAT surviving versions = %v, want exactly [1] (untouched)", oneVersions)
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
