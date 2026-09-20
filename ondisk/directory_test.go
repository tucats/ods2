package ondisk

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// putFid writes a Fid's 6-byte on-disk representation into b at the given
// offset, as a test-only inverse of DecodeFid (production code never needs
// to encode a Fid, since this project doesn't write ODS-2 volumes).
func putFid(b []byte, offset int, fid Fid) {
	binary.LittleEndian.PutUint16(b[offset:], fid.Num)
	binary.LittleEndian.PutUint16(b[offset+2:], fid.Seq)
	b[offset+4] = fid.Rvn
	b[offset+5] = fid.Nmx
}

// buildDirRecordBytes assembles one directory name record — header, name
// text, padding, and version entries — exactly as DecodeDirectoryBlock
// expects to find it on disk.
func buildDirRecordBytes(name string, versions []uint16, fids []Fid) []byte {
	if len(versions) != len(fids) {
		panic("buildDirRecordBytes: versions and fids must be the same length")
	}

	nameBytes := []byte(name)
	paddedNameLen := roundUpToEven(len(nameBytes))
	entriesStart := dirRecHeaderSize + paddedNameLen
	totalLen := entriesStart + len(versions)*dirEntSize

	b := make([]byte, totalLen)
	binary.LittleEndian.PutUint16(b[0:2], uint16(totalLen-2)) // dir$size = total record length - 2
	binary.LittleEndian.PutUint16(b[2:4], 0)                  // verlimit: unused by these tests
	b[4] = 0                                                  // flags: unused by these tests
	b[5] = byte(len(nameBytes))
	copy(b[dirRecHeaderSize:dirRecHeaderSize+len(nameBytes)], nameBytes)
	// Any padding byte between the name and entriesStart is left as the
	// zero value, matching a freshly zero-filled block on disk.

	for i, v := range versions {
		off := entriesStart + i*dirEntSize
		binary.LittleEndian.PutUint16(b[off:off+2], v)
		putFid(b, off+2, fids[i])
	}
	return b
}

// buildDirBlock assembles a full 512-byte directory block out of the given
// records, followed by the 0xFFFF end-of-data sentinel.
func buildDirBlock(records ...[]byte) []byte {
	block := make([]byte, BlockSize)
	offset := 0
	for _, r := range records {
		copy(block[offset:], r)
		offset += len(r)
	}
	if offset+2 <= len(block) {
		binary.LittleEndian.PutUint16(block[offset:offset+2], 0xFFFF)
	}
	return block
}

func TestDecodeDirectoryBlockSingleRecord(t *testing.T) {
	fid := Fid{Num: 10, Seq: 1}
	record := buildDirRecordBytes("README.TXT", []uint16{1}, []Fid{fid}) // even-length name (10 chars); see the next test for the odd-length/padding case
	block := buildDirBlock(record)

	got, err := DecodeDirectoryBlock(block)
	if err != nil {
		t.Fatalf("DecodeDirectoryBlock: %v", err)
	}

	want := []DirEntry{{Name: "README.TXT", Version: 1, Fid: fid}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeDirectoryBlock() = %+v, want %+v", got, want)
	}
}

func TestDecodeDirectoryBlockOddLengthNameIsPadded(t *testing.T) {
	// "A.TXT" is 5 characters (odd), so DecodeDirectoryBlock must skip one
	// padding byte before reading the version entry — if it didn't, it
	// would misinterpret the padding byte as part of the entry and fail
	// to decode a sensible Fid/version.
	fid := Fid{Num: 20, Seq: 2, Rvn: 1}
	record := buildDirRecordBytes("A.TXT", []uint16{3}, []Fid{fid})
	block := buildDirBlock(record)

	got, err := DecodeDirectoryBlock(block)
	if err != nil {
		t.Fatalf("DecodeDirectoryBlock: %v", err)
	}

	want := []DirEntry{{Name: "A.TXT", Version: 3, Fid: fid}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeDirectoryBlock() = %+v, want %+v", got, want)
	}
}

func TestDecodeDirectoryBlockMultipleVersionsOneName(t *testing.T) {
	fidV1 := Fid{Num: 30, Seq: 1}
	fidV2 := Fid{Num: 30, Seq: 2}
	record := buildDirRecordBytes("DATA.DAT", []uint16{1, 2}, []Fid{fidV1, fidV2})
	block := buildDirBlock(record)

	got, err := DecodeDirectoryBlock(block)
	if err != nil {
		t.Fatalf("DecodeDirectoryBlock: %v", err)
	}

	want := []DirEntry{
		{Name: "DATA.DAT", Version: 1, Fid: fidV1},
		{Name: "DATA.DAT", Version: 2, Fid: fidV2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeDirectoryBlock() = %+v, want %+v", got, want)
	}
}

func TestDecodeDirectoryBlockMultipleRecords(t *testing.T) {
	fid1 := Fid{Num: 1, Seq: 1}
	fid2 := Fid{Num: 2, Seq: 1}
	record1 := buildDirRecordBytes("ALPHA.TXT", []uint16{1}, []Fid{fid1})
	record2 := buildDirRecordBytes("BETA.DIR", []uint16{1}, []Fid{fid2})
	block := buildDirBlock(record1, record2)

	got, err := DecodeDirectoryBlock(block)
	if err != nil {
		t.Fatalf("DecodeDirectoryBlock: %v", err)
	}

	want := []DirEntry{
		{Name: "ALPHA.TXT", Version: 1, Fid: fid1},
		{Name: "BETA.DIR", Version: 1, Fid: fid2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeDirectoryBlock() = %+v, want %+v", got, want)
	}
}

func TestDecodeDirectoryBlockEmpty(t *testing.T) {
	// A block containing only the end-of-data sentinel: a legitimately
	// empty directory (or the tail block of one), not an error.
	block := buildDirBlock()

	got, err := DecodeDirectoryBlock(block)
	if err != nil {
		t.Fatalf("DecodeDirectoryBlock: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DecodeDirectoryBlock() on an empty block = %+v, want empty", got)
	}
}

func TestDecodeDirectoryBlockSizeAboveMaxEndsScanWithoutError(t *testing.T) {
	// Any size field greater than dirMaxRecordSize is indistinguishable
	// from the 0xFFFF end-of-data sentinel (the original implementation
	// treats them identically), so it should end the scan cleanly rather
	// than being reported as an error.
	record := buildDirRecordBytes("FOO.TXT", []uint16{1}, []Fid{{Num: 1}})
	binary.LittleEndian.PutUint16(record[0:2], BlockSize) // BlockSize > dirMaxRecordSize
	block := buildDirBlock(record)

	got, err := DecodeDirectoryBlock(block)
	if err != nil {
		t.Fatalf("DecodeDirectoryBlock: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DecodeDirectoryBlock() = %+v, want empty (size above dirMaxRecordSize should end the scan)", got)
	}
}

func TestDecodeDirectoryBlockRecordOverrunsBlock(t *testing.T) {
	// A record whose declared size is within the legal range
	// (<= dirMaxRecordSize) but which, combined with its starting offset,
	// would run past the end of the 512-byte block: this is genuinely
	// corrupt data, not an end-of-data marker, and must be reported as an
	// error rather than silently truncated or read out of bounds.
	block := make([]byte, BlockSize)

	// A minimal valid record at offset 0: empty name, no entries, so its
	// total length is just the 6-byte header (dir$size = 6 - 2 = 4).
	binary.LittleEndian.PutUint16(block[0:2], 4)

	// A second record starting at offset 6, claiming the maximum legal
	// size (510) — its implied end, 6+510+2 = 518, is past the end of
	// the 512-byte block.
	const secondRecordOffset = 6
	binary.LittleEndian.PutUint16(block[secondRecordOffset:secondRecordOffset+2], dirMaxRecordSize)

	if _, err := DecodeDirectoryBlock(block); err == nil {
		t.Fatal("DecodeDirectoryBlock with a record that overruns the block: want error, got nil")
	}
}

func TestDecodeDirectoryBlockCorruptEntryAlignment(t *testing.T) {
	record := buildDirRecordBytes("FOO.TXT", []uint16{1}, []Fid{{Num: 1}})
	// Shrink the record by one byte without adjusting the name length,
	// so the version-entry area is no longer a whole number of 8-byte
	// entries.
	record = record[:len(record)-1]
	binary.LittleEndian.PutUint16(record[0:2], uint16(len(record)-2))
	block := buildDirBlock(record)

	if _, err := DecodeDirectoryBlock(block); err == nil {
		t.Fatal("DecodeDirectoryBlock with misaligned entry area: want error, got nil")
	}
}

func TestDecodeDirectoryBlockWrongSize(t *testing.T) {
	if _, err := DecodeDirectoryBlock(make([]byte, BlockSize-1)); err == nil {
		t.Fatal("DecodeDirectoryBlock with wrong-size buffer: want error, got nil")
	}
}

func TestRoundUpToEven(t *testing.T) {
	cases := map[int]int{0: 0, 1: 2, 2: 2, 3: 4, 10: 10, 11: 12}
	for n, want := range cases {
		if got := roundUpToEven(n); got != want {
			t.Errorf("roundUpToEven(%d) = %d, want %d", n, got, want)
		}
	}
}
