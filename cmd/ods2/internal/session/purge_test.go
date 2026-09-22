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

// newPurgeTestSession builds a session with one mounted, writable volume
// (device "DUA0") on a real backing file -- PurgeVersions needs a genuine
// diskimage.WritableContainer, so this follows delete_test.go's own
// newDeleteTestSession pattern (diskimage.Create + volume.Initialize)
// rather than copying testdata/rq0-ra92.dsk, the same deviation from
// PHASE-03.md's original test-plan sketch that subtask 4's own DELETE
// tests already made (see that subtask's write-up for why: every
// write-path session test in this codebase builds its own small synthetic
// volume, since volume.Initialize makes that just as cheap and keeps the
// fixture's exact layout visible next to the assertions that depend on
// it). The master file directory starts with FOO.TXT;1, FOO.TXT;2,
// FOO.TXT;3, BAR.TXT;1, BAR.TXT;2, and BAZ.TXT;1 -- enough distinct names
// and version counts to exercise the default /LIMIT, an explicit /LIMIT,
// and a glob matching several distinct names in one command.
func newPurgeTestSession(t *testing.T) *Session {
	t.Helper()

	path := filepath.Join(t.TempDir(), "purge.dsk")
	c, err := diskimage.Create(path, 600)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "PURGEVOL"}); err != nil {
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

	for _, name := range []string{"FOO.TXT", "FOO.TXT", "FOO.TXT", "BAR.TXT", "BAR.TXT", "BAZ.TXT"} {
		f, err := vol.CreateFile(mfd, name, ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF}, bm, ib)
		if err != nil {
			t.Fatalf("CreateFile(%s): %v", name, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close(%s): %v", name, err)
		}
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

func TestCmdPurgeDefaultLimitKeepsOnlyHighestVersion(t *testing.T) {
	s := newPurgeTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdPurge(s, []string{"*.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdPurge: %v", err)
	}

	names := dirEntryNames(t, mfd)
	for _, want := range []string{"FOO.TXT;3", "BAR.TXT;2", "BAZ.TXT;1"} {
		if !contains(names, want) {
			t.Errorf("directory entries = %v, want %s to survive (the highest version)", names, want)
		}
	}
	for _, gone := range []string{"FOO.TXT;1", "FOO.TXT;2", "BAR.TXT;1"} {
		if contains(names, gone) {
			t.Errorf("directory entries = %v, want %s removed (default /LIMIT=1)", names, gone)
		}
	}
}

func TestCmdPurgeExplicitLimitKeepsThatManyVersions(t *testing.T) {
	s := newPurgeTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdPurge(s, []string{"FOO.TXT"}, Qualifiers{"limit": "2"}); err != nil {
		t.Fatalf("cmdPurge: %v", err)
	}

	names := dirEntryNames(t, mfd)
	for _, want := range []string{"FOO.TXT;2", "FOO.TXT;3", "BAR.TXT;1", "BAR.TXT;2", "BAZ.TXT;1"} {
		if !contains(names, want) {
			t.Errorf("directory entries = %v, want %s untouched/surviving", names, want)
		}
	}
	if contains(names, "FOO.TXT;1") {
		t.Errorf("directory entries = %v, want FOO.TXT;1 removed (/LIMIT=2)", names)
	}
}

func TestCmdPurgeGlobCoversMultipleDistinctNames(t *testing.T) {
	s := newPurgeTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	// No file-spec at all -- defaults to "*.*", matching every name in
	// the master file directory, including the reserved system files
	// Initialize itself creates (INDEXF.SYS, BITMAP.SYS, ...), each of
	// which only ever has 1 version and must be left alone under the
	// default /LIMIT=1 rather than treated as an error -- and BAZ.TXT,
	// this fixture's own single-version name, for the same reason.
	if err := cmdPurge(s, nil, Qualifiers{}); err != nil {
		t.Fatalf("cmdPurge: %v", err)
	}

	names := dirEntryNames(t, mfd)
	for _, want := range []string{"FOO.TXT;3", "BAR.TXT;2", "BAZ.TXT;1"} {
		if !contains(names, want) {
			t.Errorf("directory entries = %v, want %s to survive", names, want)
		}
	}
	for _, gone := range []string{"FOO.TXT;1", "FOO.TXT;2", "BAR.TXT;1"} {
		if contains(names, gone) {
			t.Errorf("directory entries = %v, want %s removed", names, gone)
		}
	}
}

func TestCmdPurgeInvalidLimitRejected(t *testing.T) {
	s := newPurgeTestSession(t)

	if err := cmdPurge(s, []string{"FOO.TXT"}, Qualifiers{"limit": "abc"}); err == nil {
		t.Fatal("cmdPurge with a non-numeric /LIMIT: want error, got nil")
	}
}

func TestCmdPurgeZeroLimitRejected(t *testing.T) {
	s := newPurgeTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	before := dirEntryNames(t, mfd)

	if err := cmdPurge(s, []string{"FOO.TXT"}, Qualifiers{"limit": "0"}); err == nil {
		t.Fatal("cmdPurge with /LIMIT=0: want error, got nil")
	}

	after := dirEntryNames(t, mfd)
	if !equalSets(before, after) {
		t.Errorf("directory entries changed despite the rejected /LIMIT=0 command: before %v, after %v", before, after)
	}
}

func TestCmdPurgeConfirmationMessage(t *testing.T) {
	s := newPurgeTestSession(t)

	if err := cmdPurge(s, []string{"BAZ.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdPurge: %v", err)
	}

	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "%PURGE-S-PURGED, BAZ.TXT purged (keeping 1 version(s))") {
		t.Errorf("output = %q, want a PURGE-S-PURGED confirmation for BAZ.TXT", out)
	}
}

func TestPurgeIntegrationViaExecute(t *testing.T) {
	s := newPurgeTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if _, err := s.Execute("purge FOO.TXT /limit=1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	names := dirEntryNames(t, mfd)
	if contains(names, "FOO.TXT;1") || contains(names, "FOO.TXT;2") {
		t.Errorf("directory entries = %v, want only FOO.TXT;3 to survive", names)
	}
	if !contains(names, "FOO.TXT;3") {
		t.Errorf("directory entries = %v, want FOO.TXT;3 to survive", names)
	}
}
