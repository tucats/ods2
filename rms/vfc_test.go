package rms

import (
	"bytes"
	"testing"
)

func TestFormatVFCRecord(t *testing.T) {
	cases := []struct {
		name string
		vfc0 byte
		vfc1 byte
		want []byte
	}{
		{"none", 0, 0, []byte("TEXT")},
		// 0x8D = 1000 1101: top-3-bits 100 -> "literal end-of-record
		// char", low 5 bits 0x0D -> that char is CR. This is the exact
		// byte fileFormats.html's own worked example uses for "normal"
		// trailing carriage control.
		{"normal", ' ', 0x8D, []byte("\nTEXT\r")},
		{"prompt no trailing", '$', 0, []byte("\nTEXT")},
		{"overstrike", '+', 0x8D, []byte("TEXT\r")},
		{"double space", '0', 0x8D, []byte("\n\nTEXT\r")},
		{"form feed", '1', 0x8D, []byte("\fTEXT\r")},
		{"two newlines then CR", ' ', 0x02, []byte("\nTEXT\n\n\r")}, // bit7=0, low7bits=2 -> "\n\n\r"
		{"zero newlines then CR", ' ', 0x00, []byte("\nTEXT")},      // vfc1==0: no trailing at all (distinct from "0 newlines + CR")
		{"just one CR (101 pattern)", ' ', 0xA0, []byte("\nTEXT\r")},
		{"VFU fallback (1100 pattern)", ' ', 0xC5, []byte("\nTEXT\r")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatVFCRecord([]byte{c.vfc0, c.vfc1}, []byte("TEXT"))
			if !bytes.Equal(got, c.want) {
				t.Errorf("FormatVFCRecord({%#x,%#x}, TEXT) = %q, want %q", c.vfc0, c.vfc1, got, c.want)
			}
		})
	}
}

func TestFormatVFCRecordPassesThroughForNonStandardVfcSize(t *testing.T) {
	text := []byte("TEXT")
	got := FormatVFCRecord([]byte{1, 2, 3}, text) // VfcSize 3: not the standard case
	if !bytes.Equal(got, text) {
		t.Errorf("FormatVFCRecord with 3-byte vfc = %q, want %q (passed through unchanged)", got, text)
	}
}

func TestVfcTrailingEndOfRecordChar(t *testing.T) {
	// Bit pattern 100xxxxx: bits 4-0 give the literal end-of-record
	// character. 0x81 = 1000 0001 -> character 0x01.
	got := vfcTrailing(0x81)
	want := []byte{0x01}
	if !bytes.Equal(got, want) {
		t.Errorf("vfcTrailing(0x81) = %v, want %v", got, want)
	}
}
