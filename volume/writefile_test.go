package volume

import (
	"bytes"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

// This file's tests reuse writeheader_test.go's writable fixture
// (newWritableHeaderTestVolume/installWideTestBitmap) and
// directory_test.go's newWritableTestDirectory helper, the same writable
// setup subtasks 8 and 9's own tests build on.

// blockOf returns an ondisk.BlockSize buffer filled with fill, for tests
// that just need distinguishable, non-zero block content.
func blockOf(fill byte) []byte {
	b := make([]byte, ondisk.BlockSize)
	for i := range b {
		b[i] = fill
	}
	return b
}

func TestFileWriteBlockRejectsUnarmedFile(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "RAW.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	// CreateHeader alone (unlike CreateFile) never arms the returned File
	// for writing -- WriteBlock must reject it rather than panicking on a
	// nil bm/ib.
	if err := f.WriteBlock(1, blockOf(0xAA)); err == nil {
		t.Fatal("WriteBlock on a File never armed via OpenForWrite: want error, got nil")
	}

	// Close on the same unarmed File is a harmless no-op, not an error --
	// callers that don't know whether a given File is writable should be
	// able to defer Close unconditionally.
	if err := f.Close(); err != nil {
		t.Errorf("Close on an unarmed File: want nil, got %v", err)
	}
}

func TestFileWriteBlockRejectsWrongSizedData(t *testing.T) {
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

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "SIZE.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}

	if err := f.WriteBlock(1, make([]byte, ondisk.BlockSize-1)); err == nil {
		t.Fatal("WriteBlock with undersized data: want error, got nil")
	}
	if err := f.WriteBlock(0, blockOf(0xAA)); err == nil {
		t.Fatal("WriteBlock(vbn=0): want error, got nil")
	}
}

// TestFileWriteBlockAutoExtendsAndReadsBack covers the core subtask 10
// scenario: create a file, write blocks out of order (forcing WriteBlock to
// extend past the file's current allocation more than once), close it, and
// confirm every written block reads back correctly through Phase 1's own
// File.ReadBlock -- including a block within the extended range that was
// never itself explicitly written, which must still read as zero.
func TestFileWriteBlockAutoExtendsAndReadsBack(t *testing.T) {
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

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "OOO.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}

	// Write VBN 1, then jump straight to VBN 4 (skipping 2 and 3 --
	// extending the file from 0 to 1 block, then again from 1 to 4), then
	// go back and fill in VBN 2. VBN 3 is deliberately left never written.
	if err := f.WriteBlock(1, blockOf(0x11)); err != nil {
		t.Fatalf("WriteBlock(1): %v", err)
	}
	if err := f.WriteBlock(4, blockOf(0x44)); err != nil {
		t.Fatalf("WriteBlock(4): %v", err)
	}
	if f.Blocks() != 4 {
		t.Fatalf("Blocks() after writing VBN 4 = %d, want 4 (auto-extend)", f.Blocks())
	}
	// Before VBN 3 is ever written, it must read back as zero -- the same
	// guarantee Extend's own allocated-but-unwritten space always gets.
	got := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(3, got); err != nil {
		t.Fatalf("ReadBlock(3) pre-write: %v", err)
	}
	if !bytes.Equal(got, make([]byte, ondisk.BlockSize)) {
		t.Error("ReadBlock(3) before it was ever written returned non-zero data")
	}
	if err := f.WriteBlock(2, blockOf(0x22)); err != nil {
		t.Fatalf("WriteBlock(2): %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-read everything through a completely independent OpenFID, not just
	// the in-memory f this test already mutated.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if reopened.Blocks() != 4 {
		t.Errorf("reopened Blocks() = %d, want 4", reopened.Blocks())
	}

	want := map[uint32]byte{1: 0x11, 2: 0x22, 4: 0x44}
	for vbn, fill := range want {
		buf := make([]byte, ondisk.BlockSize)
		if err := reopened.ReadBlock(vbn, buf); err != nil {
			t.Fatalf("ReadBlock(%d): %v", vbn, err)
		}
		if !bytes.Equal(buf, blockOf(fill)) {
			t.Errorf("ReadBlock(%d) = %x..., want a block filled with %#02x", vbn, buf[:4], fill)
		}
	}
	// VBN 3 was never written, and the underlying container was zero-filled
	// by diskimage.Create, so it must still read back as zero even after
	// Close's HighWaterMark advance past it.
	buf3 := make([]byte, ondisk.BlockSize)
	if err := reopened.ReadBlock(3, buf3); err != nil {
		t.Fatalf("ReadBlock(3): %v", err)
	}
	if !bytes.Equal(buf3, make([]byte, ondisk.BlockSize)) {
		t.Error("ReadBlock(3) after Close returned non-zero data, but VBN 3 was never written")
	}
}

// TestFileOpenForWriteOverwritesExistingBlockInPlace covers the subtask's
// other named scenario: reopening an already-written file (not one freshly
// created in the same test step) for write, and overwriting one of its
// existing blocks without disturbing the rest.
func TestFileOpenForWriteOverwritesExistingBlockInPlace(t *testing.T) {
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

	created, err := CreateHeader(dev, ib, NewFileHeader{Name: "EXIST.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := created.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	for vbn, fill := range map[uint32]byte{1: 0x01, 2: 0x02, 3: 0x03} {
		if err := created.WriteBlock(vbn, blockOf(fill)); err != nil {
			t.Fatalf("WriteBlock(%d): %v", vbn, err)
		}
	}
	if err := created.Close(); err != nil {
		t.Fatalf("Close (initial write): %v", err)
	}

	// Reopen as a completely independent File, via the ordinary read path,
	// then arm it for write and overwrite just VBN 2.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenFID(created.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if err := reopened.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite (reopen): %v", err)
	}
	if err := reopened.WriteBlock(2, blockOf(0xFF)); err != nil {
		t.Fatalf("WriteBlock(2) (overwrite): %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close (after overwrite): %v", err)
	}
	if reopened.Blocks() != 3 {
		t.Errorf("Blocks() after overwriting an existing block = %d, want 3 (unchanged)", reopened.Blocks())
	}

	final, err := vol.OpenFID(created.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID (final): %v", err)
	}
	want := map[uint32]byte{1: 0x01, 2: 0xFF, 3: 0x03}
	for vbn, fill := range want {
		buf := make([]byte, ondisk.BlockSize)
		if err := final.ReadBlock(vbn, buf); err != nil {
			t.Fatalf("ReadBlock(%d): %v", vbn, err)
		}
		if !bytes.Equal(buf, blockOf(fill)) {
			t.Errorf("ReadBlock(%d) = %x..., want a block filled with %#02x", vbn, buf[:4], fill)
		}
	}
	if final.Blocks() != 3 {
		t.Errorf("final Blocks() = %d, want 3", final.Blocks())
	}
}

// TestVolumeCreateFileEndToEnd exercises CreateFile itself: version
// resolution, header allocation, and directory insertion, followed by
// writing through the File it returns.
func TestVolumeCreateFileEndToEnd(t *testing.T) {
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

	f, err := vol.CreateFile(dir, "NEW.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := f.WriteBlock(1, blockOf(0x5A)); err != nil {
		t.Fatalf("WriteBlock(1): %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entry, err := dir.Lookup("NEW.DAT", 0)
	if err != nil {
		t.Fatalf("Lookup(NEW.DAT): %v", err)
	}
	if entry.Version != 1 {
		t.Errorf("first CreateFile's directory entry version = %d, want 1", entry.Version)
	}
	if entry.Fid != f.Header.Fid {
		t.Errorf("directory entry Fid = %v, want %v", entry.Fid, f.Header.Fid)
	}

	reopened, err := vol.OpenFID(entry.Fid)
	if err != nil {
		t.Fatalf("OpenFID(%v): %v", entry.Fid, err)
	}
	if reopened.Blocks() != 1 {
		t.Errorf("reopened Blocks() = %d, want 1", reopened.Blocks())
	}
	buf := make([]byte, ondisk.BlockSize)
	if err := reopened.ReadBlock(1, buf); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}
	if !bytes.Equal(buf, blockOf(0x5A)) {
		t.Error("reopened file's VBN 1 content doesn't match what was written")
	}

	// A second CreateFile with the same name must get the next version,
	// not overwrite/collide with the first.
	second, err := vol.CreateFile(dir, "NEW.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile (second version): %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close (second version): %v", err)
	}
	secondEntry, err := dir.Lookup("NEW.DAT", 0)
	if err != nil {
		t.Fatalf("Lookup(NEW.DAT) after second CreateFile: %v", err)
	}
	if secondEntry.Version != 2 {
		t.Errorf("second CreateFile's directory entry version = %d, want 2", secondEntry.Version)
	}
	if secondEntry.Fid == entry.Fid {
		t.Error("second CreateFile's Fid collides with the first version's")
	}
}

// TestFileCloseWithFinalByteRecordsPartialBlock covers CloseWithFinalByte's
// reason for existing (added alongside package rms's record writer,
// docs/PHASE-02.md subtask 13): a caller that tracks its own exact byte
// length can record FirstFreeByte precisely within the last block actually
// written, rather than Close's own always-whole-block convention.
func TestFileCloseWithFinalByteRecordsPartialBlock(t *testing.T) {
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

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "PARTIAL.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	if err := f.WriteBlock(1, blockOf(0x11)); err != nil {
		t.Fatalf("WriteBlock(1): %v", err)
	}
	if err := f.WriteBlock(2, blockOf(0x22)); err != nil {
		t.Fatalf("WriteBlock(2): %v", err)
	}

	// Only the first 100 bytes of VBN 2 are real content -- the rest of
	// that block is zero-padding WriteBlock's "exactly one block" contract
	// required, not part of the file's logical data.
	if err := f.CloseWithFinalByte(100); err != nil {
		t.Fatalf("CloseWithFinalByte: %v", err)
	}
	if got, want := f.Header.RecordAttributes.EndOfFileBlock, uint32(2); got != want {
		t.Errorf("EndOfFileBlock = %d, want %d", got, want)
	}
	if got, want := f.Header.RecordAttributes.FirstFreeByte, uint16(100); got != want {
		t.Errorf("FirstFreeByte = %d, want %d", got, want)
	}
	if got, want := f.UsedBlocks(), uint32(2); got != want {
		t.Errorf("UsedBlocks() = %d, want %d", got, want)
	}

	// Independent re-open confirms this actually reached disk, not just
	// f's own in-memory copy.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if got, want := reopened.Header.RecordAttributes.FirstFreeByte, uint16(100); got != want {
		t.Errorf("reopened FirstFreeByte = %d, want %d", got, want)
	}
	if got, want := reopened.Header.RecordAttributes.EndOfFileBlock, uint32(2); got != want {
		t.Errorf("reopened EndOfFileBlock = %d, want %d", got, want)
	}
}

// TestFileCloseIsCloseWithFinalByteZero confirms Close's documented
// equivalence to CloseWithFinalByte(0) actually holds for the whole-block
// case: closing after writing whole blocks with no explicit final-byte
// offset must produce exactly the FirstFreeByte-0 convention Close's own
// doc comment describes.
func TestFileCloseIsCloseWithFinalByteZero(t *testing.T) {
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

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "WHOLE.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	if err := f.WriteBlock(1, blockOf(0x33)); err != nil {
		t.Fatalf("WriteBlock(1): %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, want := f.Header.RecordAttributes.EndOfFileBlock, uint32(2); got != want {
		t.Errorf("EndOfFileBlock = %d, want %d", got, want)
	}
	if got, want := f.Header.RecordAttributes.FirstFreeByte, uint16(0); got != want {
		t.Errorf("FirstFreeByte = %d, want %d", got, want)
	}
}

func TestFileCloseWithFinalByteRejectsOutOfRangeOffset(t *testing.T) {
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

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "BAD.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}

	if err := f.CloseWithFinalByte(ondisk.BlockSize); err == nil {
		t.Fatal("CloseWithFinalByte(BlockSize): want error, got nil")
	}
}

func TestFileCloseIsIdempotent(t *testing.T) {
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

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "IDEM.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	if err := f.WriteBlock(1, blockOf(0x77)); err != nil {
		t.Fatalf("WriteBlock(1): %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Close disarms the File -- a WriteBlock after Close must fail cleanly
	// rather than silently writing through a stale bm/ib.
	if err := f.WriteBlock(2, blockOf(0x88)); err == nil {
		t.Error("WriteBlock after Close: want error, got nil")
	}
}

// TestVolumeCreateDirectoryEndToEnd exercises CreateDirectory itself:
// version resolution, header allocation with FchDirectory set, the
// requested version limit landing in the new header, and directory
// insertion into the parent -- followed by confirming the brand-new
// (zero-block) directory behaves like any other empty directory and can
// immediately accept an Insert of its own.
func TestVolumeCreateDirectoryEndToEnd(t *testing.T) {
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
	vol := &Volume{Devices: []*Device{dev}}

	sub, err := vol.CreateDirectory(parent, "SUB.DIR", 5, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}

	if !sub.Header.IsDirectory() {
		t.Error("CreateDirectory's result doesn't have the directory characteristic set")
	}
	if got := sub.Header.RecordAttributes.VersionLimit; got != 5 {
		t.Errorf("VersionLimit = %d, want 5", got)
	}
	if entries, err := sub.List(); err != nil || len(entries) != 0 {
		t.Errorf("brand-new directory's List() = %v, %v, want empty, nil", entries, err)
	}

	entry, err := parent.Lookup("SUB.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(SUB.DIR) in parent: %v", err)
	}
	if entry.Version != 1 {
		t.Errorf("first CreateDirectory's directory entry version = %d, want 1", entry.Version)
	}
	if entry.Fid != sub.Header.Fid {
		t.Errorf("parent's directory entry Fid = %v, want %v", entry.Fid, sub.Header.Fid)
	}

	// The new (zero-block) directory must immediately accept an Insert of
	// its own -- Directory.Insert already knows how to Extend a zero-block
	// directory the first time something is added to it (see
	// CreateDirectory's own doc comment).
	childFid := ondisk.Fid{Num: 90, Seq: 1}
	if err := sub.Insert("CHILD.TXT", 1, childFid, bm, ib); err != nil {
		t.Fatalf("Insert into brand-new directory: %v", err)
	}
	childEntries, err := sub.List()
	if err != nil {
		t.Fatalf("List after Insert: %v", err)
	}
	if len(childEntries) != 1 || childEntries[0].Name != "CHILD.TXT" {
		t.Errorf("List after Insert = %+v, want a single CHILD.TXT entry", childEntries)
	}

	// A second CreateDirectory with the same name must get the next
	// version, not overwrite/collide with the first -- matching CreateFile's
	// own auto-versioning behavior (TestVolumeCreateFileEndToEnd above).
	second, err := vol.CreateDirectory(parent, "SUB.DIR", 5, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectory (second version): %v", err)
	}
	secondEntry, err := parent.Lookup("SUB.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(SUB.DIR) after second CreateDirectory: %v", err)
	}
	if secondEntry.Version != 2 {
		t.Errorf("second CreateDirectory's directory entry version = %d, want 2", secondEntry.Version)
	}
	if second.Header.Fid == sub.Header.Fid {
		t.Error("second CreateDirectory's Fid collides with the first version's")
	}
}

// TestCreateDirectoryRejectsNameWithoutDirType confirms CreateDirectory
// refuses a name that doesn't end in ".DIR" before allocating anything --
// a directory entry under any other type would exist but be invisible to
// every directory-path-walking command (see CreateDirectory's own doc
// comment for why this matters beyond cosmetics).
func TestCreateDirectoryRejectsNameWithoutDirType(t *testing.T) {
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
	vol := &Volume{Devices: []*Device{dev}}

	if _, err := vol.CreateDirectory(parent, "SUB.TXT", 0, bm, ib); err == nil {
		t.Fatal("CreateDirectory with a non-.DIR name: want error, got nil")
	}

	entries, err := parent.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("parent directory content changed after rejected CreateDirectory: %+v", entries)
	}
}

// TestCreateDirectoryZeroVersionLimitMeansUnlimited confirms a versionLimit
// of 0 round-trips as 0 (VMS's own "unlimited" convention, docs/PHASE-03.md's
// "Version-limit design") rather than CreateDirectory silently treating it
// as "inherit from somewhere" -- that resolution is entirely the caller's
// job, not this function's.
func TestCreateDirectoryZeroVersionLimitMeansUnlimited(t *testing.T) {
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
	vol := &Volume{Devices: []*Device{dev}}

	sub, err := vol.CreateDirectory(parent, "SUB.DIR", 0, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	if got := sub.Header.RecordAttributes.VersionLimit; got != 0 {
		t.Errorf("VersionLimit = %d, want 0", got)
	}
}
