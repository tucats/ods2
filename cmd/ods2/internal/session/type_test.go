package session

import (
	"bytes"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// newTypeTestSession builds on the same volume layout as
// newDirTestSession (see directory_test.go), adding a third file,
// STREAM.TXT;1, with real StreamLF-formatted content so typeFile has
// something real to read.
func newTypeTestSession(t *testing.T) (*Session, *bytes.Buffer) {
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

	streamFid := ondisk.Fid{Num: 25, Seq: 1}
	dupFid1 := ondisk.Fid{Num: 26, Seq: 1}
	dupFid2 := ondisk.Fid{Num: 27, Seq: 1}
	c.PutBlock(mfdDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("STREAM.TXT", []uint16{1}, []ondisk.Fid{streamFid}),
		odstest.BuildDirRecordBytes("DUP.TXT", []uint16{1, 2}, []ondisk.Fid{dupFid1, dupFid2}),
	))

	data := []byte("line one\nline two\n")

	const dataLBN = 210
	
	c.PutBlock(dirTestFileHeaderLBN(streamFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            streamFid,
		Format:         ondisk.RecordFormatStreamLF,
		HighestBlock:   1,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, dataLBN),
	}))

	block := make([]byte, ondisk.BlockSize)
	copy(block, data)
	c.PutBlock(dataLBN, block)

	c.PutBlock(dirTestFileHeaderLBN(dupFid1.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: dupFid1}))
	c.PutBlock(dirTestFileHeaderLBN(dupFid2.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: dupFid2}))

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	var out bytes.Buffer

	s := New()
	s.Stdout = &out
	s.Volumes[defaultDeviceName] = vol
	s.Default.Device = defaultDeviceName

	return s, &out
}

func TestCmdTypeStreamFile(t *testing.T) {
	s, out := newTypeTestSession(t)

	if err := cmdType(s, []string{"STREAM.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType: %v", err)
	}

	want := "line one\nline two\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestCmdTypeNotFound(t *testing.T) {
	s, _ := newTypeTestSession(t)
	if err := cmdType(s, []string{"NOSUCHFILE.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdType on a nonexistent file: want error, got nil")
	}
}

func TestCmdTypeAmbiguousWildcard(t *testing.T) {
	s, _ := newTypeTestSession(t)
	if err := cmdType(s, []string{"*.TXT"}, Qualifiers{}); err == nil {
		t.Fatal("cmdType with a wildcard matching several files: want error, got nil")
	}
}

func TestCmdTypeNoVersionSelectsHighest(t *testing.T) {
	// DUP.TXT has two versions (1 and 2). filespec.Glob's default version
	// selector (an unspecified Version) resolves to the single highest
	// version, not "every version" -- so this is normal, unambiguous
	// VMS behavior (TYPE a file with no version = the latest one), not
	// an error.
	s, _ := newTypeTestSession(t)
	if err := cmdType(s, []string{"DUP.TXT"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType on a multi-version name with no version given: %v", err)
	}
}

func TestCmdTypeExplicitVersion(t *testing.T) {
	s, _ := newTypeTestSession(t)
	if err := cmdType(s, []string{"DUP.TXT;1"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdType with an explicit version: %v", err)
	}
}

func TestTypeIntegrationViaExecute(t *testing.T) {
	s, out := newTypeTestSession(t)
	
	if _, err := s.Execute("type STREAM.TXT"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !bytes.Contains(out.Bytes(), []byte("line one")) {
		t.Errorf("output = %q, want it to contain the file's content", out.String())
	}
}
