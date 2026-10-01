package filespec

import (
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// newCreateDirVolume is a freshly initialized, writable volume, and its
// bitmap caches.
func newCreateDirVolume(t *testing.T, blocks uint32) (*volume.Volume, *volume.Bitmap, *volume.IndexBitmap) {
	t.Helper()

	c, err := diskimage.Create(filepath.Join(t.TempDir(), "credir.dsk"), blocks)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "CREDIR"}); err != nil {
		t.Fatalf("volume.Initialize: %v", err)
	}

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("volume.Mount: %v", err)
	}

	bm, err := vol.Devices[0].Bitmap()
	if err != nil {
		t.Fatal(err)
	}

	ib, err := vol.Devices[0].IndexBitmap()
	if err != nil {
		t.Fatal(err)
	}

	return vol, bm, ib
}

func createdFlags(levels []CreatedDirectory) []string {
	var out []string

	for _, l := range levels {
		flag := "existed"
		if l.Created {
			flag = "created"
		}

		out = append(out, l.String()+" "+flag)
	}

	return out
}

func TestCreateDirectoryPathMakesMissingLevels(t *testing.T) {
	vol, bm, ib := newCreateDirVolume(t, 2000)

	levels, err := CreateDirectoryPath(vol, []string{"a", "B"}, nil, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectoryPath([A.B]): %v", err)
	}

	if got := createdFlags(levels); len(got) != 2 || got[0] != "[A] created" || got[1] != "[A.B] created" {
		t.Errorf("first call = %v, want [A] and [A.B] created", got)
	}

	// Again, one level deeper: the first two now exist.
	levels, err = CreateDirectoryPath(vol, []string{"A", "B", "C"}, nil, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectoryPath([A.B.C]): %v", err)
	}

	if got := createdFlags(levels); len(got) != 3 || got[0] != "[A] existed" || got[1] != "[A.B] existed" || got[2] != "[A.B.C] created" {
		t.Errorf("second call = %v, want [A], [A.B] existed and [A.B.C] created", got)
	}

	// The new directory is reachable the usual way, and is a child of
	// [A.B].
	c, err := ResolveDirectory(vol, []string{"A", "B", "C"})
	if err != nil {
		t.Fatalf("ResolveDirectory([A.B.C]): %v", err)
	}

	if c.Header.Backlink != levels[1].Directory.Header.Fid {
		t.Errorf("[A.B.C]'s backlink = %v, want [A.B]'s %v", c.Header.Backlink, levels[1].Directory.Header.Fid)
	}
}

func TestCreateDirectoryPathMFD(t *testing.T) {
	vol, bm, ib := newCreateDirVolume(t, 2000)

	levels, err := CreateDirectoryPath(vol, nil, nil, bm, ib)
	if err != nil || len(levels) != 1 || levels[0].Created || levels[0].String() != "[000000]" {
		t.Errorf("CreateDirectoryPath([000000]) = %v, %v, want the MFD, existing", createdFlags(levels), err)
	}
}

// TestCreateDirectoryPathOptionsPerLevel: each level's options are worked
// out against that level's own parent, so a limit and protection inherited
// down three new levels come from each one's parent in turn.
func TestCreateDirectoryPathOptionsPerLevel(t *testing.T) {
	vol, bm, ib := newCreateDirVolume(t, 2000)

	owner := ondisk.Uic{Group: 0o300, Member: 0o301}

	var parents []string

	options := func(parent *volume.Directory) volume.DirectoryOptions {
		ident, _ := parent.Header.Ident()
		parents = append(parents, ident.Filename)

		o := volume.InheritedDirectoryOptions(parent)
		o.Owner = &owner

		if parent.Header.Fid.Equal(ondisk.MasterFileDirectoryFid) {
			o.VersionLimit = 2
		}

		return o
	}

	levels, err := CreateDirectoryPath(vol, []string{"X", "Y", "Z"}, options, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectoryPath: %v", err)
	}

	if len(parents) != 3 {
		t.Errorf("options called for %v, want three parents", parents)
	}

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatal(err)
	}

	wantProtection := mfd.Header.FileProtection | ondisk.ProtectionNoDeleteAll

	for _, l := range levels {
		h := l.Directory.Header
		if h.Owner != owner || h.RecordAttributes.VersionLimit != 2 || h.FileProtection != wantProtection {
			t.Errorf("%s: owner %v, limit %d, protection %#x; want %v, 2, %#x",
				l, h.Owner, h.RecordAttributes.VersionLimit, h.FileProtection, owner, wantProtection)
		}
	}
}

func TestCreateDirectoryPathRefusesWildcards(t *testing.T) {
	vol, bm, ib := newCreateDirVolume(t, 2000)

	for _, dirs := range [][]string{{"A*"}, {"A", "%"}, {"A", ""}} {
		if _, err := CreateDirectoryPath(vol, dirs, nil, bm, ib); err == nil {
			t.Errorf("CreateDirectoryPath(%q): want an error, got none", dirs)
		}
	}

	if _, err := ResolveDirectory(vol, []string{"A"}); err == nil {
		t.Error("[A] exists after refused creates")
	}
}

// TestCreateDirectoryPathFullDisk: when the disk fills part way down a
// path, the levels already made stay, and are reported with the error.
func TestCreateDirectoryPathFullDisk(t *testing.T) {
	vol, bm, ib := newCreateDirVolume(t, 2000)

	// Leave room for exactly one more single-block directory.
	for bm.FreeClusters() > 1 {
		run := bm.LargestFreeRun()
		if run > bm.FreeClusters()-1 {
			run = bm.FreeClusters() - 1
		}

		e, err := bm.FindFree(run)
		if err != nil {
			t.Fatal(err)
		}

		if err := bm.MarkAllocated(e); err != nil {
			t.Fatal(err)
		}
	}

	levels, err := CreateDirectoryPath(vol, []string{"P", "Q"}, nil, bm, ib)
	if err == nil {
		t.Fatal("CreateDirectoryPath on a full disk: want an error, got none")
	}

	if got := createdFlags(levels); len(got) != 1 || got[0] != "[P] created" {
		t.Errorf("levels reported = %v, want just [P] created", got)
	}

	if _, err := ResolveDirectory(vol, []string{"P"}); err != nil {
		t.Errorf("[P] after the failure: %v", err)
	}
}
