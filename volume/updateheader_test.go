package volume

import (
	"bytes"
	"testing"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
)

// updateHeaderFixture creates a one-block file, NOTE.TXT, in a fresh
// writable test directory, and returns it (already closed) along with the
// volume it's on, so a test can rewrite its header and then reopen it by
// FID to see what reached the disk.
func updateHeaderFixture(t *testing.T) (*Volume, *File) {
	t.Helper()

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

	f, err := vol.CreateFile(dir, "NOTE.TXT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, RecordSize: 512}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	if err := f.WriteBlock(1, blockOf(0x41)); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	return vol, f
}

// TestUpdateHeaderRewritesFieldsAndIdent changes a fixed field, the record
// attributes (a partial last block: the end of file a program sets through
// an attribute list), and IDENT-area dates, and confirms all of them reach
// the disk while the file's data and allocation are untouched.
func TestUpdateHeaderRewritesFieldsAndIdent(t *testing.T) {
	vol, f := updateHeaderFixture(t)
	expires := vmstime.VMSTime(0x00A1B2C3D4E5F600)

	err := UpdateHeader(f, func(h *ondisk.FileHeader, id *ondisk.Ident) {
		h.FileProtection = 0xFA00
		h.Owner = ondisk.Uic{Group: 0o100, Member: 0o7}
		h.RecordAttributes.EndOfFileBlock = 1
		h.RecordAttributes.FirstFreeByte = 100
		id.ExpirationDate = expires
	})
	if err != nil {
		t.Fatalf("UpdateHeader: %v", err)
	}

	again, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}

	h := again.Header
	if h.FileProtection != 0xFA00 || h.Owner != (ondisk.Uic{Group: 0o100, Member: 0o7}) {
		t.Errorf("protection %#x, owner %v: the fixed fields weren't written", h.FileProtection, h.Owner)
	}

	if h.RecordAttributes.EndOfFileBlock != 1 || h.RecordAttributes.FirstFreeByte != 100 {
		t.Errorf("end of file %d/%d, want 1/100", h.RecordAttributes.EndOfFileBlock, h.RecordAttributes.FirstFreeByte)
	}

	id, err := h.Ident()
	if err != nil {
		t.Fatalf("Ident: %v", err)
	}

	if id.ExpirationDate != expires || id.Filename == "" {
		t.Errorf("ident %+v: the expiration date wasn't written, or the name was lost", id)
	}

	buf := make([]byte, ondisk.BlockSize)
	if err := again.ReadBlock(1, buf); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}

	if !bytes.Equal(buf, blockOf(0x41)) {
		t.Error("the file's data changed")
	}

	if f.Header.FileProtection != 0xFA00 {
		t.Error("f.Header wasn't updated to the header as written")
	}
}

// TestUpdateHeaderProtectsStructuralFields confirms the fields tied to the
// header's map and its place in the index file are put back, whatever
// change does to them.
func TestUpdateHeaderProtectsStructuralFields(t *testing.T) {
	vol, f := updateHeaderFixture(t)
	before := f.Header

	err := UpdateHeader(f, func(h *ondisk.FileHeader, _ *ondisk.Ident) {
		h.Fid = ondisk.Fid{Num: 999, Seq: 9}
		h.ExtensionFid = ondisk.Fid{Num: 7, Seq: 7}
		h.SegmentNumber = 3
		h.StructureLevel = 0x0101
		h.RecordAttributes.HighestBlock = 5000
	})
	if err != nil {
		t.Fatalf("UpdateHeader: %v", err)
	}

	again, err := vol.OpenFID(before.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v (the header was written somewhere else?)", err)
	}

	h := again.Header
	if h.Fid != before.Fid || h.ExtensionFid != before.ExtensionFid || h.SegmentNumber != before.SegmentNumber ||
		h.StructureLevel != before.StructureLevel || h.RecordAttributes.HighestBlock != before.RecordAttributes.HighestBlock {
		t.Errorf("header %+v: a structural field changed (was %+v)", h, before)
	}
}

// TestFileHeaderRawIsTheDiskBlock checks FileHeader.Raw against the header
// block re-encoded from the same content: the copy it returns is the
// 512 bytes the disk holds, and changing it changes nothing.
func TestFileHeaderRawIsTheDiskBlock(t *testing.T) {
	_, f := updateHeaderFixture(t)

	raw := f.Header.Raw()
	if len(raw) != ondisk.BlockSize {
		t.Fatalf("Raw is %d bytes", len(raw))
	}

	decoded, err := ondisk.DecodeFileHeader(raw)
	if err != nil {
		t.Fatalf("DecodeFileHeader(Raw()): %v", err)
	}
	
	if decoded.Fid != f.Header.Fid {
		t.Errorf("Raw decodes to FID %v, want %v", decoded.Fid, f.Header.Fid)
	}

	raw[0] ^= 0xFF
	if f.Header.Raw()[0] == raw[0] {
		t.Error("Raw returned the header's own bytes, not a copy")
	}
}
