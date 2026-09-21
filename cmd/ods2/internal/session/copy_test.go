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
