package session

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// newDeleteTestSession builds a session with one mounted, writable volume
// (device "DUA0") on a real backing file — DeleteFile needs a genuine
// diskimage.WritableContainer, unlike newTypeTestSession's in-memory,
// read-only odstest fixture, so this follows copytovolume_test.go's own
// newVolumeDestTestSession pattern (diskimage.Create + volume.Initialize)
// instead. The master file directory starts with FOO.TXT;1, FOO.TXT;2,
// BAR.TXT;1, BAR.TXT;2, and BAZ.TXT;1 — enough distinct names and
// versions to exercise ";n", ";*", and a wildcarded name all deleting
// more than one file in a single command.
func newDeleteTestSession(t *testing.T) *Session {
	t.Helper()

	path := filepath.Join(t.TempDir(), "delete.dsk")
	c, err := diskimage.Create(path, 600)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "DELVOL"}); err != nil {
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

	for _, name := range []string{"FOO.TXT", "FOO.TXT", "BAR.TXT", "BAR.TXT", "BAZ.TXT"} {
		createDeleteTestFile(t, vol, mfd, name, bm, ib)
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

// createDeleteTestFile creates one small Stream_LF file named name in dir,
// auto-assigned the next version the same way CreateFile always does —
// the content itself is never checked by delete_test.go's own assertions,
// only that the file exists and that its storage is reclaimed correctly.
func createDeleteTestFile(t *testing.T, vol *volume.Volume, dir *volume.Directory, name string, bm *volume.Bitmap, ib *volume.IndexBitmap) {
	t.Helper()

	f, err := vol.CreateFile(dir, name, ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile(%s): %v", name, err)
	}

	content := "content of " + name
	block := make([]byte, ondisk.BlockSize)
	copy(block, content)
	if err := f.WriteBlock(1, block); err != nil {
		t.Fatalf("WriteBlock(%s): %v", name, err)
	}
	if err := f.CloseWithFinalByte(uint16(len(content))); err != nil {
		t.Fatalf("Close(%s): %v", name, err)
	}
}

// dirEntryNames returns dir's current entries as "NAME.TYPE;version"
// strings, sorted implicitly by List's own order, for easy comparison in
// assertions below.
func dirEntryNames(t *testing.T, dir *volume.Directory) []string {
	t.Helper()

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = fmt.Sprintf("%s;%d", e.Name, e.Version)
	}
	return names
}

func TestCmdDeleteSpecificVersion(t *testing.T) {
	s := newDeleteTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdDelete(s, []string{"FOO.TXT;1"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDelete: %v", err)
	}

	names := dirEntryNames(t, mfd)
	for _, want := range []string{"FOO.TXT;2", "BAR.TXT;1", "BAR.TXT;2", "BAZ.TXT;1"} {
		if !contains(names, want) {
			t.Errorf("directory entries = %v, want %s still present", names, want)
		}
	}
	if contains(names, "FOO.TXT;1") {
		t.Errorf("directory entries = %v, want FOO.TXT;1 removed", names)
	}

	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "%DELETE-S-DELETED, FOO.TXT;1 deleted") {
		t.Errorf("output = %q, want a DELETE-S-DELETED confirmation for FOO.TXT;1", out)
	}
}

func TestCmdDeleteAllVersionsOfAName(t *testing.T) {
	s := newDeleteTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdDelete(s, []string{"FOO.TXT;*"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDelete: %v", err)
	}

	names := dirEntryNames(t, mfd)
	if contains(names, "FOO.TXT;1") || contains(names, "FOO.TXT;2") {
		t.Errorf("directory entries = %v, want every FOO.TXT version removed", names)
	}
	for _, want := range []string{"BAR.TXT;1", "BAR.TXT;2", "BAZ.TXT;1"} {
		if !contains(names, want) {
			t.Errorf("directory entries = %v, want %s untouched", names, want)
		}
	}
}

func TestCmdDeleteWildcardNameAcrossMultipleFiles(t *testing.T) {
	s := newDeleteTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if err := cmdDelete(s, []string{"*.TXT;2"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDelete: %v", err)
	}

	names := dirEntryNames(t, mfd)
	if contains(names, "FOO.TXT;2") || contains(names, "BAR.TXT;2") {
		t.Errorf("directory entries = %v, want every ;2 version removed", names)
	}
	for _, want := range []string{"FOO.TXT;1", "BAR.TXT;1", "BAZ.TXT;1"} {
		if !contains(names, want) {
			t.Errorf("directory entries = %v, want %s untouched", names, want)
		}
	}
}

func TestCmdDeleteWithoutVersionIsRejected(t *testing.T) {
	s := newDeleteTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	before := dirEntryNames(t, mfd)

	if err := cmdDelete(s, []string{"FOO.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdDelete with no version: want error, got nil")
	}

	after := dirEntryNames(t, mfd)
	if !equalSets(before, after) {
		t.Errorf("directory entries changed despite the rejected command: before %v, after %v", before, after)
	}
}

func TestCmdDeleteWithTrailingSemicolonIsRejected(t *testing.T) {
	s := newDeleteTestSession(t)

	if err := cmdDelete(s, []string{"FOO.TXT;"}, Qualifiers{}); err == nil {
		t.Fatal("cmdDelete with an empty version (FOO.TXT;): want error, got nil")
	}
}

func TestCmdDeleteNonexistentFileErrors(t *testing.T) {
	s := newDeleteTestSession(t)

	if err := cmdDelete(s, []string{"NOSUCHFILE.TXT;1"}, Qualifiers{}); err == nil {
		t.Fatal("cmdDelete of a nonexistent file: want error, got nil")
	}
}

func TestCmdDeleteNonexistentVersionErrors(t *testing.T) {
	s := newDeleteTestSession(t)

	if err := cmdDelete(s, []string{"FOO.TXT;9"}, Qualifiers{}); err == nil {
		t.Fatal("cmdDelete of a nonexistent version: want error, got nil")
	}
}

func TestCmdDeleteReclaimsStorageForReuse(t *testing.T) {
	s := newDeleteTestSession(t)
	vol := s.Volumes["DUA0"]

	if err := cmdDelete(s, []string{"FOO.TXT;1"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDelete: %v", err)
	}

	// cmdDelete flushes both bitmap caches itself (see its own doc
	// comment) -- opening brand-new Bitmap/IndexBitmap instances (rather
	// than reusing the Device's already-cached ones) confirms the freed
	// state genuinely reached disk, not just this process's in-memory
	// cache.
	dev := vol.Devices[0]
	ib, err := volume.OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	if _, err := ib.FindFreeSlot(); err != nil {
		t.Errorf("FindFreeSlot after delete: %v (former header slot not reclaimed)", err)
	}

	// The reclaimed slot must also be usable end-to-end via the ordinary
	// CreateFile path, not just satisfy FindFreeSlot's own check.
	bm, err := volume.OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}
	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	createDeleteTestFile(t, vol, mfd, "NEW.TXT", bm, ib)

	names := dirEntryNames(t, mfd)
	if !contains(names, "NEW.TXT;1") {
		t.Errorf("directory entries = %v, want NEW.TXT;1 to have been created successfully", names)
	}
}

func TestDeleteIntegrationViaExecute(t *testing.T) {
	s := newDeleteTestSession(t)
	mfd, err := s.Volumes["DUA0"].OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	if _, err := s.Execute("delete FOO.TXT;1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	names := dirEntryNames(t, mfd)
	if contains(names, "FOO.TXT;1") {
		t.Errorf("directory entries = %v, want FOO.TXT;1 removed", names)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, item := range a {
		if !contains(b, item) {
			return false
		}
	}
	return true
}
