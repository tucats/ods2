package volume

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// renameFixture is a freshly initialized, writable volume with two empty
// subdirectories, [SUB1] and [SUB2], for Rename's tests.
type renameFixture struct {
	vol        *Volume
	mfd        *Directory
	sub1, sub2 *Directory
	bm         *Bitmap
	ib         *IndexBitmap
}

func newRenameFixture(t *testing.T) renameFixture {
	t.Helper()

	vol := newInitializedTestVolume(t, "RENAME")
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

	fx := renameFixture{vol: vol, mfd: mfd, bm: bm, ib: ib}

	if fx.sub1, err = vol.CreateDirectory(mfd, "SUB1.DIR", 0, bm, ib); err != nil {
		t.Fatal(err)
	}

	if fx.sub2, err = vol.CreateDirectory(mfd, "SUB2.DIR", 0, bm, ib); err != nil {
		t.Fatal(err)
	}

	return fx
}

// newInitializedTestVolume initializes and mounts a fresh 2000-block
// volume labeled label.
func newInitializedTestVolume(t *testing.T, label string) *Volume {
	t.Helper()

	c, err := diskimage.Create(filepath.Join(t.TempDir(), label+".dsk"), 2000)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if err := Initialize(c, InitializeOptions{Label: label}); err != nil {
		t.Fatal(err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatal(err)
	}

	return vol
}

// create makes name (the next version) in dir and returns its FID.
func (fx renameFixture) create(t *testing.T, dir *Directory, name string, version uint16) ondisk.Fid {
	t.Helper()

	f, err := fx.vol.CreateFileVersion(dir, name, version, ondisk.RecAttr{}, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	return f.Header.Fid
}

// header reads fid's header back from the disk.
func (fx renameFixture) header(t *testing.T, fid ondisk.Fid) (ondisk.FileHeader, ondisk.Ident) {
	t.Helper()

	f, err := fx.vol.OpenFID(fid)
	if err != nil {
		t.Fatal(err)
	}

	id, err := f.Header.Ident()
	if err != nil {
		t.Fatal(err)
	}

	return f.Header, id
}

// assertEntry checks that dir has name;version, pointing at fid.
func assertEntry(t *testing.T, dir *Directory, name string, version uint16, fid ondisk.Fid) {
	t.Helper()

	e, err := dir.Lookup(name, version)
	if err != nil {
		t.Fatalf("Lookup(%s;%d): %v", name, version, err)
	}

	if e.Fid != fid {
		t.Errorf("%s;%d points at %v, want %v", name, version, e.Fid, fid)
	}
}

// assertNoEntry checks that dir has no name;version (0: no version at all).
func assertNoEntry(t *testing.T, dir *Directory, name string, version uint16) {
	t.Helper()

	if _, err := dir.Lookup(name, version); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup(%s;%d) = %v, want ErrNotFound", name, version, err)
	}
}

// TestRenameInSameDirectory: the file keeps its FID, loses its old entry,
// and its header records the new name as a revision.
func TestRenameInSameDirectory(t *testing.T) {
	fx := newRenameFixture(t)
	fid := fx.create(t, fx.sub1, "OLD.TXT", 0)

	_, before := fx.header(t, fid)

	got, err := fx.vol.Rename(fx.sub1, "OLD.TXT", 0, fx.sub1, "NEW.DAT", 0, fx.bm, fx.ib)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if got.Fid != fid || got.Name != "NEW.DAT" || got.Version != 1 || got.OldName != "OLD.TXT" || got.OldVersion != 1 {
		t.Errorf("Rename = %+v", got)
	}

	assertNoEntry(t, fx.sub1, "OLD.TXT", 0)
	assertEntry(t, fx.sub1, "NEW.DAT", 1, fid)

	h, id := fx.header(t, fid)
	if h.Backlink != fx.sub1.Header.Fid {
		t.Errorf("back link = %v, want [SUB1] %v", h.Backlink, fx.sub1.Header.Fid)
	}

	if id.Filename != "NEW.DAT;1" {
		t.Errorf("header name = %q, want NEW.DAT;1", id.Filename)
	}

	if id.Revision != before.Revision+1 {
		t.Errorf("revision = %d, want %d", id.Revision, before.Revision+1)
	}

	if id.CreationDate != before.CreationDate {
		t.Errorf("creation date changed: %v -> %v", before.CreationDate, id.CreationDate)
	}
}

// TestRenameToAnotherDirectory moves a file, which moves its back link.
func TestRenameToAnotherDirectory(t *testing.T) {
	fx := newRenameFixture(t)
	fid := fx.create(t, fx.sub1, "MOVE.ME", 0)

	if _, err := fx.vol.Rename(fx.sub1, "MOVE.ME", 1, fx.sub2, "MOVE.ME", 0, fx.bm, fx.ib); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	assertNoEntry(t, fx.sub1, "MOVE.ME", 0)
	assertEntry(t, fx.sub2, "MOVE.ME", 1, fid)

	if h, _ := fx.header(t, fid); h.Backlink != fx.sub2.Header.Fid {
		t.Errorf("back link = %v, want [SUB2] %v", h.Backlink, fx.sub2.Header.Fid)
	}
}

// TestRenameVersions covers the version rules: old version 0 is the
// highest, new version 0 is the next, and an explicit new version is used
// as given.
func TestRenameVersions(t *testing.T) {
	fx := newRenameFixture(t)
	fx.create(t, fx.sub1, "A.TXT", 1)
	a2 := fx.create(t, fx.sub1, "A.TXT", 2)
	b := fx.create(t, fx.sub1, "B.TXT", 4)

	// A.TXT (highest, ;2) -> B.TXT (next, ;5).
	got, err := fx.vol.Rename(fx.sub1, "A.TXT", 0, fx.sub1, "B.TXT", 0, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	if got.OldVersion != 2 || got.Version != 5 {
		t.Errorf("renamed ;%d to ;%d, want ;2 to ;5", got.OldVersion, got.Version)
	}

	assertEntry(t, fx.sub1, "B.TXT", 5, a2)
	assertEntry(t, fx.sub1, "B.TXT", 4, b)
	assertNoEntry(t, fx.sub1, "A.TXT", 2)

	// B.TXT;4 -> B.TXT;9: an explicit version.
	if _, err := fx.vol.Rename(fx.sub1, "B.TXT", 4, fx.sub1, "B.TXT", 9, fx.bm, fx.ib); err != nil {
		t.Fatal(err)
	}

	assertEntry(t, fx.sub1, "B.TXT", 9, b)
	assertNoEntry(t, fx.sub1, "B.TXT", 4)
}

// TestRenameOnlyVersionToSameNameStartsOver: the next version is computed
// after the old entry is gone, so a name's only version renamed to the
// same name with no version becomes ;1, as on VMS.
func TestRenameOnlyVersionToSameNameStartsOver(t *testing.T) {
	fx := newRenameFixture(t)
	fid := fx.create(t, fx.sub1, "ONLY.TXT", 7)

	got, err := fx.vol.Rename(fx.sub1, "ONLY.TXT", 7, fx.sub1, "ONLY.TXT", 0, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}

	assertEntry(t, fx.sub1, "ONLY.TXT", 1, fid)
	assertNoEntry(t, fx.sub1, "ONLY.TXT", 7)
}

// TestRenameToItself is allowed and leaves the entry where it was.
func TestRenameToItself(t *testing.T) {
	fx := newRenameFixture(t)
	fid := fx.create(t, fx.sub1, "SAME.TXT", 3)

	if _, err := fx.vol.Rename(fx.sub1, "SAME.TXT", 3, fx.sub1, "same.txt", 3, fx.bm, fx.ib); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	assertEntry(t, fx.sub1, "SAME.TXT", 3, fid)
}

// TestRenameRefusalsChangeNothing: an existing new version, a missing old
// file, and a version above 32767 are all refused before anything
// changes.
func TestRenameRefusalsChangeNothing(t *testing.T) {
	fx := newRenameFixture(t)
	a := fx.create(t, fx.sub1, "A.TXT", 1)
	b := fx.create(t, fx.sub2, "B.TXT", 1)

	if _, err := fx.vol.Rename(fx.sub1, "A.TXT", 1, fx.sub2, "B.TXT", 1, fx.bm, fx.ib); !errors.Is(err, ErrExists) {
		t.Errorf("onto an existing version: %v, want ErrExists", err)
	}

	if _, err := fx.vol.Rename(fx.sub1, "NOPE.TXT", 0, fx.sub2, "X.TXT", 0, fx.bm, fx.ib); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing file: %v, want ErrNotFound", err)
	}

	if _, err := fx.vol.Rename(fx.sub1, "A.TXT", 2, fx.sub2, "X.TXT", 0, fx.bm, fx.ib); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing version: %v, want ErrNotFound", err)
	}

	if _, err := fx.vol.Rename(fx.sub1, "A.TXT", 1, fx.sub2, "X.TXT", 32768, fx.bm, fx.ib); !errors.Is(err, ErrBadVersion) {
		t.Errorf("version 32768: %v, want ErrBadVersion", err)
	}

	assertEntry(t, fx.sub1, "A.TXT", 1, a)
	assertEntry(t, fx.sub2, "B.TXT", 1, b)
	assertNoEntry(t, fx.sub2, "X.TXT", 0)
}

// TestRenameAcrossVolumesRefused: a directory on another volume can't
// receive the file.
func TestRenameAcrossVolumesRefused(t *testing.T) {
	fx := newRenameFixture(t)
	a := fx.create(t, fx.sub1, "A.TXT", 1)

	other := newInitializedTestVolume(t, "OTHER")

	otherMFD, err := other.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fx.vol.Rename(fx.sub1, "A.TXT", 1, otherMFD, "A.TXT", 0, fx.bm, fx.ib); !errors.Is(err, ErrCrossVolume) {
		t.Errorf("Rename to another volume: %v, want ErrCrossVolume", err)
	}

	assertEntry(t, fx.sub1, "A.TXT", 1, a)
}

// TestRenameMovesDirectory moves [SUB2] under [SUB1]: its files come
// with it, and its back link names its new parent.
func TestRenameMovesDirectory(t *testing.T) {
	fx := newRenameFixture(t)
	inside := fx.create(t, fx.sub2, "INSIDE.TXT", 0)

	if _, err := fx.vol.Rename(fx.mfd, "SUB2.DIR", 1, fx.sub1, "SUB2.DIR", 1, fx.bm, fx.ib); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	assertNoEntry(t, fx.mfd, "SUB2.DIR", 0)
	assertEntry(t, fx.sub1, "SUB2.DIR", 1, fx.sub2.Header.Fid)

	h, _ := fx.header(t, fx.sub2.Header.Fid)
	if h.Backlink != fx.sub1.Header.Fid {
		t.Errorf("[SUB1.SUB2]'s back link = %v, want [SUB1] %v", h.Backlink, fx.sub1.Header.Fid)
	}

	if !h.IsDirectory() {
		t.Error("the moved directory is no longer a directory")
	}

	moved, err := fx.vol.OpenDirectory(fx.sub2.Header.Fid)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, moved, "INSIDE.TXT", 1, inside)
}

// TestRenameDirectoryIntoItselfRefused: a directory can't move into
// itself or below itself -- including the master file directory, which
// everything is below.
func TestRenameDirectoryIntoItselfRefused(t *testing.T) {
	fx := newRenameFixture(t)

	deeper, err := fx.vol.CreateDirectory(fx.sub1, "DEEPER.DIR", 0, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []struct {
		what string
		dir  *Directory
	}{
		{"itself", fx.sub1},
		{"its subdirectory", deeper},
	} {
		if _, err := fx.vol.Rename(fx.mfd, "SUB1.DIR", 1, target.dir, "SUB1.DIR", 1, fx.bm, fx.ib); !errors.Is(err, ErrDirectoryLoop) {
			t.Errorf("[SUB1] into %s: %v, want ErrDirectoryLoop", target.what, err)
		}
	}

	if _, err := fx.vol.Rename(fx.mfd, "000000.DIR", 1, fx.sub2, "000000.DIR", 1, fx.bm, fx.ib); !errors.Is(err, ErrDirectoryLoop) {
		t.Errorf("the MFD into [SUB2]: %v, want ErrDirectoryLoop", err)
	}

	assertEntry(t, fx.mfd, "SUB1.DIR", 1, fx.sub1.Header.Fid)
	assertEntry(t, fx.mfd, "000000.DIR", 1, ondisk.MasterFileDirectoryFid)

	// Renaming a directory within its own parent is fine.
	if _, err := fx.vol.Rename(fx.mfd, "SUB1.DIR", 1, fx.mfd, "FIRST.DIR", 1, fx.bm, fx.ib); err != nil {
		t.Errorf("renaming [SUB1] to [FIRST]: %v", err)
	}
}

// TestRenamePurgesToVersionLimit: entering a version of a name already at
// its limit deletes the name's oldest version -- never the renamed file.
func TestRenamePurgesToVersionLimit(t *testing.T) {
	fx := newRenameFixture(t)

	limited, err := fx.vol.CreateDirectory(fx.mfd, "LIMITED.DIR", 2, fx.bm, fx.ib)
	if err != nil {
		t.Fatal(err)
	}

	fx.create(t, limited, "C.DAT", 5)
	c6 := fx.create(t, limited, "C.DAT", 6)
	mover := fx.create(t, fx.sub1, "MOVER.DAT", 0)

	// An explicit version below both: the renamed file stays, ;5 goes.
	got, err := fx.vol.Rename(fx.sub1, "MOVER.DAT", 0, limited, "C.DAT", 2, fx.bm, fx.ib)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if len(got.Purged) != 1 || got.Purged[0] != 5 {
		t.Errorf("purged %v, want [5]", got.Purged)
	}

	assertEntry(t, limited, "C.DAT", 2, mover)
	assertEntry(t, limited, "C.DAT", 6, c6)
	assertNoEntry(t, limited, "C.DAT", 5)

	if h, _ := fx.header(t, mover); h.RecordAttributes.VersionLimit != 2 {
		t.Errorf("the renamed file's version limit = %d, want the name's, 2", h.RecordAttributes.VersionLimit)
	}
}

// TestRenameAliasLeavesHeader: renaming a secondary entry (a second name
// for a file whose back link is elsewhere) changes only the entry.
func TestRenameAliasLeavesHeader(t *testing.T) {
	fx := newRenameFixture(t)
	fid := fx.create(t, fx.sub1, "REAL.TXT", 0)

	if err := fx.sub2.Insert("ALIAS.TXT", 1, fid, fx.bm, fx.ib); err != nil {
		t.Fatal(err)
	}

	before, beforeID := fx.header(t, fid)

	if _, err := fx.vol.Rename(fx.sub2, "ALIAS.TXT", 1, fx.sub2, "OTHER.TXT", 0, fx.bm, fx.ib); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	assertEntry(t, fx.sub2, "OTHER.TXT", 1, fid)
	assertEntry(t, fx.sub1, "REAL.TXT", 1, fid)

	after, afterID := fx.header(t, fid)
	if after.Backlink != before.Backlink || afterID != beforeID {
		t.Errorf("the header changed: %v %+v -> %v %+v", before.Backlink, beforeID, after.Backlink, afterID)
	}
}

// TestRenameSurvivesRemount: the rename is on the disk, not just in
// memory.
func TestRenameSurvivesRemount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remount.dsk")

	c, err := diskimage.Create(path, 2000)
	if err != nil {
		t.Fatal(err)
	}

	if err := Initialize(c, InitializeOptions{Label: "REMOUNT"}); err != nil {
		t.Fatal(err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatal(err)
	}

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

	f, err := vol.CreateFile(mfd, "KEEP.TXT", ondisk.RecAttr{}, bm, ib)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := vol.Rename(mfd, "KEEP.TXT", 0, mfd, "KEPT.TXT", 0, bm, ib); err != nil {
		t.Fatal(err)
	}

	// Dismount flushes the bitmaps and closes c.
	if err := vol.Dismount(); err != nil {
		t.Fatal(err)
	}

	c2, err := diskimage.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = c2.Close() }()

	vol2, err := Mount(c2)
	if err != nil {
		t.Fatal(err)
	}

	mfd2, err := vol2.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatal(err)
	}

	assertEntry(t, mfd2, "KEPT.TXT", 1, f.Header.Fid)
	assertNoEntry(t, mfd2, "KEEP.TXT", 0)

	report, err := AnalyzeDisk(vol2.Devices[0])
	if err != nil {
		t.Fatal(err)
	}

	if !report.Clean() {
		t.Errorf("ANALYZE/DISK after the rename: %+v", report)
	}
}
