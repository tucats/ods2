package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
	"github.com/tucats/ods2/volume"
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

// newSingleFileSession builds a minimal session with one mounted volume
// (keyed "DUA0") containing a single file, NAME.TXT;1, in the master
// file directory, with rec supplying its record-attribute fixture and
// data its raw on-disk content (padded to a whole block, as a real
// file's allocation always is). rec.Fid, rec.MapOffsetWords, and rec.
// MapBytes are filled in automatically.
func newSingleFileSession(t *testing.T, name string, rec odstest.FileHeaderFixture, data []byte) *Session {
	t.Helper()

	c := odstest.NewMemContainer(300)
	c.PutBlock(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:       1,
		Rvn:           1,
		IdxBitmapVBN:  dirTestIdxBitmapVBN,
		IdxBitmapLBN:  dirTestIdxBitmapLBN,
		IdxBitmapSize: dirTestIdxBitmapSize,
	}))
	c.PutBlock(dirTestFileHeaderLBN(ondisk.IndexFileFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(200, dirTestIdxBitmapLBN),
	}))

	mfdFid := ondisk.MasterFileDirectoryFid
	const mfdDataLBN = 200
	c.PutBlock(dirTestFileHeaderLBN(mfdFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            mfdFid,
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, mfdDataLBN),
	}))

	fileFid := ondisk.Fid{Num: 30, Seq: 1}
	c.PutBlock(mfdDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes(name, []uint16{1}, []ondisk.Fid{fileFid}),
	))

	const dataLBN = 220
	rec.Fid = fileFid
	rec.MapOffsetWords = 55
	rec.MapBytes = odstest.EncodeExtentFormat2(1, dataLBN)
	if rec.HighestBlock == 0 {
		rec.HighestBlock = 1
	}
	c.PutBlock(dirTestFileHeaderLBN(fileFid.Num), odstest.BuildFileHeaderBytes(t, rec))

	block := make([]byte, ondisk.BlockSize)
	copy(block, data)
	c.PutBlock(dataLBN, block)

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	s := New()
	s.Stdout = &bytes.Buffer{}
	s.Volumes["DUA0"] = vol
	s.Default.Device = "DUA0"
	return s
}

func TestCmdCopyPreservesTimeWithTimeQualifier(t *testing.T) {
	wantTime := vmstime.FromTime(time.Date(2000, time.January, 2, 3, 4, 5, 0, time.UTC))
	data := []byte("content\n")

	s := newSingleFileSession(t, "DATED.TXT", odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamLF,
		IdentOffset:    40,
		RevisionDate:   wantTime,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := cmdCopy(s, []string{"DATED.TXT", outPath}, Qualifiers{"time": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(wantTime.Time()) {
		t.Errorf("ModTime = %v, want %v", info.ModTime(), wantTime.Time())
	}
}

func TestCmdCopyWithoutTimeQualifierDoesNotPreserveTime(t *testing.T) {
	oldTime := vmstime.FromTime(time.Date(1990, time.January, 1, 0, 0, 0, 0, time.UTC))
	data := []byte("content\n")

	s := newSingleFileSession(t, "DATED.TXT", odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamLF,
		IdentOffset:    40,
		RevisionDate:   oldTime,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := cmdCopy(s, []string{"DATED.TXT", outPath}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.ModTime().Equal(oldTime.Time()) {
		t.Error("ModTime matches the source's 1990 date even without /time -- want the copy's own creation time")
	}
}

func TestCmdCopyIgnoreFallsBackToRawOnCorruptRecord(t *testing.T) {
	// A Variable-format record whose length prefix claims far more data
	// (0x63 = 99 bytes) than the file actually has left ("short" = 5
	// bytes) -- rms.Reader reports this as ErrCorruptRecord.
	raw := append([]byte{0x63, 0x00}, []byte("short")...)

	s := newSingleFileSession(t, "CORRUPT.TXT", odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatVariable,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(raw)),
	}, raw)

	outPath := filepath.Join(t.TempDir(), "out.bin")

	if err := cmdCopy(s, []string{"CORRUPT.TXT", outPath}, Qualifiers{}); err == nil {
		t.Fatal("cmdCopy on a corrupt record without /ignore: want error, got nil")
	}

	if err := cmdCopy(s, []string{"CORRUPT.TXT", outPath}, Qualifiers{"ignore": ""}); err != nil {
		t.Fatalf("cmdCopy with /ignore: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("raw fallback content = %q, want %q", got, raw)
	}
}

// newCopyDirsTestSession builds a session with one mounted volume
// containing SUBDIR.DIR (file #40) in the master file directory, itself
// containing NESTED.TXT;1 (file #41, StreamLF, "nested content\n") --
// enough of a directory tree to exercise /DIRS.
func newCopyDirsTestSession(t *testing.T) *Session {
	t.Helper()

	c := odstest.NewMemContainer(400)
	c.PutBlock(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:       1,
		Rvn:           1,
		IdxBitmapVBN:  dirTestIdxBitmapVBN,
		IdxBitmapLBN:  dirTestIdxBitmapLBN,
		IdxBitmapSize: dirTestIdxBitmapSize,
	}))
	c.PutBlock(dirTestFileHeaderLBN(ondisk.IndexFileFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(300, dirTestIdxBitmapLBN),
	}))

	mfdFid := ondisk.MasterFileDirectoryFid
	subdirFid := ondisk.Fid{Num: 40, Seq: 1}
	nestedFid := ondisk.Fid{Num: 41, Seq: 1}

	const mfdDataLBN = 200
	c.PutBlock(dirTestFileHeaderLBN(mfdFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            mfdFid,
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, mfdDataLBN),
	}))
	c.PutBlock(mfdDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("SUBDIR.DIR", []uint16{1}, []ondisk.Fid{subdirFid}),
	))

	const subdirDataLBN = 201
	c.PutBlock(dirTestFileHeaderLBN(subdirFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            subdirFid,
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, subdirDataLBN),
	}))
	c.PutBlock(subdirDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("NESTED.TXT", []uint16{1}, []ondisk.Fid{nestedFid}),
	))

	nestedData := []byte("nested content\n")
	const nestedDataLBN = 210
	c.PutBlock(dirTestFileHeaderLBN(nestedFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            nestedFid,
		Format:         ondisk.RecordFormatStreamLF,
		HighestBlock:   1,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(nestedData)),
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, nestedDataLBN),
	}))
	block := make([]byte, ondisk.BlockSize)
	copy(block, nestedData)
	c.PutBlock(nestedDataLBN, block)

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	s := New()
	s.Stdout = &bytes.Buffer{}
	s.Volumes["DUA0"] = vol
	s.Default.Device = "DUA0"
	return s
}

func TestCmdCopyDirsCreatesHostDirectoryAndPreservesHierarchy(t *testing.T) {
	s := newCopyDirsTestSession(t)
	dest := t.TempDir()

	if err := cmdCopy(s, []string{"[...]*.*", dest}, Qualifiers{"dirs": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	subdirPath := filepath.Join(dest, "SUBDIR")
	if info, err := os.Stat(subdirPath); err != nil || !info.IsDir() {
		t.Fatalf("expected %s to be a directory, err=%v", subdirPath, err)
	}

	got, err := os.ReadFile(filepath.Join(subdirPath, "NESTED.TXT;1"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "nested content\n" {
		t.Errorf("content = %q, want %q", got, "nested content\n")
	}
}

func TestCmdCopyWithoutDirsSkipsDirectoryEntries(t *testing.T) {
	s := newCopyDirsTestSession(t)
	dest := t.TempDir()

	// "*.*" at the top level matches only SUBDIR.DIR, which should be
	// silently skipped (not an error, and no output) without /dirs.
	if err := cmdCopy(s, []string{"*.*", dest}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dest contents = %v, want none", entries)
	}
}

func TestCmdCopyStreamQualifierPreservesRawBytes(t *testing.T) {
	// A StreamCR file: without /stream, text-mode copying re-serializes
	// records using the default LF line ending, changing '\r'
	// terminators to '\n'. With /stream, the exact original bytes
	// (including the '\r' terminators) pass through unchanged.
	data := []byte("one\rtwo\r")
	s := newSingleFileSession(t, "CRFILE.TXT", odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamCR,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := cmdCopy(s, []string{"CRFILE.TXT", outPath}, Qualifiers{"stream": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want exact source bytes %q", got, data)
	}
}

func TestCmdCopyWithoutStreamNormalizesLineEndings(t *testing.T) {
	data := []byte("one\rtwo\r")
	s := newSingleFileSession(t, "CRFILE.TXT", odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamCR,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := cmdCopy(s, []string{"CRFILE.TXT", outPath}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "one\ntwo\n"; string(got) != want {
		t.Errorf("content = %q, want normalized %q", got, want)
	}
}

func TestCmdCopyCRLFQualifier(t *testing.T) {
	data := []byte("one\ntwo\n")
	s := newSingleFileSession(t, "LFFILE.TXT", odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamLF,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := cmdCopy(s, []string{"LFFILE.TXT", outPath}, Qualifiers{"crlf": ""}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "one\r\ntwo\r\n"; string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestCmdCopyCRLFAndLFAreMutuallyExclusive(t *testing.T) {
	s, _ := newTypeTestSession(t)
	outPath := filepath.Join(t.TempDir(), "out.txt")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath}, Qualifiers{"crlf": "", "lf": ""}); err == nil {
		t.Fatal("cmdCopy with both /crlf and /lf: want error, got nil")
	}
}

func TestCmdCopyVFCQualifierIsAcceptedNoOp(t *testing.T) {
	s, _ := newTypeTestSession(t)
	outPath1 := filepath.Join(t.TempDir(), "out1.txt")
	outPath2 := filepath.Join(t.TempDir(), "out2.txt")

	if err := cmdCopy(s, []string{"STREAM.TXT", outPath1}, Qualifiers{}); err != nil {
		t.Fatalf("cmdCopy: %v", err)
	}
	if err := cmdCopy(s, []string{"STREAM.TXT", outPath2}, Qualifiers{"vfc": ""}); err != nil {
		t.Fatalf("cmdCopy with /vfc: %v", err)
	}

	got1, err := os.ReadFile(outPath1)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got2, err := os.ReadFile(outPath2)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got1) != string(got2) {
		t.Errorf("output with /vfc = %q, want the same as without it: %q", got2, got1)
	}
}
