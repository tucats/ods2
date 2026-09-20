package rms

import (
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// testFileFid is the Fid every file built by newTestFile uses. Tests
// never need to vary it, since each test builds its own fresh volume.
var testFileFid = ondisk.Fid{Num: 20, Seq: 1}

// Index bitmap layout for the small volumes newTestFile builds — see
// volume/file_test.go's newTestVolume for the same, already-proven
// pattern this mirrors.
const (
	testIdxBitmapLBN  = 5
	testIdxBitmapVBN  = 1
	testIdxBitmapSize = 3
	testDataLBN       = 100
)

func testFileHeaderLBN(fileNum uint16) uint32 {
	idxblk := uint32(fileNum) - 1 + testIdxBitmapVBN + testIdxBitmapSize
	return testIdxBitmapLBN + (idxblk - 1)
}

// newTestFile builds a minimal mountable volume containing one file whose
// on-disk data is exactly data (padded out to a whole number of blocks,
// as every real file's allocation is), with rec supplying the file's
// record-attribute fixture (Format, EndOfFileBlock, FirstFreeByte,
// MaxRecordSize, VfcSize as needed by the test) — rec.Fid,
// rec.MapOffsetWords, rec.MapBytes, and rec.HighestBlock are filled in
// automatically and don't need to be set by the caller.
func newTestFile(t *testing.T, rec odstest.FileHeaderFixture, data []byte) *volume.File {
	t.Helper()

	numBlocks := (len(data) + ondisk.BlockSize - 1) / ondisk.BlockSize
	if numBlocks == 0 {
		numBlocks = 1 // a zero-length file still occupies at least one allocated block
	}

	c := odstest.NewMemContainer(testDataLBN + numBlocks + 10)
	c.PutBlock(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:       1,
		Rvn:           1,
		IdxBitmapVBN:  testIdxBitmapVBN,
		IdxBitmapLBN:  testIdxBitmapLBN,
		IdxBitmapSize: testIdxBitmapSize,
	}))
	c.PutBlock(testFileHeaderLBN(ondisk.IndexFileFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(200, testIdxBitmapLBN),
	}))

	rec.Fid = testFileFid
	rec.MapOffsetWords = 55
	rec.MapBytes = odstest.EncodeExtentFormat2(uint32(numBlocks), testDataLBN)
	if rec.HighestBlock == 0 {
		rec.HighestBlock = uint32(numBlocks)
	}
	c.PutBlock(testFileHeaderLBN(testFileFid.Num), odstest.BuildFileHeaderBytes(t, rec))

	for i := 0; i < numBlocks; i++ {
		block := make([]byte, ondisk.BlockSize)
		start := i * ondisk.BlockSize
		end := start + ondisk.BlockSize
		if end > len(data) {
			end = len(data)
		}
		copy(block, data[start:end])
		c.PutBlock(uint32(testDataLBN+i), block)
	}

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	f, err := vol.OpenFID(testFileFid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	return f
}
