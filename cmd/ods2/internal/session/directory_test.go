package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// Layout mirrors filespec/glob_test.go's own test volume: an index bitmap
// at LBN 5 (3 blocks), giving file N's header at LBN N+7, with two files
// (README.TXT, DATA.DAT) directly in the master file directory.
const (
	dirTestIdxBitmapLBN  = 5
	dirTestIdxBitmapVBN  = 1
	dirTestIdxBitmapSize = 3
)

func dirTestFileHeaderLBN(fileNum uint16) uint32 {
	idxblk := uint32(fileNum) - 1 + dirTestIdxBitmapVBN + dirTestIdxBitmapSize
	return dirTestIdxBitmapLBN + (idxblk - 1)
}

// newDirTestSession builds a session with one mounted volume (keyed
// "DUA0") containing README.TXT;1 and DATA.DAT;1 in the master file
// directory, and a default directory already pointed at it.
func newDirTestSession(t *testing.T) (*Session, *bytes.Buffer) {
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

	readmeFid := ondisk.Fid{Num: 20, Seq: 1}
	dataFid := ondisk.Fid{Num: 21, Seq: 1}
	c.PutBlock(mfdDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("README.TXT", []uint16{1}, []ondisk.Fid{readmeFid}),
		odstest.BuildDirRecordBytes("DATA.DAT", []uint16{1}, []ondisk.Fid{dataFid}),
	))
	c.PutBlock(dirTestFileHeaderLBN(readmeFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:          readmeFid,
		HighestBlock: 3,
	}))
	c.PutBlock(dirTestFileHeaderLBN(dataFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:          dataFid,
		HighestBlock: 1,
	}))

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	s := New()
	var out bytes.Buffer
	s.Stdout = &out
	s.Volumes["DUA0"] = vol
	s.Default.Device = "DUA0"

	return s, &out
}

func TestCmdDirectoryBasic(t *testing.T) {
	s, out := newDirTestSession(t)

	if err := cmdDirectory(s, nil, Qualifiers{}); err != nil {
		t.Fatalf("cmdDirectory: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "README.TXT;1") {
		t.Errorf("output = %q, want it to contain %q", got, "README.TXT;1")
	}
	if !strings.Contains(got, "DATA.DAT;1") {
		t.Errorf("output = %q, want it to contain %q", got, "DATA.DAT;1")
	}
	if !strings.Contains(got, "Total of 2 file(s)") {
		t.Errorf("output = %q, want a total-files summary", got)
	}
}

func TestCmdDirectorySpecificFile(t *testing.T) {
	s, out := newDirTestSession(t)

	if err := cmdDirectory(s, []string{"README.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDirectory: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "README.TXT;1") {
		t.Errorf("output = %q, want it to contain %q", got, "README.TXT;1")
	}
	if strings.Contains(got, "DATA.DAT") {
		t.Errorf("output = %q, want it to NOT contain DATA.DAT", got)
	}
	if !strings.Contains(got, "Total of 1 file(s)") {
		t.Errorf("output = %q, want a total of 1 file", got)
	}
}

func TestCmdDirectorySizeQualifier(t *testing.T) {
	s, out := newDirTestSession(t)

	if err := cmdDirectory(s, []string{"README.TXT"}, Qualifiers{"size": ""}); err != nil {
		t.Fatalf("cmdDirectory: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, fmt.Sprintf("%5d", 3)) {
		t.Errorf("output = %q, want it to show the file's block count (3, right-justified in a 5-wide field)", got)
	}
	if !strings.Contains(got, "3 block(s)") {
		t.Errorf("output = %q, want a total-blocks summary", got)
	}
}

func TestCmdDirectoryFileQualifier(t *testing.T) {
	s, out := newDirTestSession(t)

	if err := cmdDirectory(s, []string{"README.TXT"}, Qualifiers{"file": ""}); err != nil {
		t.Fatalf("cmdDirectory: %v", err)
	}

	// The Fid string form is "(number,seq,rvn)" -- confirm something in
	// that shape shows up.
	if !strings.Contains(out.String(), "(20,1,") {
		t.Errorf("output = %q, want it to contain the file's Fid", out.String())
	}
}

func TestCmdDirectoryFullImpliesOthers(t *testing.T) {
	s, out := newDirTestSession(t)

	if err := cmdDirectory(s, []string{"README.TXT"}, Qualifiers{"full": ""}); err != nil {
		t.Fatalf("cmdDirectory: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, fmt.Sprintf("%5d", 3)) { // size
		t.Errorf("output = %q, want /full to imply /size", got)
	}
	if !strings.Contains(got, "(20,1,") { // file id
		t.Errorf("output = %q, want /full to imply /file", got)
	}
}

func TestCmdDirectoryNoMatches(t *testing.T) {
	s, out := newDirTestSession(t)

	if err := cmdDirectory(s, []string{"NOSUCHFILE.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdDirectory: %v", err)
	}
	if !strings.Contains(out.String(), "Total of 0 file(s)") {
		t.Errorf("output = %q, want a total of 0 files", out.String())
	}
}

func TestCmdDirectoryNoDeviceMounted(t *testing.T) {
	s := New()
	s.Stdout = &bytes.Buffer{}

	if err := cmdDirectory(s, []string{"FOO.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdDirectory with no volume mounted: want error, got nil")
	}
}

func TestDirectoryIntegrationViaExecute(t *testing.T) {
	s, out := newDirTestSession(t)

	if _, err := s.Execute("dir README.TXT /size"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "README.TXT;1") {
		t.Errorf("output = %q, want it to contain the file", out.String())
	}
}
