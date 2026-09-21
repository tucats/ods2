package ondisk

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// putWord appends a little-endian 16-bit word to b and returns the result,
// as a convenience for hand-assembling retrieval-pointer map bytes in
// tests.
func putWord(b []byte, word uint16) []byte {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, word)
	return append(b, buf...)
}

func TestRetrievalPointersAllFormats(t *testing.T) {
	var mapBytes []byte

	// Format 0: a single placeholder word that decodes to no extent at
	// all. Top 2 bits 00, rest of the bits are irrelevant.
	mapBytes = putWord(mapBytes, 0x0000)

	// Format 1 ("byte" format, top 2 bits 01): count=5 (stored as 4,
	// i.e. count-1), LBN=0x012345 (top 6 bits 0x01, low 16 bits 0x2345).
	// word0 = (0b01 << 14) | (0x01 << 8) | 4 = 0x4000 | 0x0100 | 0x0004.
	mapBytes = putWord(mapBytes, 0x4104)
	mapBytes = putWord(mapBytes, 0x2345)

	// Format 2 ("word" format, top 2 bits 10): count=100 (stored as 99 =
	// 0x63), LBN=0x0012ABCD (word1=low 16 bits, word2=high 16 bits).
	mapBytes = putWord(mapBytes, 0x8063)
	mapBytes = putWord(mapBytes, 0xABCD)
	mapBytes = putWord(mapBytes, 0x0012)

	// Format 3 ("longword" format, top 2 bits 11): count=5 (stored as
	// high14=0, low16=4, i.e. count-1=4), LBN=0x00998877 (word2=low 16
	// bits, word3=high 16 bits).
	mapBytes = putWord(mapBytes, 0xC000)
	mapBytes = putWord(mapBytes, 0x0004)
	mapBytes = putWord(mapBytes, 0x8877)
	mapBytes = putWord(mapBytes, 0x0099)

	mapWords := uint8(len(mapBytes) / 2)
	const mapOffsetWords = 55 // byte 110: safely inside the header's variable-data area

	b := validFileHeaderBytes(t, mapOffsetWords, mapWords, mapBytes)
	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	got, err := h.RetrievalPointers()
	if err != nil {
		t.Fatalf("RetrievalPointers: %v", err)
	}

	want := []Extent{
		{Count: 5, StartLBN: 0x012345},
		{Count: 100, StartLBN: 0x0012ABCD},
		{Count: 5, StartLBN: 0x00998877},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RetrievalPointers() = %+v, want %+v", got, want)
	}
}

func TestRetrievalPointersEmptyMap(t *testing.T) {
	b := validFileHeaderBytes(t, 0, 0, nil)
	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	got, err := h.RetrievalPointers()
	if err != nil {
		t.Fatalf("RetrievalPointers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("RetrievalPointers() on an empty map = %+v, want empty", got)
	}
}

func TestRetrievalPointersOnlyPlaceholders(t *testing.T) {
	var mapBytes []byte
	mapBytes = putWord(mapBytes, 0x0000)
	mapBytes = putWord(mapBytes, 0x0000)
	mapWords := uint8(len(mapBytes) / 2)

	b := validFileHeaderBytes(t, 55, mapWords, mapBytes)
	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	got, err := h.RetrievalPointers()
	if err != nil {
		t.Fatalf("RetrievalPointers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("RetrievalPointers() with only placeholder entries = %+v, want empty", got)
	}
}

func TestRetrievalPointersOutOfBounds(t *testing.T) {
	// MapWordsInUse claims more words than actually fit before the end of
	// the 512-byte header.
	b := validFileHeaderBytes(t, 0, 0, nil)
	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}
	h.MapOffset = 255
	h.MapWordsInUse = 255 // 255*2 = 510 bytes starting at byte 510: runs off the end

	if _, err := h.RetrievalPointers(); err == nil {
		t.Fatal("RetrievalPointers with out-of-bounds map area: want error, got nil")
	}
}

// TestEncodeExtentPicksNarrowestFormat confirms encodeExtent doesn't just
// produce correct bytes but the narrowest ones — the whole point of
// picking format by size rather than always emitting format 3 (see
// PHASE-02.md's "what we're deliberately not porting" table). wantWords is
// the encoded length in 16-bit words: 2 for format 1, 3 for format 2, 4 for
// format 3.
func TestEncodeExtentPicksNarrowestFormat(t *testing.T) {
	tests := []struct {
		name      string
		extent    Extent
		wantWords int
	}{
		{"format1 smallest", Extent{Count: 1, StartLBN: 0}, 2},
		{"format1 max count, max LBN", Extent{Count: 256, StartLBN: 0x3FFFFF}, 2},
		{"one more count than format1 allows", Extent{Count: 257, StartLBN: 0}, 3},
		{"one more LBN than format1 allows", Extent{Count: 1, StartLBN: 0x400000}, 3},
		{"format2 max count, full-width LBN", Extent{Count: 16384, StartLBN: 0xFFFFFFFF}, 3},
		{"one more count than format2 allows", Extent{Count: 16385, StartLBN: 0}, 4},
		{"format3 max count, full-width LBN", Extent{Count: 0x40000000, StartLBN: 0xFFFFFFFF}, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := encodeExtent(tt.extent)
			if err != nil {
				t.Fatalf("encodeExtent(%+v): %v", tt.extent, err)
			}
			if gotWords := len(enc) / 2; gotWords != tt.wantWords {
				t.Errorf("encodeExtent(%+v) = %d words, want %d", tt.extent, gotWords, tt.wantWords)
			}
		})
	}
}

func TestEncodeExtentInvalidCount(t *testing.T) {
	if _, err := encodeExtent(Extent{Count: 0, StartLBN: 0}); err == nil {
		t.Error("encodeExtent with Count 0: want error, got nil")
	}
	if _, err := encodeExtent(Extent{Count: 0x40000001, StartLBN: 0}); err == nil {
		t.Error("encodeExtent with Count exceeding the format-3 ceiling: want error, got nil")
	}
}

// TestEncodeRetrievalPointersRoundTrip builds a header from a mix of
// extents spanning every format's boundary, encodes it via
// EncodeRetrievalPointers + EncodeFileHeader, decodes it back via
// DecodeFileHeader + RetrievalPointers, and confirms the exact same
// extents come back — the "RetrievalPointers(Encode(extents)) == extents"
// round trip PHASE-02.md's subtask 3 test plan calls for, exercised across
// boundary values for each format.
func TestEncodeRetrievalPointersRoundTrip(t *testing.T) {
	want := []Extent{
		{Count: 1, StartLBN: 0},
		{Count: 256, StartLBN: 0x3FFFFF},          // format 1 boundary
		{Count: 257, StartLBN: 1},                 // just past format 1's count limit
		{Count: 1, StartLBN: 0x400000},            // just past format 1's LBN limit
		{Count: 16384, StartLBN: 0xABCDEF},        // format 2 boundary
		{Count: 16385, StartLBN: 2},               // just past format 2's count limit
		{Count: 0x40000000, StartLBN: 0xFFFFFFFF}, // format 3 boundary
	}

	mapBytes, err := EncodeRetrievalPointers(want)
	if err != nil {
		t.Fatalf("EncodeRetrievalPointers: %v", err)
	}

	b, err := EncodeFileHeader(FileHeader{Fid: Fid{Num: 5, Seq: 1}}, FileHeaderAreas{MapBytes: mapBytes})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	got, err := h.RetrievalPointers()
	if err != nil {
		t.Fatalf("RetrievalPointers: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("RetrievalPointers(EncodeFileHeader(EncodeRetrievalPointers(extents))) = %+v, want %+v", got, want)
	}
}

func TestEncodeRetrievalPointersEmpty(t *testing.T) {
	got, err := EncodeRetrievalPointers(nil)
	if err != nil {
		t.Fatalf("EncodeRetrievalPointers(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("EncodeRetrievalPointers(nil) = %v, want empty", got)
	}
}

func TestEncodeRetrievalPointersPropagatesError(t *testing.T) {
	_, err := EncodeRetrievalPointers([]Extent{{Count: 1}, {Count: 0}})
	if err == nil {
		t.Fatal("EncodeRetrievalPointers with an invalid extent: want error, got nil")
	}
}
