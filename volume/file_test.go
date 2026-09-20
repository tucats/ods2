package volume

import (
	"bytes"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

// This file's tests share a small, consistent layout for a synthetic
// volume:
//
//	LBN 1                    home block
//	LBN testIndexBitmapLBN   (=5) start of the (empty, for these tests) index bitmap
//	LBN 8                    INDEXF.SYS's own header (IndexBitmapLBN + IndexBitmapSize)
//	LBN 5..                  the file header area, one 512-byte slot per file
//	                         number, addressed as file N's header living at
//	                         LBN 5 + (N - 1) -- see the comment on
//	                         readFileHeaderViaIndex for why this arithmetic
//	                         lines up with the bootstrap LBN above when N=1.
//	LBN 100+                 a data area used by individual test files
const (
	testIndexBitmapLBN  = 5
	testIndexBitmapVBN  = 1
	testIndexBitmapSize = 3
)

// fileHeaderLBN returns the absolute LBN of file number fileNum's header,
// given this test file's layout: the header area starts right after the
// index bitmap, and INDEXF.SYS's map (installed by newTestVolume) is one
// big extent starting at testIndexBitmapLBN, so virtual block N of
// INDEXF.SYS maps directly to LBN testIndexBitmapLBN + (N - 1).
func fileHeaderLBN(fileNum uint16) uint32 {
	idxblk := uint32(fileNum) - 1 + testIndexBitmapVBN + testIndexBitmapSize
	return testIndexBitmapLBN + (idxblk - 1)
}

// newTestVolume builds a mountable in-memory container whose INDEXF.SYS
// describes one large extent covering the whole header area (and beyond,
// into the data area used by individual tests), then mounts it.
func newTestVolume(t *testing.T) (*Volume, *odstest.MemContainer) {
	t.Helper()

	c := odstest.NewMemContainer(300)
	c.PutBlock(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:       1,
		Rvn:           1,
		IdxBitmapVBN:  testIndexBitmapVBN,
		IdxBitmapLBN:  testIndexBitmapLBN,
		IdxBitmapSize: testIndexBitmapSize,
	}))

	c.PutBlock(testIndexBitmapLBN+testIndexBitmapSize, odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(250, testIndexBitmapLBN),
	}))

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return vol, c
}

func TestOpenFIDReadsFileData(t *testing.T) {
	vol, c := newTestVolume(t)

	fid := ondisk.Fid{Num: 10, Seq: 1}
	c.PutBlock(fileHeaderLBN(fid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            fid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(5, 100), // file's data: VBN 1-5 -> LBN 100-104
	}))

	wantData := bytes.Repeat([]byte{0xAB}, ondisk.BlockSize)
	c.PutBlock(100, wantData) // VBN 1 of the file

	f, err := vol.OpenFID(fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if !f.Header.Fid.Equal(fid) {
		t.Errorf("Header.Fid = %v, want %v", f.Header.Fid, fid)
	}

	got := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(1, got); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}
	if !bytes.Equal(got, wantData) {
		t.Error("ReadBlock(1) did not return the expected data")
	}
}

func TestOpenFIDRejectsStaleFid(t *testing.T) {
	vol, c := newTestVolume(t)

	// The header slot for file 10 actually holds Seq=2 (as if file 10;1
	// was deleted and its slot reused by a newer file), but the caller
	// asks for Seq=1 -- a stale Fid it obtained before the deletion.
	c.PutBlock(fileHeaderLBN(10), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid: ondisk.Fid{Num: 10, Seq: 2},
	}))

	if _, err := vol.OpenFID(ondisk.Fid{Num: 10, Seq: 1}); err == nil {
		t.Fatal("OpenFID with a stale Fid: want error, got nil")
	}
}

func TestFileReadBlockZeroFillsBeyondHighWaterMark(t *testing.T) {
	vol, c := newTestVolume(t)

	fid := ondisk.Fid{Num: 11, Seq: 1}
	c.PutBlock(fileHeaderLBN(fid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            fid,
		IdentOffset:    40, // > 39: enables the high-water-mark check
		HighWaterMark:  3,  // VBNs 1-2 are real data; VBN 3 onward has never been written
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(5, 200),
	}))

	// Leftover, previously-deleted-file garbage sitting at VBN 3's
	// physical location (LBN 202) -- ReadBlock must never expose this.
	c.PutBlock(202, bytes.Repeat([]byte{0xFF}, ondisk.BlockSize))

	f, err := vol.OpenFID(fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}

	got := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(3, got); err != nil {
		t.Fatalf("ReadBlock(3): %v", err)
	}
	if !bytes.Equal(got, make([]byte, ondisk.BlockSize)) {
		t.Error("ReadBlock at/beyond the high-water mark returned non-zero data")
	}
}

func TestFileReadBlockIgnoresHighWaterMarkWhenAbsent(t *testing.T) {
	// A header with IdentOffset <= 39 predates the high-water-mark field
	// existing at all; File.ReadBlock must not treat HighWaterMark's
	// zero value as "everything is unwritten" in that case, or every
	// read against such a header would incorrectly return zeros.
	vol, c := newTestVolume(t)

	fid := ondisk.Fid{Num: 12, Seq: 1}
	c.PutBlock(fileHeaderLBN(fid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            fid,
		IdentOffset:    30, // <= 39: no high-water-mark field present
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(2, 210),
	}))

	wantData := bytes.Repeat([]byte{0x42}, ondisk.BlockSize)
	c.PutBlock(210, wantData)

	f, err := vol.OpenFID(fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}

	got := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(1, got); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}
	if !bytes.Equal(got, wantData) {
		t.Error("ReadBlock incorrectly zero-filled a block when no high-water mark is present")
	}
}

func TestOpenFIDFollowsExtensionHeaderChain(t *testing.T) {
	vol, c := newTestVolume(t)

	primaryFid := ondisk.Fid{Num: 20, Seq: 1}
	extensionFid := ondisk.Fid{Num: 21, Seq: 1}

	// The primary header describes no data of its own -- everything is
	// in the extension segment.
	c.PutBlock(fileHeaderLBN(primaryFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:          primaryFid,
		ExtensionFid: extensionFid,
	}))
	c.PutBlock(fileHeaderLBN(extensionFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            extensionFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(2, 220),
	}))

	wantData := bytes.Repeat([]byte{0x99}, ondisk.BlockSize)
	c.PutBlock(220, wantData)

	f, err := vol.OpenFID(primaryFid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if len(f.Extents) != 1 || f.Extents[0].StartLBN != 220 {
		t.Fatalf("Extents = %+v, want a single extent starting at LBN 220 (from the extension segment)", f.Extents)
	}

	got := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(1, got); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}
	if !bytes.Equal(got, wantData) {
		t.Error("ReadBlock(1) did not return the extension segment's data")
	}
}

func TestReadExtentsRejectsZeroVBN(t *testing.T) {
	_, err := readExtents(&Device{}, nil, 0)
	if err == nil {
		t.Fatal("readExtents with VBN 0: want error, got nil (VBNs are 1-based)")
	}
}

func TestReadExtentsPastEndOfFile(t *testing.T) {
	extents := []ExtentLocation{{Extent: ondisk.Extent{Count: 2, StartLBN: 100}}}
	if _, err := readExtents(&Device{}, extents, 3); err == nil {
		t.Fatal("readExtents past the end of a file's extents: want error, got nil")
	}
}
