package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
)

// writeHostFile is a small os.WriteFile wrapper used by this file's tests
// to build a source file on the host filesystem for /HOST to read.
func writeHostFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", path, err)
	}

	return path
}

func TestCmdCopyFromHostDefaultIsStreamText(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "source.txt", "line one\nline two\n")

	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.TXT"}, Qualifiers{"quiet": "", "host": ""}); err != nil {
		t.Fatalf("cmdCopy /host: %v", err)
	}

	var out bytes.Buffer

	s.Stdout = &out
	if err := cmdType(s, []string{"DEST:OUT.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType: %v", err)
	}

	if got, want := out.String(), testContentTwoLinesOfText; got != want {
		t.Errorf("content read back from DEST:OUT.TXT = %q, want %q", got, want)
	}

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

func TestCmdCopyFromHostBinaryRoundTrips(t *testing.T) {
	s := newVolumeDestTestSession(t)

	hostPath := writeHostFile(t, t.TempDir(), "source.bin", testContentTwoLinesOfText)

	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.BIN"}, Qualifiers{"quiet": "", "host": "", "binary": ""}); err != nil {
		t.Fatalf("cmdCopy /host /binary: %v", err)
	}

	// TYPE can't be used to check this: it reads an Undefined-format file
	// (what /BINARY creates) as fixed-size records, which breaks on a
	// length that isn't a whole number of blocks -- a pre-existing
	// limitation of the volume -> host /BINARY direction, unrelated to
	// /HOST. Round-tripping back out via that same, already-tested
	// direction (as TestCmdCopyToVolumeDestinationBinaryRoundTrips does)
	// sidesteps it and still proves /HOST wrote the exact source bytes.
	outPath := filepath.Join(t.TempDir(), "roundtrip.bin")
	if err := cmdCopy(s, []string{"DEST:OUT.BIN", outPath}, Qualifiers{"quiet": "", "binary": ""}); err != nil {
		t.Fatalf("cmdCopy back to host: %v", err)
	}

	got, err := readFile(t, outPath)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}

	if got != testContentTwoLinesOfText {
		t.Errorf("round-tripped content = %q, want %q", got, testContentTwoLinesOfText)
	}
}

func TestCmdCopyFromHostWildcardDestinationUppercasesHostBaseName(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "go.sum", "hello\n")

	if err := cmdCopy(s, []string{hostPath, "DEST:*.*"}, Qualifiers{"quiet": "", "host": ""}); err != nil {
		t.Fatalf("cmdCopy /host to wildcard destination: %v", err)
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
	// The host base name is lower-case ("go.sum"), but the destination
	// should get VMS's own upper-cased convention, not a literal copy of
	// the host's casing.
	if !names["GO.SUM"] {
		t.Errorf("destination entries = %v, want the host file's base name upper-cased to \"GO.SUM\"", names)
	}

	if names["go.sum"] {
		t.Errorf("destination entries = %v, want no lower-case \"go.sum\" entry", names)
	}
}

func TestCmdCopyFromHostAssignsNextVersion(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "source.txt", "content\n")

	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.TXT"}, Qualifiers{"quiet": "", "host": ""}); err != nil {
		t.Fatalf("first cmdCopy: %v", err)
	}

	var out bytes.Buffer

	s.Stdout = &out
	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.TXT"}, Qualifiers{"host": ""}); err != nil {
		t.Fatalf("second cmdCopy: %v", err)
	}

	if !strings.Contains(out.String(), "OUT.TXT;2") {
		t.Errorf("second copy's confirmation = %q, want it to mention version 2", out.String())
	}
}

func TestCmdCopyFromHostRejectsNonVolumeDestination(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "source.txt", "content\n")
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if err := cmdCopy(s, []string{hostPath, outPath}, Qualifiers{"host": ""}); err == nil {
		t.Fatal("cmdCopy /host with a host destination: want error, got nil")
	}
}

func TestCmdCopyFromHostMissingSourceFile(t *testing.T) {
	s := newVolumeDestTestSession(t)

	if err := cmdCopy(s, []string{filepath.Join(t.TempDir(), "nosuchfile.txt"), "DEST:OUT.TXT"}, Qualifiers{"host": ""}); err == nil {
		t.Fatal("cmdCopy /host with a missing host source: want error, got nil")
	}
}

func TestCmdCopyFromHostRejectsDirectorySource(t *testing.T) {
	s := newVolumeDestTestSession(t)

	if err := cmdCopy(s, []string{t.TempDir(), "DEST:OUT.TXT"}, Qualifiers{"host": ""}); err == nil {
		t.Fatal("cmdCopy /host with a directory source: want error, got nil")
	}
}

func TestCmdCopyFromHostTestQualifierDoesNotWrite(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "source.txt", "content\n")

	var out bytes.Buffer

	s.Stdout = &out

	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.TXT"}, Qualifiers{"host": "", "test": ""}); err != nil {
		t.Fatalf("cmdCopy /host /test: %v", err)
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

func TestCmdCopyFromHostNormalizesCRLFToLF(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "source.txt", "line one\r\nline two\r\n")

	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.TXT"}, Qualifiers{"quiet": "", "host": ""}); err != nil {
		t.Fatalf("cmdCopy /host: %v", err)
	}

	var out bytes.Buffer

	s.Stdout = &out
	if err := cmdType(s, []string{"DEST:OUT.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType: %v", err)
	}

	if got, want := out.String(), "line one\nline two\n"; got != want {
		t.Errorf("content read back = %q, want %q (CRLF normalized to LF)", got, want)
	}
}

func TestCmdCopyFromHostFileWithNoTrailingNewline(t *testing.T) {
	s := newVolumeDestTestSession(t)
	hostPath := writeHostFile(t, t.TempDir(), "source.txt", "line one\nline two, no newline")

	if err := cmdCopy(s, []string{hostPath, "DEST:OUT.TXT"}, Qualifiers{"quiet": "", "host": ""}); err != nil {
		t.Fatalf("cmdCopy /host: %v", err)
	}

	var out bytes.Buffer

	s.Stdout = &out
	if err := cmdType(s, []string{"DEST:OUT.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType: %v", err)
	}

	if got, want := out.String(), "line one\nline two, no newline\n"; got != want {
		t.Errorf("content read back = %q, want %q", got, want)
	}
}
