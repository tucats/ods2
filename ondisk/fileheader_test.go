package ondisk

import (
	"encoding/binary"
	"testing"
)

// validFileHeaderBytes builds a syntactically valid, correctly-checksummed
// 512-byte file header. mapOffsetWords/mapWords let a test control the
// retrieval-pointer map area's position and size for RetrievalPointers
// tests; pass 0 for both if a test doesn't care about the map area.
func validFileHeaderBytes(t *testing.T, mapOffsetWords, mapWords uint8, mapBytes []byte) []byte {
	t.Helper()
	b := make([]byte, FileHeaderSize)

	b[fhOffIdOffset] = 39 // a plausible IDENT area offset, unused by these tests
	b[fhOffMpOffset] = mapOffsetWords
	b[fhOffAcOffset] = 200
	b[fhOffRsOffset] = 250
	binary.LittleEndian.PutUint16(b[fhOffSegNum:], 0)
	binary.LittleEndian.PutUint16(b[fhOffStrucLevel:], 0x0102)

	// Fid: Num=5, Seq=1, Rvn=0, Nmx=0
	binary.LittleEndian.PutUint16(b[fhOffFid:], 5)
	binary.LittleEndian.PutUint16(b[fhOffFid+2:], 1)

	// ExtensionFid left zero: no extension segment.

	binary.LittleEndian.PutUint32(b[fhOffFileChar:], FchDirectory)
	b[fhOffMapInUse] = mapWords
	b[fhOffAccMode] = 0

	binary.LittleEndian.PutUint16(b[fhOffFileOwner:], 4)   // Uic.Member
	binary.LittleEndian.PutUint16(b[fhOffFileOwner+2:], 1) // Uic.Group
	binary.LittleEndian.PutUint16(b[fhOffFileProt:], 0xFF00)

	// Backlink: Num=4 (the parent directory's file number)
	binary.LittleEndian.PutUint16(b[fhOffBacklink:], 4)
	binary.LittleEndian.PutUint16(b[fhOffBacklink+2:], 1)

	binary.LittleEndian.PutUint32(b[fhOffHighwater:], 10)

	if mapBytes != nil {
		start := int(mapOffsetWords) * 2
		copy(b[start:start+len(mapBytes)], mapBytes)
	}

	sum, err := Checksum(b)
	if err != nil {
		t.Fatalf("computing test fixture checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[fhOffChecksum:], sum)

	return b
}

func TestDecodeFileHeaderValid(t *testing.T) {
	b := validFileHeaderBytes(t, 0, 0, nil)

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	if h.Fid.Num != 5 || h.Fid.Seq != 1 {
		t.Errorf("Fid = %+v, want Num=5 Seq=1", h.Fid)
	}
	if !h.ExtensionFid.IsZero() {
		t.Errorf("ExtensionFid = %+v, want the zero Fid", h.ExtensionFid)
	}
	if !h.IsDirectory() {
		t.Error("IsDirectory() = false, want true (FchDirectory was set)")
	}
	if h.IsMarkedForDeletion() {
		t.Error("IsMarkedForDeletion() = true, want false")
	}
	if h.Owner.Member != 4 || h.Owner.Group != 1 {
		t.Errorf("Owner = %+v, want {Member:4 Group:1}", h.Owner)
	}
	if h.Backlink.Num != 4 {
		t.Errorf("Backlink.Num = %d, want 4", h.Backlink.Num)
	}
	if h.HighWaterMark != 10 {
		t.Errorf("HighWaterMark = %d, want 10", h.HighWaterMark)
	}
}

func TestDecodeFileHeaderBadChecksum(t *testing.T) {
	b := validFileHeaderBytes(t, 0, 0, nil)
	b[fhOffHighwater] ^= 0xFF // corrupt a field outside the checksum field itself

	if _, err := DecodeFileHeader(b); err == nil {
		t.Fatal("DecodeFileHeader with corrupted data: want error, got nil")
	}
}

func TestDecodeFileHeaderWrongSize(t *testing.T) {
	if _, err := DecodeFileHeader(make([]byte, FileHeaderSize-1)); err == nil {
		t.Fatal("DecodeFileHeader with wrong-size buffer: want error, got nil")
	}
}

func TestFidIsZero(t *testing.T) {
	if !(Fid{}).IsZero() {
		t.Error("zero-value Fid.IsZero() = false, want true")
	}
	if (Fid{Num: 1}).IsZero() {
		t.Error("Fid{Num: 1}.IsZero() = true, want false")
	}
	if (Fid{Nmx: 1}).IsZero() {
		t.Error("Fid{Nmx: 1}.IsZero() = true, want false")
	}
	// Seq and Rvn are not part of the zero-Fid convention.
	if !(Fid{Seq: 99, Rvn: 3}).IsZero() {
		t.Error("Fid{Seq: 99, Rvn: 3}.IsZero() = false, want true (only Num/Nmx matter)")
	}
}
