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

// TestEncodeFileHeaderRoundTrip builds a FileHeader with an IDENT area and
// a retrieval-pointer map area, encodes it, and confirms every meaningful
// piece survives the round trip -- both the fixed fields (compared
// directly) and the variable-position areas (compared via the existing
// decode-side Ident()/RetrievalPointers() methods, since EncodeFileHeader
// chooses its own IdentOffset/MapOffset/AclOffset/EndOffset layout rather
// than replaying whatever the input FileHeader happened to carry in those
// fields -- see EncodeFileHeader's doc comment).
func TestEncodeFileHeaderRoundTrip(t *testing.T) {
	in := FileHeader{
		SegmentNumber:  0,
		StructureLevel: 0x0102,
		Fid:            Fid{Num: 5, Seq: 1},
		ExtensionFid:   Fid{Num: 6, Seq: 1},
		RecordAttributes: RecAttr{
			Format:       RecordFormatVariable,
			HighestBlock: 20,
		},
		FileCharacteristics: FchDirectory,
		AccessMode:          1,
		Owner:               Uic{Member: 4, Group: 1},
		FileProtection:      0xFF00,
		Backlink:            Fid{Num: 4, Seq: 1},
		Journaling:          0,
		RecoveryUnitActive:  0,
		HighWaterMark:       10,
	}
	in.ClassProtection[0] = 0xAB

	ident := Ident{Filename: "TEST.TXT", Revision: 3}
	mapBytes := make([]byte, 6)
	binary.LittleEndian.PutUint16(mapBytes[0:2], 0x8000|((100-1)&0x3FFF)) // format 2: count=100
	binary.LittleEndian.PutUint16(mapBytes[2:4], 0x2000)                  // LBN low word
	binary.LittleEndian.PutUint16(mapBytes[4:6], 0x0001)                  // LBN high word

	b, err := EncodeFileHeader(in, FileHeaderAreas{Ident: &ident, MapBytes: mapBytes})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	out, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader(EncodeFileHeader(in)): %v", err)
	}

	if out.SegmentNumber != in.SegmentNumber {
		t.Errorf("SegmentNumber = %d, want %d", out.SegmentNumber, in.SegmentNumber)
	}
	if out.StructureLevel != in.StructureLevel {
		t.Errorf("StructureLevel = %#x, want %#x", out.StructureLevel, in.StructureLevel)
	}
	if out.Fid != in.Fid {
		t.Errorf("Fid = %+v, want %+v", out.Fid, in.Fid)
	}
	if out.ExtensionFid != in.ExtensionFid {
		t.Errorf("ExtensionFid = %+v, want %+v", out.ExtensionFid, in.ExtensionFid)
	}
	if out.RecordAttributes != in.RecordAttributes {
		t.Errorf("RecordAttributes = %+v, want %+v", out.RecordAttributes, in.RecordAttributes)
	}
	if out.FileCharacteristics != in.FileCharacteristics {
		t.Errorf("FileCharacteristics = %#x, want %#x", out.FileCharacteristics, in.FileCharacteristics)
	}
	if out.AccessMode != in.AccessMode {
		t.Errorf("AccessMode = %d, want %d", out.AccessMode, in.AccessMode)
	}
	if out.Owner != in.Owner {
		t.Errorf("Owner = %+v, want %+v", out.Owner, in.Owner)
	}
	if out.FileProtection != in.FileProtection {
		t.Errorf("FileProtection = %#x, want %#x", out.FileProtection, in.FileProtection)
	}
	if out.Backlink != in.Backlink {
		t.Errorf("Backlink = %+v, want %+v", out.Backlink, in.Backlink)
	}
	if out.HighWaterMark != in.HighWaterMark {
		t.Errorf("HighWaterMark = %d, want %d", out.HighWaterMark, in.HighWaterMark)
	}
	if out.ClassProtection != in.ClassProtection {
		t.Errorf("ClassProtection = %v, want %v", out.ClassProtection, in.ClassProtection)
	}

	gotIdent, err := out.Ident()
	if err != nil {
		t.Fatalf("Ident: %v", err)
	}
	if gotIdent.Filename != ident.Filename || gotIdent.Revision != ident.Revision {
		t.Errorf("Ident() = %+v, want Filename=%q Revision=%d", gotIdent, ident.Filename, ident.Revision)
	}

	extents, err := out.RetrievalPointers()
	if err != nil {
		t.Fatalf("RetrievalPointers: %v", err)
	}
	if len(extents) != 1 || extents[0].Count != 100 || extents[0].StartLBN != 0x00012000 {
		t.Errorf("RetrievalPointers() = %+v, want one extent {Count:100 StartLBN:0x12000}", extents)
	}
}

// TestEncodeFileHeaderNoIdent confirms a nil Ident produces a header with
// a zero-length IDENT area -- IdentOffset and MapOffset land on the same
// word position, with no IDENT content in between -- rather than
// EncodeFileHeader requiring one.
func TestEncodeFileHeaderNoIdent(t *testing.T) {
	b, err := EncodeFileHeader(FileHeader{Fid: Fid{Num: 5, Seq: 1}}, FileHeaderAreas{})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}
	if h.IdentOffset != h.MapOffset {
		t.Errorf("IdentOffset = %d, MapOffset = %d, want them equal (zero-length IDENT area)", h.IdentOffset, h.MapOffset)
	}
}

func TestEncodeFileHeaderAreasTooLarge(t *testing.T) {
	// 402 bytes are available for IDENT+map+ACL combined; a map area alone
	// bigger than that can't possibly fit.
	_, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{MapBytes: make([]byte, 500)})
	if err == nil {
		t.Fatal("EncodeFileHeader with an oversized map area: want error, got nil")
	}
}

func TestEncodeFileHeaderOddLengthMapBytes(t *testing.T) {
	_, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{MapBytes: make([]byte, 3)})
	if err == nil {
		t.Fatal("EncodeFileHeader with odd-length MapBytes: want error, got nil")
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
