package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// newVolumeDestTestSession builds on newTypeTestSession's existing source
// fixture (device "DUA0", mounted read-only: STREAM.TXT;1 "line
// one\nline two\n", DUP.TXT;1 and ;2 both empty) by mounting a second,
// freshly Initialize'd volume under device "DEST" -- a real, writable
// backing file (diskimage.Create/volume.Initialize both require one,
// unlike DUA0's in-memory odstest fixture), genuinely open for write, so
// COPY's host^Wvolume-to-volume direction (docs/PHASE-02.md subtask 16)
// can be exercised end to end.
func newVolumeDestTestSession(t *testing.T) *Session {
	t.Helper()

	s, _ := newTypeTestSession(t)

	path := filepath.Join(t.TempDir(), "dest.dsk")
	c, err := diskimage.Create(path, 600)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "DESTVOL"}); err != nil {
		t.Fatalf("volume.Initialize: %v", err)
	}

	destVol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("volume.Mount: %v", err)
	}
	s.Volumes["DEST"] = destVol

	return s
}

func TestCmdCopyToVolumeDestinationDefaultIsStreamText(t *testing.T) {
	s := newVolumeDestTestSession(t)

	if err := cmdCopy(s, []string{"STREAM.TXT", "DEST:OUT.TXT"}, Qualifiers{"quiet": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	var out bytes.Buffer
	s.Stdout = &out
	if err := cmdType(s, []string{"DEST:OUT.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType: %v", err)
	}
	if got, want := out.String(), "line one\nline two\n"; got != want {
		t.Errorf("content read back from DEST:OUT.TXT = %q, want %q", got, want)
	}

	// Confirm it actually landed as Stream_LF, not just that it reads
	// back correctly by coincidence.
	destVol := s.Volumes["DEST"]
	matches, err := filespec.Glob(destVol, filespec.Spec{Name: "OUT", Type: "TXT"})
	if err != nil || len(matches) != 1 {
		t.Fatalf("Glob(OUT.TXT) = %v, %v", matches, err)
	}
	f, err := destVol.OpenFID(matches[0].Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if got, want := f.Header.RecordAttributes.Format, ondisk.RecordFormatStreamLF; got != want {
		t.Errorf("created file's RecordFormat = %v, want %v", got, want)
	}
}

func TestCmdCopyToVolumeDestinationBinaryRoundTrips(t *testing.T) {
	s := newVolumeDestTestSession(t)

	if err := cmdCopy(s, []string{"STREAM.TXT", "DEST:OUT.BIN"}, Qualifiers{"quiet": "", "binary": ""}); err != nil {
		t.Fatalf("cmdCopy /binary to volume: %v", err)
	}

	// Round-trip back out to a host file via copy's original (volume ->
	// host) direction, and confirm the bytes are exactly what STREAM.TXT
	// itself contains -- proof /BINARY preserved the source's exact bytes
	// with no text-mode reframing, on a file this test never hand-built
	// directly.
	outPath := filepath.Join(t.TempDir(), "roundtrip.bin")
	if err := cmdCopy(s, []string{"DEST:OUT.BIN", outPath}, Qualifiers{"quiet": "", "binary": ""}); err != nil {
		t.Fatalf("cmdCopy back to host: %v", err)
	}

	got, err := readFile(t, outPath)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if want := "line one\nline two\n"; got != want {
		t.Errorf("round-tripped content = %q, want %q", got, want)
	}
}

func TestCmdCopyToVolumeDestinationWildcardKeepsSourceNames(t *testing.T) {
	s := newVolumeDestTestSession(t)

	if err := cmdCopy(s, []string{"*.TXT", "DEST:"}, Qualifiers{"quiet": ""}); err != nil {
		t.Fatalf("cmdCopy *.TXT to directory-only destination: %v", err)
	}

	destVol := s.Volumes["DEST"]
	matches, err := filespec.Glob(destVol, filespec.Spec{})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	names := make(map[string]bool, len(matches))
	for _, m := range matches {
		names[m.Name+"."+m.Type] = true
	}
	if !names["STREAM.TXT"] || !names["DUP.TXT"] {
		t.Errorf("destination entries = %v, want STREAM.TXT and DUP.TXT (each under its own source name)", names)
	}
}

func TestCmdCopyToVolumeDestinationAssignsNextVersion(t *testing.T) {
	s := newVolumeDestTestSession(t)

	if err := cmdCopy(s, []string{"STREAM.TXT", "DEST:OUT.TXT"}, Qualifiers{"quiet": ""}); err != nil {
		t.Fatalf("first cmdCopy: %v", err)
	}

	var out bytes.Buffer
	s.Stdout = &out
	if err := cmdCopy(s, []string{"STREAM.TXT", "DEST:OUT.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("second cmdCopy: %v", err)
	}
	if !strings.Contains(out.String(), "OUT.TXT;2") {
		t.Errorf("second copy's confirmation = %q, want it to mention version 2", out.String())
	}

	destVol := s.Volumes["DEST"]
	matches, err := filespec.Glob(destVol, filespec.Spec{Name: "OUT", Type: "TXT", Version: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("OUT.TXT versions = %v, want 2 (;1 and ;2)", matches)
	}
}

func TestCmdCopyToVolumeDestinationAmbiguousWithoutWildcard(t *testing.T) {
	s := newVolumeDestTestSession(t)

	// *.TXT matches both STREAM.TXT and DUP.TXT: a literal destination
	// name on a volume can't sensibly receive more than one, the same
	// rule cmdCopy already enforces for a literal host destination path.
	if err := cmdCopy(s, []string{"*.TXT", "DEST:SAME.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCopy with an ambiguous volume destination: want error, got nil")
	}
}

func TestCmdCopyToVolumeDestinationRejectsReadOnlyMount(t *testing.T) {
	s, _ := newTypeTestSession(t)

	path := filepath.Join(t.TempDir(), "readonly.dsk")
	c, err := diskimage.Create(path, 600)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	if err := volume.Initialize(c, volume.InitializeOptions{Label: "RODEST"}); err != nil {
		t.Fatalf("volume.Initialize: %v", err)
	}
	_ = c.Close()

	ro, err := diskimage.Open(path)
	if err != nil {
		t.Fatalf("diskimage.Open: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	roVol, err := volume.Mount(ro)
	if err != nil {
		t.Fatalf("volume.Mount: %v", err)
	}
	s.Volumes["RODEST"] = roVol

	if err := cmdCopy(s, []string{"STREAM.TXT", "RODEST:OUT.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdCopy to a volume mounted read-only: want error, got nil")
	}
}

func TestCmdCopyToVolumeDestinationTestQualifierDoesNotWrite(t *testing.T) {
	s := newVolumeDestTestSession(t)
	var out bytes.Buffer
	s.Stdout = &out

	if err := cmdCopy(s, []string{"STREAM.TXT", "DEST:OUT.TXT"}, Qualifiers{"test": ""}); err != nil {
		t.Fatalf("cmdCopy /test: %v", err)
	}
	if !strings.Contains(out.String(), "COPY-I-TEST") {
		t.Errorf("output = %q, want a /test report", out.String())
	}

	destVol := s.Volumes["DEST"]
	matches, err := filespec.Glob(destVol, filespec.Spec{Name: "OUT", Type: "TXT"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("OUT.TXT matches after /test = %v, want none written", matches)
	}
}

func TestCmdCopyToVolumeDestinationHostToVolumeDoesNotAffectHostDirection(t *testing.T) {
	// A plain, unmounted-device-prefixed destination (no ':' at all, or a
	// ':' that doesn't name a mounted device) must still behave exactly
	// as it did before this subtask -- an ordinary host path.
	s, _ := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy to host path: %v", err)
	}
	got, err := readFile(t, outPath)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if want := "line one\nline two\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// readFile is a small os.ReadFile wrapper returning a string, to keep the
// tests above's assertions terse.
func readFile(t *testing.T, path string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
