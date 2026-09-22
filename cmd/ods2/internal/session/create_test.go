package session

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// newCreateTestSession builds a session with one mounted, writable volume
// (device "DUA0") on a real backing file -- CreateDirectory needs a
// genuine diskimage.WritableContainer, the same reason
// newDeleteTestSession (delete_test.go) doesn't reuse the read-only,
// in-memory newTypeTestSession fixture either. The master file directory
// starts with one existing, empty subdirectory, EXISTING.DIR, for tests
// that create a nested directory inside an already-existing one; nothing
// here sets the MFD's own VersionLimit explicitly (it stays 0, "unlimited"
// -- volume.Initialize's own default), since there's no SetVersionLimit
// API yet (docs/PHASE-03.md subtask 6, not this feature's job) to
// override it after the fact -- TestCmdCreateDirectoryInheritsParent
// VersionLimit below demonstrates non-zero inheritance a different way,
// by creating an explicit-/VERSION parent first.
func newCreateTestSession(t *testing.T) *Session {
	t.Helper()

	path := filepath.Join(t.TempDir(), "create.dsk")
	c, err := diskimage.Create(path, 600)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "CREVOL"}); err != nil {
		t.Fatalf("volume.Initialize: %v", err)
	}

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("volume.Mount: %v", err)
	}

	dev := vol.Devices[0]
	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}
	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap: %v", err)
	}

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if _, err := vol.CreateDirectory(mfd, "EXISTING.DIR", 0, bm, ib); err != nil {
		t.Fatalf("CreateDirectory(EXISTING.DIR): %v", err)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	s := New()
	var out bytes.Buffer
	s.Stdout = &out
	s.Volumes["DUA0"] = vol
	s.Default.Device = "DUA0"
	return s
}

func TestCmdCreateDirectoryAtTopLevel(t *testing.T) {
	s := newCreateTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdCreate(s, []string{"directory", "[NEWDIR]"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCreate: %v", err)
	}

	entry, err := mfd.Lookup("NEWDIR.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(NEWDIR.DIR): %v", err)
	}
	if entry.Version != 1 {
		t.Errorf("NEWDIR.DIR version = %d, want 1", entry.Version)
	}

	newDir, err := s.Volumes["DUA0"].OpenDirectory(entry.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory(NEWDIR.DIR): %v", err)
	}
	if got := newDir.Header.RecordAttributes.VersionLimit; got != 0 {
		t.Errorf("NEWDIR.DIR's VersionLimit = %d, want 0 (inherited from the MFD's own default)", got)
	}
	if !newDir.Header.IsDirectory() {
		t.Error("NEWDIR.DIR's header doesn't have the directory characteristic set")
	}

	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "%CREATE-S-CREATED") || !strings.Contains(out, "NEWDIR.DIR;1") {
		t.Errorf("output = %q, want a CREATE-S-CREATED confirmation naming NEWDIR.DIR;1", out)
	}
}

func TestCmdCreateDirectoryExplicitVersionOverridesInherited(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[OVERRIDE]"}, Qualifiers{"version": "9"}); err != nil {
		t.Fatalf("cmdCreate: %v", err)
	}

	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	entry, err := mfd.Lookup("OVERRIDE.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(OVERRIDE.DIR): %v", err)
	}
	newDir, err := s.Volumes["DUA0"].OpenDirectory(entry.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory(OVERRIDE.DIR): %v", err)
	}
	if got := newDir.Header.RecordAttributes.VersionLimit; got != 9 {
		t.Errorf("VersionLimit = %d, want 9 (explicit /VERSION override)", got)
	}
}

// TestCmdCreateDirectoryInheritsParentVersionLimit confirms inheritance
// picks up a genuinely non-zero value, not just the MFD's own untouched
// default (0): a parent created with an explicit /VERSION=4, followed by
// a child created with no /VERSION of its own, must end up with the
// parent's 4 -- proving the child actually reads its immediate parent's
// current VersionLimit rather than, say, always defaulting to 0 or to the
// MFD's.
func TestCmdCreateDirectoryInheritsParentVersionLimit(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[VERPARENT]"}, Qualifiers{"version": "4"}); err != nil {
		t.Fatalf("cmdCreate (parent): %v", err)
	}
	if err := cmdCreate(s, []string{"directory", "[VERPARENT.CHILD]"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCreate (child): %v", err)
	}

	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	parentEntry, err := mfd.Lookup("VERPARENT.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(VERPARENT.DIR): %v", err)
	}
	parentDir, err := s.Volumes["DUA0"].OpenDirectory(parentEntry.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory(VERPARENT.DIR): %v", err)
	}
	childEntry, err := parentDir.Lookup("CHILD.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(CHILD.DIR): %v", err)
	}
	childDir, err := s.Volumes["DUA0"].OpenDirectory(childEntry.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory(CHILD.DIR): %v", err)
	}
	if got := childDir.Header.RecordAttributes.VersionLimit; got != 4 {
		t.Errorf("CHILD.DIR's VersionLimit = %d, want 4 (inherited from VERPARENT.DIR)", got)
	}
}

func TestCmdCreateDirectoryNested(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[EXISTING.CHILD]"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCreate: %v", err)
	}

	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	existingEntry, err := mfd.Lookup("EXISTING.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(EXISTING.DIR): %v", err)
	}
	existingDir, err := s.Volumes["DUA0"].OpenDirectory(existingEntry.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory(EXISTING.DIR): %v", err)
	}
	if _, err := existingDir.Lookup("CHILD.DIR", 0); err != nil {
		t.Errorf("Lookup(CHILD.DIR) inside EXISTING.DIR: %v", err)
	}
}

func TestCmdCreateDirectoryUnderMissingParentErrors(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[NOSUCHPARENT.CHILD]"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCreate with a nonexistent parent: want error, got nil")
	}
}

func TestCmdCreateDirectoryRejectsFileSpec(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[NEWDIR]FOO.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCreate with a trailing file name: want error, got nil")
	}
}

func TestCmdCreateDirectoryRejectsBareName(t *testing.T) {
	s := newCreateTestSession(t)

	// Real VMS's own CREATE/DIRECTORY always requires bracketed directory
	// syntax; a bare word is parsed as a file name, not a directory path.
	if err := cmdCreate(s, []string{"directory", "NEWDIR"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCreate with an unbracketed name: want error, got nil")
	}
}

func TestCmdCreateDirectoryRejectsMasterFileDirectorySpec(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[000000]"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCreate targeting [000000] (no new name given): want error, got nil")
	}
}

func TestCmdCreateDirectoryInvalidVersionQualifier(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"directory", "[BAD]"}, Qualifiers{"version": "notanumber"}); err == nil {
		t.Fatal("cmdCreate with a non-numeric /VERSION: want error, got nil")
	}
}

func TestCmdCreateUnrecognizedObjectErrors(t *testing.T) {
	s := newCreateTestSession(t)

	if err := cmdCreate(s, []string{"file", "[NEWDIR]"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCreate with an unrecognized object: want error, got nil")
	}
}

func TestCreateDirectoryIntegrationViaExecute(t *testing.T) {
	s := newCreateTestSession(t)

	if _, err := s.Execute("create directory [VIAEXEC] /version=2"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	entry, err := mfd.Lookup("VIAEXEC.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup(VIAEXEC.DIR): %v", err)
	}
	newDir, err := s.Volumes["DUA0"].OpenDirectory(entry.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory(VIAEXEC.DIR): %v", err)
	}
	if got := newDir.Header.RecordAttributes.VersionLimit; got != 2 {
		t.Errorf("VersionLimit = %d, want 2", got)
	}
}
