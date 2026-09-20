package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdCopyToExplicitPath(t *testing.T) {
	s, _ := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "line one\nline two\n" {
		t.Errorf("copied content = %q, want %q", got, "line one\nline two\n")
	}
}

func TestCmdCopyToDirectory(t *testing.T) {
	s, _ := newTypeTestSession(t)
	dir := t.TempDir()

	if err := cmdCopy(s, []string{"STREAM.TXT", dir}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "STREAM.TXT") {
		t.Fatalf("directory contents = %v, want a single STREAM.TXT... file", entries)
	}
}

func TestCmdCopyWildcardDestination(t *testing.T) {
	s, _ := newTypeTestSession(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "*.OLD")

	if err := cmdCopy(s, []string{"STREAM.TXT", dest}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "STREAM.OLD")); err != nil {
		t.Errorf("expected STREAM.OLD to exist: %v", err)
	}
}

func TestCmdCopyQuietSuppressesMessage(t *testing.T) {
	s, out := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath}, Qualifiers{"quiet": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}
	if strings.Contains(out.String(), "COPY-S-COPIED") {
		t.Errorf("output = %q, want no confirmation message under /quiet", out.String())
	}
}

func TestCmdCopyTestDoesNotWrite(t *testing.T) {
	s, out := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath}, Qualifiers{"test": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("copy /test created %s, want no file written", outPath)
	}
	if !strings.Contains(out.String(), "COPY-I-TEST") {
		t.Errorf("output = %q, want a /test report", out.String())
	}
}

func TestCmdCopyBinaryMatchesSourceBytes(t *testing.T) {
	s, _ := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.bin")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath}, Qualifiers{"binary": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "line one\nline two\n" {
		t.Errorf("binary-copied content = %q, want %q", got, "line one\nline two\n")
	}
}

func TestCmdCopyAmbiguousDestination(t *testing.T) {
	s, _ := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	// *.TXT matches both STREAM.TXT and DUP.TXT: a literal, non-wildcard,
	// non-directory destination can't sensibly receive more than one.
	if err := cmdCopy(s, []string{"*.TXT", outPath}, Qualifiers{}); err == nil {
		t.Fatal("cmdCopy with an ambiguous destination: want error, got nil")
	}
}

func TestCmdCopyNotFound(t *testing.T) {
	s, _ := newTypeTestSession(t)
	if err := cmdCopy(s, []string{"NOSUCHFILE.TXT", filepath.Join(t.TempDir(), "out.txt")}, Qualifiers{}); err == nil {
		t.Fatal("cmdCopy on a nonexistent file: want error, got nil")
	}
}

func TestCopyIntegrationViaExecute(t *testing.T) {
	s, _ := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if _, err := s.Execute("copy STREAM.TXT " + outPath); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("expected %s to exist: %v", outPath, err)
	}
}
