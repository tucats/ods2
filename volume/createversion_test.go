package volume

import (
	"errors"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

// createVersionFixture returns an empty writable test directory, its
// volume, and the volume's bitmap caches.
func createVersionFixture(t *testing.T) (*Volume, *Directory, *Bitmap, *IndexBitmap) {
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

	return &Volume{Devices: []*Device{dev}}, dir, bm, ib
}

// TestCreateFileVersionExplicitAndNext creates an explicit ;5, then lets
// the next version follow it, then refuses a second ;5.
func TestCreateFileVersionExplicitAndNext(t *testing.T) {
	vol, dir, bm, ib := createVersionFixture(t)

	five, err := vol.CreateFileVersion(dir, "V.DAT", 5, ondisk.RecAttr{}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFileVersion(;5): %v", err)
	}
	if err := five.Close(); err != nil {
		t.Fatal(err)
	}

	if e, err := dir.Lookup("V.DAT", 5); err != nil || e.Fid != five.Header.Fid {
		t.Fatalf("Lookup(;5) = %+v, %v", e, err)
	}

	six, err := vol.CreateFileVersion(dir, "V.DAT", 0, ondisk.RecAttr{}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFileVersion(next): %v", err)
	}
	if e, err := dir.Lookup("V.DAT", 6); err != nil || e.Fid != six.Header.Fid {
		t.Errorf("the next version isn't ;6: %+v, %v", e, err)
	}

	slot, _ := ib.FindFreeSlot()

	if _, err := vol.CreateFileVersion(dir, "v.dat", 5, ondisk.RecAttr{}, bm, ib); !errors.Is(err, ErrExists) {
		t.Errorf("a second ;5: %v, want ErrExists", err)
	}

	if again, _ := ib.FindFreeSlot(); again != slot {
		t.Errorf("the refused create allocated header slot %d", slot)
	}
}

// TestCreateFileVersionInheritsHighestVersionLimit: an explicit version
// below the highest carries the highest one's version limit forward.
func TestCreateFileVersionInheritsHighestVersionLimit(t *testing.T) {
	vol, dir, bm, ib := createVersionFixture(t)

	f, err := vol.CreateFileVersion(dir, "L.DAT", 9, ondisk.RecAttr{}, bm, ib)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetVersionLimit(f, 7); err != nil {
		t.Fatal(err)
	}

	low, err := vol.CreateFileVersion(dir, "L.DAT", 3, ondisk.RecAttr{}, bm, ib)
	if err != nil {
		t.Fatal(err)
	}
	if got := low.Header.RecordAttributes.VersionLimit; got != 7 {
		t.Errorf("L.DAT;3's version limit %d, want ;9's 7", got)
	}
}

// TestDirectoryInsertRefusesDuplicate: the same name (in any case) and
// version can't be entered twice; another version can.
func TestDirectoryInsertRefusesDuplicate(t *testing.T) {
	_, dir, bm, ib := createVersionFixture(t)
	fid := ondisk.Fid{Num: 40, Seq: 1}

	if err := dir.Insert("D.DAT", 1, fid, bm, ib); err != nil {
		t.Fatal(err)
	}
	if err := dir.Insert("d.dat", 1, fid, bm, ib); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate Insert: %v, want ErrExists", err)
	}
	if err := dir.Insert("D.DAT", 2, fid, bm, ib); err != nil {
		t.Errorf("another version: %v", err)
	}

	if entries, _ := dir.List(); len(entries) != 2 {
		t.Errorf("entries %+v, want two", entries)
	}
}
