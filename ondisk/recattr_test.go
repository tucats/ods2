package ondisk

import "testing"

// buildRecAttrBytes assembles a 32-byte RecAttr record with each field set
// to an easily-recognizable, distinct value, for exercising DecodeRecAttr.
func buildRecAttrBytes() []byte {
	b := make([]byte, RecAttrSize)
	b[0] = byte(RecordFormatVFC)       // Format
	b[1] = AttrFortranCC | AttrPrintCC // Attributes
	b[2], b[3] = 0x11, 0x22            // RecordSize    = 0x2211
	b[4], b[5] = 0x01, 0x00            // HighestBlock: first word   = 0x0001
	b[6], b[7] = 0x02, 0x00            //               second word  = 0x0002
	b[8], b[9] = 0x03, 0x00            // EndOfFileBlock: first word = 0x0003
	b[10], b[11] = 0x04, 0x00          //                 second word = 0x0004
	b[12], b[13] = 0x55, 0x66          // FirstFreeByte = 0x6655
	b[14] = 7                          // BucketSize
	b[15] = 2                          // VfcSize
	b[16], b[17] = 0x00, 0x10          // MaxRecordSize = 0x1000
	b[18], b[19] = 0x0A, 0x00          // DefaultExtend = 10
	b[20], b[21] = 0x03, 0x00          // GlobalBufferCount = 3
	// b[22:30] reserved, left zero
	b[30], b[31] = 0x05, 0x00 // VersionLimit = 5
	return b
}

func TestDecodeRecAttr(t *testing.T) {
	ra, err := DecodeRecAttr(buildRecAttrBytes())
	if err != nil {
		t.Fatalf("DecodeRecAttr: %v", err)
	}

	if ra.Format != RecordFormatVFC {
		t.Errorf("Format = %v, want %v", ra.Format, RecordFormatVFC)
	}
	if want := AttrFortranCC | AttrPrintCC; ra.Attributes != want {
		t.Errorf("Attributes = %#x, want %#x", ra.Attributes, want)
	}
	if ra.RecordSize != 0x2211 {
		t.Errorf("RecordSize = %#x, want 0x2211", ra.RecordSize)
	}

	// This is the important case to get right: HighestBlock and
	// EndOfFileBlock use the "swapped longword" encoding, so the FIRST
	// 16-bit word becomes the HIGH half of the result and the SECOND word
	// becomes the LOW half — backwards from a plain little-endian uint32
	// decode of the same 4 bytes. Bytes {01 00 02 00} therefore decode to
	// 0x00010002, not 0x00020001 (which is what a naive LE32 read would
	// produce).
	if want := uint32(0x00010002); ra.HighestBlock != want {
		t.Errorf("HighestBlock = %#x, want %#x (check the swapped-longword decode)", ra.HighestBlock, want)
	}
	if want := uint32(0x00030004); ra.EndOfFileBlock != want {
		t.Errorf("EndOfFileBlock = %#x, want %#x (check the swapped-longword decode)", ra.EndOfFileBlock, want)
	}

	if ra.FirstFreeByte != 0x6655 {
		t.Errorf("FirstFreeByte = %#x, want 0x6655", ra.FirstFreeByte)
	}
	if ra.BucketSize != 7 {
		t.Errorf("BucketSize = %d, want 7", ra.BucketSize)
	}
	if ra.VfcSize != 2 {
		t.Errorf("VfcSize = %d, want 2", ra.VfcSize)
	}
	if ra.MaxRecordSize != 0x1000 {
		t.Errorf("MaxRecordSize = %#x, want 0x1000", ra.MaxRecordSize)
	}
	if ra.DefaultExtend != 10 {
		t.Errorf("DefaultExtend = %d, want 10", ra.DefaultExtend)
	}
	if ra.GlobalBufferCount != 3 {
		t.Errorf("GlobalBufferCount = %d, want 3", ra.GlobalBufferCount)
	}
	if ra.VersionLimit != 5 {
		t.Errorf("VersionLimit = %d, want 5", ra.VersionLimit)
	}
}

func TestDecodeRecAttrShortBuffer(t *testing.T) {
	if _, err := DecodeRecAttr(make([]byte, RecAttrSize-1)); err == nil {
		t.Fatal("DecodeRecAttr with too-short buffer: want error, got nil")
	}
}

func TestRecordFormatString(t *testing.T) {
	cases := []struct {
		format RecordFormat
		want   string
	}{
		{RecordFormatUndefined, "UNDEFINED"},
		{RecordFormatFixed, "FIXED"},
		{RecordFormatVariable, "VARIABLE"},
		{RecordFormatVFC, "VFC"},
		{RecordFormatStreamCRLF, "STREAM"},
		{RecordFormatStreamLF, "STREAMLF"},
		{RecordFormatStreamCR, "STREAMCR"},
		{RecordFormat(99), "RecordFormat(99)"},
	}
	for _, c := range cases {
		if got := c.format.String(); got != c.want {
			t.Errorf("RecordFormat(%d).String() = %q, want %q", c.format, got, c.want)
		}
	}
}
