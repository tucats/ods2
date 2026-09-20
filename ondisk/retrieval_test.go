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
