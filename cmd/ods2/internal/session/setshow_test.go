package session

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

func TestCmdSetDefault(t *testing.T) {
	s := New()
	s.Default = filespec.Spec{Device: defaultDeviceName, Dirs: []string{"FOO"}}

	if err := cmdSet(s, []string{"default", "[.BAR]"}, nil); err != nil {
		t.Fatalf("cmdSet: %v", err)
	}

	want := []string{"FOO", "BAR"}

	if len(s.Default.Dirs) != 2 || s.Default.Dirs[0] != want[0] || s.Default.Dirs[1] != want[1] {
		t.Errorf("Default.Dirs = %v, want %v", s.Default.Dirs, want)
	}

	if s.Default.Device != defaultDeviceName {
		t.Errorf("Default.Device = %q, want %q (should be preserved)", s.Default.Device, defaultDeviceName)
	}
}

func TestCmdSetUnrecognizedAttribute(t *testing.T) {
	s := New()
	if err := cmdSet(s, []string{"protection", "SOMETHING"}, nil); err == nil {
		t.Fatal(`cmdSet("protection", ...): want error, got nil`)
	}
}

func TestCmdSetDefaultAbbreviated(t *testing.T) {
	s := New()

	if err := cmdSet(s, []string{"def", "[FOO]"}, nil); err != nil {
		t.Fatalf("cmdSet with abbreviated sub-verb: %v", err)
	}
}

func TestCmdShowDefault(t *testing.T) {
	var out bytes.Buffer

	s := New()
	s.Stdout = &out
	s.Default = filespec.Spec{Device: defaultDeviceName, Dirs: []string{"FOO"}}

	if err := cmdShow(s, []string{"default"}, nil); err != nil {
		t.Fatalf("cmdShow: %v", err)
	}

	if !strings.Contains(out.String(), "DUA0:[FOO]") {
		t.Errorf("show default output = %q, want it to contain %q", out.String(), "DUA0:[FOO]")
	}
}

func TestCmdShowTime(t *testing.T) {
	var out bytes.Buffer

	s := New()
	s.Stdout = &out

	if err := cmdShow(s, []string{"time"}, nil); err != nil {
		t.Fatalf("cmdShow: %v", err)
	}
	// Just confirm something plausible was printed; the exact instant is
	// inherently untestable here.
	if out.Len() < len("01-JAN-1970 00:00:00.00") {
		t.Errorf("show time output = %q, looks too short to be a timestamp", out.String())
	}
}

func TestCmdShowUnrecognizedAttribute(t *testing.T) {
	s := New()

	if err := cmdShow(s, []string{"protection"}, nil); err == nil {
		t.Fatal(`cmdShow("protection"): want error, got nil`)
	}
}

// newSetFileTestSession builds a session with one mounted, writable volume
// (device "DUA0") containing FOO.TXT;1, FOO.TXT;2, BAR.TXT;1 in the master
// file directory and one empty subdirectory, SUBDIR.DIR -- enough to
// exercise `set file/version_limit=n` against a plain file (with and
// without a wildcard) and against a directory's own file spec. Follows
// delete_test.go's newDeleteTestSession pattern (diskimage.Create +
// volume.Initialize), since SetVersionLimit needs a genuine
// diskimage.WritableContainer.
func newSetFileTestSession(t *testing.T) *Session {
	t.Helper()

	path := filepath.Join(t.TempDir(), "setfile.dsk")

	c, err := diskimage.Create(path, 600)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "SETVOL"}); err != nil {
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

	for _, name := range []string{"FOO.TXT", "FOO.TXT", "BAR.TXT"} {
		f, err := vol.CreateFile(mfd, name, ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF}, bm, ib)
		if err != nil {
			t.Fatalf("CreateFile(%s): %v", name, err)
		}

		if err := f.Close(); err != nil {
			t.Fatalf("Close(%s): %v", name, err)
		}
	}

	if _, err := vol.CreateDirectory(mfd, "SUBDIR.DIR", 0, bm, ib); err != nil {
		t.Fatalf("CreateDirectory(SUBDIR.DIR): %v", err)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}

	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	var out bytes.Buffer

	s := New()
	s.Stdout = &out
	s.Volumes[defaultDeviceName] = vol
	s.Default.Device = defaultDeviceName

	return s
}

// versionLimitOf looks up name;version in dir and returns its own header's
// RecordAttributes.VersionLimit, for asserting what cmdSetFile actually
// wrote to disk (via a fresh Directory.Lookup/Volume.OpenFID, not any
// in-memory state cmdSetFile itself held).
func versionLimitOf(t *testing.T, vol *volume.Volume, dir *volume.Directory, name string, version uint16) uint16 {
	t.Helper()

	entry, err := dir.Lookup(name, version)
	if err != nil {
		t.Fatalf("Lookup(%s;%d): %v", name, version, err)
	}

	f, err := vol.OpenFID(entry.Fid)
	if err != nil {
		t.Fatalf("OpenFID(%s;%d): %v", name, version, err)
	}

	return f.Header.RecordAttributes.VersionLimit
}

func TestCmdSetFileSetsVersionLimitOnPlainFile(t *testing.T) {
	s := newSetFileTestSession(t)
	vol := s.Volumes[defaultDeviceName]

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdSet(s, []string{"file", "BAR.TXT"}, Qualifiers{"version_limit": "3"}); err != nil {
		t.Fatalf("cmdSet(file): %v", err)
	}

	if got := versionLimitOf(t, vol, mfd, "BAR.TXT", 1); got != 3 {
		t.Errorf("BAR.TXT;1's VersionLimit = %d, want 3", got)
	}

	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "%SET-S-SET, BAR.TXT;1 version limit set to 3") {
		t.Errorf("output = %q, want a SET-S-SET confirmation for BAR.TXT;1", out)
	}
}

func TestCmdSetFileWithoutVersionTargetsHighest(t *testing.T) {
	s := newSetFileTestSession(t)
	vol := s.Volumes[defaultDeviceName]

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdSet(s, []string{"file", "FOO.TXT"}, Qualifiers{"version_limit": "5"}); err != nil {
		t.Fatalf("cmdSet(file): %v", err)
	}

	if got := versionLimitOf(t, vol, mfd, "FOO.TXT", 2); got != 5 {
		t.Errorf("FOO.TXT;2 (highest)'s VersionLimit = %d, want 5", got)
	}

	if got := versionLimitOf(t, vol, mfd, "FOO.TXT", 1); got != 0 {
		t.Errorf("FOO.TXT;1's VersionLimit = %d, want 0 (untouched)", got)
	}
}

func TestCmdSetFileWildcardSetsEveryMatch(t *testing.T) {
	s := newSetFileTestSession(t)
	vol := s.Volumes[defaultDeviceName]

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdSet(s, []string{"file", "*.TXT;*"}, Qualifiers{"version_limit": "9"}); err != nil {
		t.Fatalf("cmdSet(file): %v", err)
	}

	if got := versionLimitOf(t, vol, mfd, "FOO.TXT", 1); got != 9 {
		t.Errorf("FOO.TXT;1's VersionLimit = %d, want 9", got)
	}

	if got := versionLimitOf(t, vol, mfd, "FOO.TXT", 2); got != 9 {
		t.Errorf("FOO.TXT;2's VersionLimit = %d, want 9", got)
	}

	if got := versionLimitOf(t, vol, mfd, "BAR.TXT", 1); got != 9 {
		t.Errorf("BAR.TXT;1's VersionLimit = %d, want 9", got)
	}
}

// TestCmdSetFileTargetsDirectorySpec confirms `set file/version_limit`
// works against a directory's own file spec (SUBDIR.DIR, matching COPY's
// own "a directory entry is just a NAME.DIR file" convention) exactly like
// an ordinary file -- and that a name subsequently created inside that
// directory inherits the newly-set value, the same subtask 5/6
// integration TestSetVersionLimitOnDirectoryAffectsFutureInheritance
// already covers at the volume-API layer, exercised here through the CLI.
func TestCmdSetFileTargetsDirectorySpec(t *testing.T) {
	s := newSetFileTestSession(t)
	vol := s.Volumes[defaultDeviceName]

	if err := cmdSet(s, []string{"file", "SUBDIR.DIR"}, Qualifiers{"version_limit": "4"}); err != nil {
		t.Fatalf("cmdSet(file) on a directory spec: %v", err)
	}

	sub, err := filespec.ResolveDirectory(vol, []string{"SUBDIR"})
	if err != nil {
		t.Fatalf("ResolveDirectory(SUBDIR): %v", err)
	}

	if got := sub.Header.RecordAttributes.VersionLimit; got != 4 {
		t.Errorf("SUBDIR.DIR's own VersionLimit = %d, want 4", got)
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

	child, err := vol.CreateFile(sub, "CHILD.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile(CHILD.DAT): %v", err)
	}

	if err := child.Close(); err != nil {
		t.Fatalf("Close(CHILD.DAT): %v", err)
	}

	if got := child.Header.RecordAttributes.VersionLimit; got != 4 {
		t.Errorf("new file created inside SUBDIR after SET FILE's inherited VersionLimit = %d, want 4", got)
	}
}

func TestCmdSetFileMissingQualifierRejected(t *testing.T) {
	s := newSetFileTestSession(t)

	if err := cmdSet(s, []string{"file", "BAR.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdSet(file) without /VERSION_LIMIT: want error, got nil")
	}
}

func TestCmdSetFileInvalidValueRejected(t *testing.T) {
	s := newSetFileTestSession(t)

	if err := cmdSet(s, []string{"file", "BAR.TXT"}, Qualifiers{"version_limit": "abc"}); err == nil {
		t.Fatal("cmdSet(file) with a non-numeric /VERSION_LIMIT: want error, got nil")
	}

	if err := cmdSet(s, []string{"file", "BAR.TXT"}, Qualifiers{"version_limit": "-1"}); err == nil {
		t.Fatal("cmdSet(file) with a negative /VERSION_LIMIT: want error, got nil")
	}
}

func TestCmdSetFileNonexistentTargetErrors(t *testing.T) {
	s := newSetFileTestSession(t)

	if err := cmdSet(s, []string{"file", "NOSUCHFILE.TXT"}, Qualifiers{"version_limit": "1"}); err == nil {
		t.Fatal("cmdSet(file) on a nonexistent file: want error, got nil")
	}
}

func TestSetFileIntegrationViaExecute(t *testing.T) {
	s := newSetFileTestSession(t)
	vol := s.Volumes[defaultDeviceName]

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if _, err := s.Execute("set file /version_limit=2 BAR.TXT"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := versionLimitOf(t, vol, mfd, "BAR.TXT", 1); got != 2 {
		t.Errorf("BAR.TXT;1's VersionLimit = %d, want 2", got)
	}
}

func TestSetShowIntegrationViaExecute(t *testing.T) {
	var out bytes.Buffer

	s := New()
	s.Stdout = &out

	if _, err := s.Execute("set default [FOO.BAR]"); err != nil {
		t.Fatalf("Execute(set default): %v", err)
	}

	if _, err := s.Execute("show default"); err != nil {
		t.Fatalf("Execute(show default): %v", err)
	}

	if !strings.Contains(out.String(), "[FOO.BAR]") {
		t.Errorf("output = %q, want it to contain %q", out.String(), "[FOO.BAR]")
	}
}
