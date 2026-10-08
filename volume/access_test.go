package volume

import (
	"bytes"
	"errors"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

// accessFixture is a fresh volume with one file, DATA.DAT;1, holding two
// blocks of data, in the master file directory.
type accessFixture struct {
	vol *Volume
	mfd *Directory
	bm  *Bitmap
	ib  *IndexBitmap
	fid ondisk.Fid
}

func newAccessFixture(t *testing.T) accessFixture {
	t.Helper()

	vol := newInitializedTestVolume(t, "SHARE")
	dev := vol.Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatal(err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatal(err)
	}

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatal(err)
	}

	f, err := vol.CreateFile(mfd, "DATA.DAT", ondisk.RecAttr{}, bm, ib)
	if err != nil {
		t.Fatal(err)
	}

	for vbn := uint32(1); vbn <= 2; vbn++ {
		if err := f.WriteBlock(vbn, blockOf(byte('0'+vbn))); err != nil {
			t.Fatal(err)
		}
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	return accessFixture{vol: vol, mfd: mfd, bm: bm, ib: ib, fid: f.Header.Fid}
}

// mustAccess accesses the fixture's file with mode, failing the test if
// it can't.
func (fx accessFixture) mustAccess(t *testing.T, mode AccessMode) *Access {
	t.Helper()

	a, err := fx.vol.Access(fx.fid, mode)
	if err != nil {
		t.Fatalf("Access(%+v): %v", mode, err)
	}

	return a
}

// TestAccessArbitration: a second access is refused exactly when it
// conflicts with the first, in each direction.
func TestAccessArbitration(t *testing.T) {
	read := AccessMode{}
	write := AccessMode{Write: true}
	readDenyWrite := AccessMode{NoWrite: true}
	writeDenyWrite := AccessMode{Write: true, NoWrite: true}
	exclusive := AccessMode{Write: true, NoRead: true, NoWrite: true}
	readDenyRead := AccessMode{NoRead: true}

	cases := []struct {
		name          string
		first, second AccessMode
		ok            bool
	}{
		{"two readers", read, read, true},
		{"reader then writer", read, write, true},
		{"writer then reader", write, read, true},
		{"two writers", write, write, true},
		{"a reader denying writers, then a writer", readDenyWrite, write, false},
		{"a writer, then a reader denying writers", write, readDenyWrite, false},
		{"two readers denying writers", readDenyWrite, readDenyWrite, true},
		{"a writer denying writers, then a reader", writeDenyWrite, read, true},
		{"a writer denying writers, then a writer", writeDenyWrite, write, false},
		{"exclusive, then a reader", exclusive, read, false},
		{"a reader, then exclusive", read, exclusive, false},
		{"a reader denying readers, then a reader", readDenyRead, read, false},
		{"a reader, then one denying readers", read, readDenyRead, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newAccessFixture(t)
			a := fx.mustAccess(t, c.first)

			b, err := fx.vol.Access(fx.fid, c.second)
			if c.ok != (err == nil) {
				t.Fatalf("second Access: %v; want ok %v", err, c.ok)
			}

			if !c.ok && !errors.Is(err, ErrAccessConflict) {
				t.Errorf("second Access: %v; want ErrAccessConflict", err)
			}

			if b != nil {
				if b.File != a.File {
					t.Error("the two accessors don't share one File")
				}

				_ = b.Deaccess()
			}

			_ = a.Deaccess()

			// With both gone, anything goes again.
			c := fx.mustAccess(t, exclusive)
			_ = c.Deaccess()
		})
	}
}

// TestAccessSharesOneFile: an extension and an end of file set through
// one accessor are seen through another and through OpenFID, and the
// last Deaccess writes them, so a later OpenFID (from the disk) has
// them too.
func TestAccessSharesOneFile(t *testing.T) {
	fx := newAccessFixture(t)
	a := fx.mustAccess(t, AccessMode{Write: true})
	b := fx.mustAccess(t, AccessMode{Write: true})

	if err := a.File.WriteBlock(10, blockOf('A')); err != nil {
		t.Fatal(err)
	}

	a.File.SetEndOfFile(11, 0)

	peek, err := fx.vol.OpenFID(fx.fid)
	if err != nil {
		t.Fatal(err)
	}

	if peek != b.File || b.File.Blocks() < 10 || b.File.UsedBlocks() != 10 {
		t.Errorf("the second accessor sees %d blocks, %d used", b.File.Blocks(), b.File.UsedBlocks())
	}

	if err := a.Deaccess(); err != nil {
		t.Fatal(err)
	}

	// The other accessor's File is still armed for writing.
	if err := b.File.WriteBlock(11, blockOf('B')); err != nil {
		t.Fatalf("writing after the first accessor left: %v", err)
	}

	b.File.SetEndOfFile(12, 0)

	if err := b.Deaccess(); err != nil {
		t.Fatal(err)
	}

	if fx.vol.Accessed(fx.fid) {
		t.Error("the file is still accessed")
	}

	fresh, err := fx.vol.OpenFID(fx.fid)
	if err != nil {
		t.Fatal(err)
	}

	if fresh == b.File {
		t.Error("OpenFID of a file no one accesses returned the shared File")
	}

	if fresh.UsedBlocks() != 11 || fresh.Blocks() < 11 {
		t.Errorf("on disk: %d blocks, %d used; want 11 used", fresh.Blocks(), fresh.UsedBlocks())
	}

	buf := make([]byte, ondisk.BlockSize)
	if err := fresh.ReadBlock(10, buf); err != nil || !bytes.Equal(buf, blockOf('A')) {
		t.Errorf("block 10: %v, %q...", err, buf[:4])
	}

	if err := fresh.ReadBlock(11, buf); err != nil || !bytes.Equal(buf, blockOf('B')) {
		t.Errorf("block 11: %v, %q...", err, buf[:4])
	}

	assertCleanDisk(t, fx.vol.Devices[0], fx.bm)
}

// TestCloseLeavesSharedFileArmed: a Close through another path (a File
// from OpenFID, armed and closed) doesn't disarm the File its accessors
// are writing.
func TestCloseLeavesSharedFileArmed(t *testing.T) {
	fx := newAccessFixture(t)
	a := fx.mustAccess(t, AccessMode{Write: true})

	peek, err := fx.vol.OpenFID(fx.fid)
	if err != nil {
		t.Fatal(err)
	}

	if err := peek.Close(); err != nil {
		t.Fatal(err)
	}

	if err := a.File.WriteBlock(3, blockOf('C')); err != nil {
		t.Errorf("WriteBlock after another Close: %v", err)
	}

	_ = a.Deaccess()
}

// TestDeleteWhileAccessed: deleting an accessed file removes its entry at
// once; its accessor still reads its data; the last Deaccess frees its
// header and blocks; and it can't be accessed meanwhile.
func TestDeleteWhileAccessed(t *testing.T) {
	fx := newAccessFixture(t)
	a := fx.mustAccess(t, AccessMode{})

	if err := DeleteFile(fx.mfd, "DATA.DAT", 1, fx.bm, fx.ib); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	if _, err := fx.mfd.Lookup("DATA.DAT", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("the entry is still there: %v", err)
	}

	if !a.File.MarkedForDelete() {
		t.Error("not marked for delete")
	}

	if _, err := fx.vol.Access(fx.fid, AccessMode{}); !errors.Is(err, ErrMarkedForDelete) {
		t.Errorf("accessing it again: %v", err)
	}

	// A new file must not get its header slot or blocks.
	g, err := fx.vol.CreateFile(fx.mfd, "NEW.DAT", ondisk.RecAttr{}, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	for vbn := uint32(1); vbn <= 4; vbn++ {
		if err := g.WriteBlock(vbn, blockOf('N')); err != nil {
			t.Fatal(err)
		}
	}

	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	if g.Header.Fid.Number() == fx.fid.Number() {
		t.Error("the new file took the deleted file's header slot")
	}

	buf := make([]byte, ondisk.BlockSize)
	if err := a.File.ReadBlock(2, buf); err != nil || !bytes.Equal(buf, blockOf('2')) {
		t.Errorf("reading the deleted file: %v, %q...", err, buf[:4])
	}

	if err := a.Deaccess(); err != nil {
		t.Fatal(err)
	}

	if _, err := fx.vol.OpenFID(fx.fid); err == nil {
		t.Error("the file's header is still there after its last deaccess")
	}

	assertCleanDisk(t, fx.vol.Devices[0], fx.bm)
}

// TestDeleteHeaderWhileAccessed: the same, for a file deleted by its ID.
func TestDeleteHeaderWhileAccessed(t *testing.T) {
	fx := newAccessFixture(t)
	a := fx.mustAccess(t, AccessMode{Write: true})

	if err := fx.mfd.Remove("DATA.DAT", 1, fx.bm, fx.ib); err != nil {
		t.Fatal(err)
	}

	if err := DeleteHeader(fx.vol.Devices[0], fx.fid, fx.bm, fx.ib); err != nil {
		t.Fatal(err)
	}

	if _, err := fx.vol.OpenFID(fx.fid); err != nil {
		t.Errorf("the file went before its last deaccess: %v", err)
	}

	if err := a.Deaccess(); err != nil {
		t.Fatal(err)
	}

	if _, err := fx.vol.OpenFID(fx.fid); err == nil {
		t.Error("the file is still there")
	}

	assertCleanDisk(t, fx.vol.Devices[0], fx.bm)
}

// TestAccessFileForCreator: a file just created is accessed by its
// creator through the File in hand.
func TestAccessFileForCreator(t *testing.T) {
	fx := newAccessFixture(t)

	f, err := fx.vol.CreateFile(fx.mfd, "MADE.DAT", ondisk.RecAttr{}, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	a, err := fx.vol.AccessFile(f, AccessMode{Write: true, NoWrite: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fx.vol.Access(f.Header.Fid, AccessMode{Write: true}); !errors.Is(err, ErrAccessConflict) {
		t.Errorf("a second writer: %v", err)
	}

	if err := a.Deaccess(); err != nil {
		t.Fatal(err)
	}
}

// TestSetEndOfFile: the end of file, and the high-water mark it moves.
func TestSetEndOfFile(t *testing.T) {
	fx := newAccessFixture(t)
	a := fx.mustAccess(t, AccessMode{Write: true})
	f := a.File

	f.SetEndOfFile(5, 100)

	if got := f.Header.RecordAttributes; got.EndOfFileBlock != 5 || got.FirstFreeByte != 100 || f.Header.HighWaterMark < 6 {
		t.Errorf("EOF %d/%d, HWM %d", got.EndOfFileBlock, got.FirstFreeByte, f.Header.HighWaterMark)
	}

	f.SetEndOfFile(1, 0)

	if f.UsedBlocks() != 0 {
		t.Errorf("used blocks %d after an end of file at the start", f.UsedBlocks())
	}

	_ = a.Deaccess()
}

// assertCleanDisk flushes bm and fails the test if ANALYZE/DISK finds
// the bitmap and the headers disagree.
func assertCleanDisk(t *testing.T, dev *Device, bm *Bitmap) {
	t.Helper()

	if err := bm.Flush(); err != nil {
		t.Fatal(err)
	}

	report, err := AnalyzeDisk(dev)
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range report.Discrepancies {
		t.Error(d)
	}
}
