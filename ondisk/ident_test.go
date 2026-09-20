package ondisk

import (
	"encoding/binary"
	"testing"

	"github.com/tucats/ods2/vmstime"
)

func TestFileHeaderIdent(t *testing.T) {
	b := make([]byte, FileHeaderSize)

	// Word offset 54 -> byte offset 108, exactly where the "rest of it"
	// variable-position area begins (right after the fixed portion of the
	// header, which ends at byte 108) -- safely clear of every fixed
	// field this test doesn't otherwise set.
	const identOffsetWords = 54
	identStart := identOffsetWords * 2
	b[fhOffIdOffset] = identOffsetWords

	area := b[identStart : identStart+identSize]
	copy(area[identOffFilename:identOffFilename+20], "TEST.TXT")
	binary.LittleEndian.PutUint16(area[identOffRevision:], 3)
	binary.LittleEndian.PutUint64(area[identOffCreDate:], 0)               // VMS epoch
	binary.LittleEndian.PutUint64(area[identOffRevDate:], uint64(1000000)) // an arbitrary later time
	copy(area[identOffFilenameExt:identOffFilenameExt+66], "")

	sum, err := Checksum(b)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[fhOffChecksum:], sum)

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	ident, err := h.Ident()
	if err != nil {
		t.Fatalf("Ident: %v", err)
	}

	if ident.Filename != "TEST.TXT" {
		t.Errorf("Filename = %q, want %q", ident.Filename, "TEST.TXT")
	}
	if ident.Revision != 3 {
		t.Errorf("Revision = %d, want 3", ident.Revision)
	}
	if ident.CreationDate != vmstime.VMSTime(0) {
		t.Errorf("CreationDate = %v, want the VMS epoch", ident.CreationDate)
	}
	if ident.RevisionDate != vmstime.VMSTime(1000000) {
		t.Errorf("RevisionDate = %v, want 1000000", ident.RevisionDate)
	}
	if ident.FilenameExtension != "" {
		t.Errorf("FilenameExtension = %q, want empty", ident.FilenameExtension)
	}
}

func TestFileHeaderIdentOutOfBounds(t *testing.T) {
	b := make([]byte, FileHeaderSize)
	b[fhOffIdOffset] = 250 // word offset 500 -> IDENT would run past the 512-byte header

	sum, err := Checksum(b)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[fhOffChecksum:], sum)

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	if _, err := h.Ident(); err == nil {
		t.Fatal("Ident() with an out-of-bounds IdentOffset: want error, got nil")
	}
}

func TestDecodeNulPaddedString(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte("HELLO\x00\x00\x00"), "HELLO"},
		{[]byte("HELLO   "), "HELLO"},
		{[]byte("HELLO\x00WORLD\x00\x00"), "HELLO\x00WORLD"}, // only the trailing run is trimmed; an embedded NUL is preserved
		{[]byte("\x00\x00\x00"), ""},
	}
	for _, c := range cases {
		if got := decodeNulPaddedString(c.in); got != c.want {
			t.Errorf("decodeNulPaddedString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
