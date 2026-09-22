package main

// Integration tests: these build the real ods2 binary and run it as a
// subprocess against a small synthetic ODS-2 image, exercising the
// entire stack together (diskimage -> ondisk -> volume -> filespec ->
// rms -> session -> repl/cobra -> this binary) the way an actual user
// would invoke it, complementing the unit tests in each of those
// packages that verify one layer at a time in isolation.
//
// There's no real, VMS-created ODS-2 image checked into this repository
// to test against (see testdata/README.md for why) — buildTestImage
// synthesizes one instead, using the same byte-level fixture builders
// (internal/odstest) the rest of this project's tests already rely on.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

// binaryPath is the ods2 binary built once by TestMain and reused by
// every test in this file, rather than paying a fresh "go build" (or "go
// run") cost per test case.
var binaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ods2-integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating temp dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binaryPath = filepath.Join(dir, "ods2")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}

	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building ods2 for integration tests:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// Index bitmap layout, matching the pattern established across this
// project's other tests (see e.g. volume/file_test.go's newTestVolume).
const (
	testIdxBitmapLBN  = 5
	testIdxBitmapVBN  = 1
	testIdxBitmapSize = 3
)

func testFileHeaderLBN(fileNum uint16) uint32 {
	idxblk := uint32(fileNum) - 1 + testIdxBitmapVBN + testIdxBitmapSize
	return testIdxBitmapLBN + (idxblk - 1)
}

// buildTestImage synthesizes a small ODS-2 volume image:
//
//	[000000]
//	  README.TXT;1   ("Hello from ODS2!\nSecond line.\n")
//	  SUBDIR.DIR;1
//	    NESTED.TXT;1 ("Nested content.\n")
//
// and writes it to a temporary plain-image file, returning its path.
func buildTestImage(t *testing.T) string {
	t.Helper()

	c := odstest.NewMemContainer(400)
	c.PutBlock(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN: 1, Rvn: 1,
		IdxBitmapVBN: testIdxBitmapVBN, IdxBitmapLBN: testIdxBitmapLBN, IdxBitmapSize: testIdxBitmapSize,
	}))
	c.PutBlock(testFileHeaderLBN(ondisk.IndexFileFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(300, testIdxBitmapLBN),
	}))

	fid := func(n uint16) ondisk.Fid { return ondisk.Fid{Num: n, Seq: 1} }

	mfdFid := ondisk.MasterFileDirectoryFid
	const mfdDataLBN = 200
	c.PutBlock(testFileHeaderLBN(mfdFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid: mfdFid, FileChar: ondisk.FchDirectory, HighestBlock: 1,
		MapOffsetWords: 55, MapBytes: odstest.EncodeExtentFormat2(1, mfdDataLBN),
	}))
	c.PutBlock(mfdDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("README.TXT", []uint16{1}, []ondisk.Fid{fid(20)}),
		odstest.BuildDirRecordBytes("SUBDIR.DIR", []uint16{1}, []ondisk.Fid{fid(10)}),
	))

	readmeData := []byte("Hello from ODS2!\nSecond line.\n")
	const readmeLBN = 210
	c.PutBlock(testFileHeaderLBN(20), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid: fid(20), Format: ondisk.RecordFormatStreamLF,
		HighestBlock: 1, EndOfFileBlock: 1, FirstFreeByte: uint16(len(readmeData)),
		MapOffsetWords: 55, MapBytes: odstest.EncodeExtentFormat2(1, readmeLBN),
	}))
	readmeBlock := make([]byte, ondisk.BlockSize)
	copy(readmeBlock, readmeData)
	c.PutBlock(readmeLBN, readmeBlock)

	nestedData := []byte("Nested content.\n")
	const nestedLBN = 211
	const subdirDataLBN = 201
	c.PutBlock(testFileHeaderLBN(10), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid: fid(10), FileChar: ondisk.FchDirectory, HighestBlock: 1,
		MapOffsetWords: 55, MapBytes: odstest.EncodeExtentFormat2(1, subdirDataLBN),
	}))
	c.PutBlock(subdirDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("NESTED.TXT", []uint16{1}, []ondisk.Fid{fid(11)}),
	))
	c.PutBlock(testFileHeaderLBN(11), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid: fid(11), Format: ondisk.RecordFormatStreamLF,
		HighestBlock: 1, EndOfFileBlock: 1, FirstFreeByte: uint16(len(nestedData)),
		MapOffsetWords: 55, MapBytes: odstest.EncodeExtentFormat2(1, nestedLBN),
	}))
	nestedBlock := make([]byte, ondisk.BlockSize)
	copy(nestedBlock, nestedData)
	c.PutBlock(nestedLBN, nestedBlock)

	buf := make([]byte, 0, int(c.Blocks())*ondisk.BlockSize)
	block := make([]byte, ondisk.BlockSize)
	for i := uint32(0); i < c.Blocks(); i++ {
		if err := c.ReadBlock(i, block); err != nil {
			t.Fatalf("ReadBlock(%d): %v", i, err)
		}
		buf = append(buf, block...)
	}

	path := filepath.Join(t.TempDir(), "test.img")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// run executes the built ods2 binary with args and returns its combined
// stdout+stderr.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func TestIntegrationOneShotDirectory(t *testing.T) {
	image := buildTestImage(t)

	out, err := run(t, "", "dir", image)
	if err != nil {
		t.Fatalf("ods2 dir: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "README.TXT;1") || !strings.Contains(out, "SUBDIR.DIR;1") {
		t.Errorf("output = %q, want it to list both top-level entries", out)
	}
}

func TestIntegrationOneShotDirectorySubdirectory(t *testing.T) {
	image := buildTestImage(t)

	out, err := run(t, "", "dir", image, "[SUBDIR]")
	if err != nil {
		t.Fatalf("ods2 dir: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "NESTED.TXT;1") {
		t.Errorf("output = %q, want it to list NESTED.TXT", out)
	}
}

func TestIntegrationOneShotType(t *testing.T) {
	image := buildTestImage(t)

	out, err := run(t, "", "type", image, "README.TXT")
	if err != nil {
		t.Fatalf("ods2 type: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "Hello from ODS2!") || !strings.Contains(out, "Second line.") {
		t.Errorf("output = %q, want the file's content", out)
	}
}

func TestIntegrationOneShotCopyToAbsolutePath(t *testing.T) {
	image := buildTestImage(t)
	outPath := filepath.Join(t.TempDir(), "copied.txt")

	// This specifically exercises the tokenizer fix for absolute Unix
	// paths starting with '/' being mistaken for a qualifier -- see
	// cmd/ods2/internal/session/tokenize.go.
	out, err := run(t, "", "copy", image, "README.TXT", outPath)
	if err != nil {
		t.Fatalf("ods2 copy: %v\noutput:\n%s", err, out)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "Hello from ODS2!\nSecond line.\n" {
		t.Errorf("copied content = %q, want the source file's content", got)
	}
}

func TestIntegrationOneShotSearch(t *testing.T) {
	image := buildTestImage(t)

	out, err := run(t, "", "search", image, "README.TXT", "Second")
	if err != nil {
		t.Fatalf("ods2 search: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "Second line.") {
		t.Errorf("output = %q, want the matching line", out)
	}
}

func TestIntegrationOneShotNonexistentImage(t *testing.T) {
	out, err := run(t, "", "dir", filepath.Join(t.TempDir(), "nope.img"))
	if err == nil {
		t.Fatalf("ods2 dir on a nonexistent image: want a non-zero exit, output:\n%s", out)
	}
}

func TestIntegrationInteractiveREPL(t *testing.T) {
	image := buildTestImage(t)
	script := fmt.Sprintf("mount DUA0 %s\ndir *.*\ntype README.TXT\nexit\n", image)

	out, err := run(t, script)
	if err != nil {
		t.Fatalf("ods2 (REPL): %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "README.TXT;1") {
		t.Errorf("output = %q, want the directory listing", out)
	}
	if !strings.Contains(out, "Hello from ODS2!") {
		t.Errorf("output = %q, want the typed file's content", out)
	}
}

func TestIntegrationInteractiveREPLReportsBadCommand(t *testing.T) {
	out, err := run(t, "frobnicate\nexit\n")
	if err != nil {
		t.Fatalf("ods2 (REPL): %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "unrecognized command") {
		t.Errorf("output = %q, want an unrecognized-command message", out)
	}
}
