package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLocalFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "local.txt")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	return path
}

func TestCmdDifferenceIdentical(t *testing.T) {
	s, out := newTypeTestSession(t)
	local := writeLocalFile(t, "line one\nline two\n")

	if err := cmdDifference(s, []string{"STREAM.TXT", local}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDifference: %v", err)
	}

	if !strings.Contains(out.String(), "identical") {
		t.Errorf("output = %q, want it to report the files as identical", out.String())
	}
}

func TestCmdDifferenceDiffers(t *testing.T) {
	s, out := newTypeTestSession(t)
	local := writeLocalFile(t, "line one\nDIFFERENT\n")

	if err := cmdDifference(s, []string{"STREAM.TXT", local}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDifference: %v", err)
	}

	if !strings.Contains(out.String(), "line 2 differs") {
		t.Errorf("output = %q, want it to report line 2 as differing", out.String())
	}
}

func TestCmdDifferenceLengthMismatch(t *testing.T) {
	s, out := newTypeTestSession(t)
	local := writeLocalFile(t, "line one\nline two\nline three\n")

	if err := cmdDifference(s, []string{"STREAM.TXT", local}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDifference: %v", err)
	}

	if !strings.Contains(out.String(), "line 3 differs") {
		t.Errorf("output = %q, want it to report the extra local line as a difference", out.String())
	}
}

func TestCmdDifferenceLocalFileMissing(t *testing.T) {
	s, _ := newTypeTestSession(t)
	if err := cmdDifference(s, []string{"STREAM.TXT", filepath.Join(t.TempDir(), "nope.txt")}, Qualifiers{}); err == nil {
		t.Fatal("cmdDifference with a missing local file: want error, got nil")
	}
}

func TestCmdDifferenceAmbiguousSpec(t *testing.T) {
	s, _ := newTypeTestSession(t)
	local := writeLocalFile(t, "anything")
	
	if err := cmdDifference(s, []string{"*.TXT", local}, Qualifiers{}); err == nil {
		t.Fatal("cmdDifference with a wildcard matching multiple files: want error, got nil")
	}
}
