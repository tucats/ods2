package rms

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

// TestReaderFromBytes reads variable-length records from data held in
// memory, one record crossing from the first block into the second, and
// stops at the end of file the attributes give, not at the data's end.
func TestReaderFromBytes(t *testing.T) {
	data := make([]byte, 2*ondisk.BlockSize)

	put := func(off int, text string) int {
		binary.LittleEndian.PutUint16(data[off:], uint16(len(text)))
		copy(data[off+2:], text)

		return off + 2 + len(text) + len(text)%2
	}

	off := put(0, "FIRST")
	off = put(off, string(bytes.Repeat([]byte("x"), 600)))
	end := put(off, "LAST")
	put(end, "PAST THE END")

	attr := ondisk.RecAttr{
		Format:         ondisk.RecordFormatVariable,
		EndOfFileBlock: uint32(end/ondisk.BlockSize) + 1,
		FirstFreeByte:  uint16(end % ondisk.BlockSize),
	}

	r, err := NewReaderFrom(BytesSource(data), attr)
	if err != nil {
		t.Fatalf("NewReaderFrom: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != 3 || string(got[0]) != "FIRST" || len(got[1]) != 600 || string(got[2]) != "LAST" {
		t.Errorf("records = %q, want FIRST, 600 x's, LAST", got)
	}
}

// TestBytesSourceReadBlock reads a short last block and a block past the
// data's end, both padded with zeros.
func TestBytesSourceReadBlock(t *testing.T) {
	src := BytesSource(bytes.Repeat([]byte{7}, ondisk.BlockSize+3))
	buf := bytes.Repeat([]byte{9}, ondisk.BlockSize)

	if err := src.ReadBlock(2, buf); err != nil {
		t.Fatalf("ReadBlock(2): %v", err)
	}

	if !bytes.Equal(buf[:3], []byte{7, 7, 7}) || buf[3] != 0 || buf[ondisk.BlockSize-1] != 0 {
		t.Errorf("block 2 = % x..., want three 7s and zeros", buf[:8])
	}

	if err := src.ReadBlock(5, buf); err != nil || !bytes.Equal(buf, make([]byte, ondisk.BlockSize)) {
		t.Errorf("block 5: %v, want zeros", err)
	}

	if err := src.ReadBlock(0, buf); err == nil {
		t.Error("ReadBlock(0) succeeded")
	}
}
