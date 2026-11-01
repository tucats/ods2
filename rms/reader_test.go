package rms

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

func readAllRecords(t *testing.T, r *Reader) [][]byte {
	t.Helper()

	var records [][]byte

	for {
		rec, err := r.Next()
		if err == io.EOF {
			return records
		}

		if err != nil {
			t.Fatalf("Next(): %v", err)
		}

		records = append(records, rec)
	}
}

func TestReaderFixed(t *testing.T) {
	// Three 4-byte fixed records, back to back, no framing at all.
	data := []byte("AAAABBBBCCCC")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatFixed,
		MaxRecordSize:  4,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAllRecords(t, r)
	want := [][]byte{[]byte("AAAA"), []byte("BBBB"), []byte("CCCC")}

	if len(records) != len(want) {
		t.Fatalf("records = %q, want %q", records, want)
	}

	for i := range want {
		if !bytes.Equal(records[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, records[i], want[i])
		}
	}
}

func TestReaderFixedZeroSizeDoesNotLoopForever(t *testing.T) {
	// A file whose header was never fully populated (MaxRecordSize left
	// at its zero value) must not cause Next() to spin forever: reading
	// zero bytes at a time can never naturally reach end of file, since
	// blockStream.ReadFull(0) trivially "succeeds" on every call. This
	// test has an implicit timeout via `go test`'s own test timeout —
	// if the fix regresses, this test hangs rather than failing cleanly.
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format: ondisk.RecordFormatFixed,
		// MaxRecordSize, EndOfFileBlock, FirstFreeByte all left at 0.
	}, nil)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next() with a zero fixed record size: err = %v, want io.EOF", err)
	}
}

// TestReaderFixedOdd: odd-length records are padded to a word, the last pad
// byte inside the end of file or not.
func TestReaderFixedOdd(t *testing.T) {
	for _, data := range [][]byte{[]byte("ABC\x00DEF\x00"), []byte("ABC\x00DEF")} {
		f := newTestFile(t, odstest.FileHeaderFixture{
			Format:         ondisk.RecordFormatFixed,
			MaxRecordSize:  3,
			EndOfFileBlock: 1,
			FirstFreeByte:  uint16(len(data)),
		}, data)

		r, err := NewReader(f)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		for i, want := range []string{"ABC", "DEF"} {
			rec, err := r.Next()
			if err != nil || string(rec) != want {
				t.Fatalf("Next() #%d = %q, %v, want %q", i, rec, err, want)
			}

			if r.RecordOffset() != int64(i*4) {
				t.Errorf("record %d's offset %d", i, r.RecordOffset())
			}
		}

		if _, err := r.Next(); err != io.EOF {
			t.Fatalf("Next() at the end: %v, want io.EOF", err)
		}
	}
}

// TestReaderRefusesOtherOrganizations: a relative file isn't read as records.
func TestReaderRefusesOtherOrganizations(t *testing.T) {
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatFixed | ondisk.RecordFormat(ondisk.OrganizationRelative),
		MaxRecordSize:  3,
		EndOfFileBlock: 2,
	}, make([]byte, 512))

	if _, err := NewReader(f); !errors.Is(err, ErrNotSequential) {
		t.Fatalf("NewReader of a relative file: %v, want ErrNotSequential", err)
	}
}

func TestFixedRecordOffset(t *testing.T) {
	cases := []struct {
		size, attributes int
		number           uint32
		want             int64
	}{
		{16, 0, 1, 0},
		{16, 0, 3, 32},
		{5, 0, 3, 12},
		{513, 0, 2, 514},
		{300, int(ondisk.AttrNoSpan), 2, 512},
		{300, int(ondisk.AttrNoSpan), 3, 1024},
		{100, int(ondisk.AttrNoSpan), 6, 512},
		{600, int(ondisk.AttrNoSpan), 2, 600},
	}

	for _, c := range cases {
		attr := ondisk.RecAttr{MaxRecordSize: uint16(c.size), Attributes: uint8(c.attributes)}
		if got := FixedRecordOffset(attr, c.number); got != c.want {
			t.Errorf("size %d attributes %d record %d: %d, want %d", c.size, c.attributes, c.number, got, c.want)
		}
	}
}

func TestReaderFixedTruncated(t *testing.T) {
	// 4-byte fixed records, but only 6 bytes of data: one whole record
	// plus 2 dangling bytes of a second, incomplete one.
	data := []byte("AAAABB")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatFixed,
		MaxRecordSize:  4,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	first, err := r.Next()
	if err != nil {
		t.Fatalf("Next() #1: %v", err)
	}
	
	if !bytes.Equal(first, []byte("AAAA")) {
		t.Fatalf("Next() #1 = %q, want %q", first, "AAAA")
	}

	// What the file has of the last record is the record.
	second, err := r.Next()
	if err != nil || !bytes.Equal(second, []byte("BB")) {
		t.Fatalf("Next() #2 (cut short) = %q, %v, want %q", second, err, "BB")
	}

	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next() #3 err = %v, want io.EOF", err)
	}
}

// buildVarRecord assembles one VAR/VFC-framed record: a 2-byte length
// prefix, the data, and a padding byte if the length is odd.
func buildVarRecord(data []byte) []byte {
	var (
		buf      bytes.Buffer
		lenBytes [2]byte
	)

	binary.LittleEndian.PutUint16(lenBytes[:], uint16(len(data)))
	buf.Write(lenBytes[:])
	buf.Write(data)

	if len(data)%2 != 0 {
		buf.WriteByte(0)
	}

	return buf.Bytes()
}

func TestReaderVariable(t *testing.T) {
	var data []byte

	data = append(data, buildVarRecord([]byte("HELLO"))...)  // odd length: padded
	data = append(data, buildVarRecord([]byte("WORLD!"))...) // even length: no padding
	data = append(data, buildVarRecord([]byte(""))...)       // a blank record

	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatVariable,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAllRecords(t, r)
	want := [][]byte{[]byte("HELLO"), []byte("WORLD!"), {}}

	if len(records) != len(want) {
		t.Fatalf("records = %q, want %q", records, want)
	}

	for i := range want {
		if !bytes.Equal(records[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, records[i], want[i])
		}
	}
}

func TestReaderVariableCorruptLength(t *testing.T) {
	// A length prefix claiming far more data than the file actually has.
	var lenBytes [2]byte

	binary.LittleEndian.PutUint16(lenBytes[:], 9999)
	data := append(lenBytes[:], []byte("short")...)

	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatVariable,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if _, err := r.Next(); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("Next() with an over-long declared length: err = %v, want ErrCorruptRecord", err)
	}
}

func TestReaderVFCIncludesControlBytes(t *testing.T) {
	// A VFC record's control bytes are part of its declared length, and
	// Next() returns them as part of the raw record -- splitting them
	// from the text is the caller's job (via FormatVFCRecord).
	vfcAndText := append([]byte{' ', 0x0D}, []byte("HELLO")...) // vfc0=' ', vfc1=CR, text="HELLO"
	data := buildVarRecord(vfcAndText)

	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatVFC,
		VfcSize:        2,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	rec, err := r.Next()
	if err != nil {
		t.Fatalf("Next(): %v", err)
	}

	if !bytes.Equal(rec, vfcAndText) {
		t.Fatalf("Next() = %q, want %q (control bytes included)", rec, vfcAndText)
	}
}

func TestReaderStreamLF(t *testing.T) {
	data := []byte("one\ntwo\nthree")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamLF,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAllRecords(t, r)

	want := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	if len(records) != len(want) {
		t.Fatalf("records = %q, want %q", records, want)
	}

	for i := range want {
		if !bytes.Equal(records[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, records[i], want[i])
		}
	}
}

func TestReaderStreamCR(t *testing.T) {
	data := []byte("one\rtwo\rthree\r")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamCR,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAllRecords(t, r)
	want := [][]byte{[]byte("one"), []byte("two"), []byte("three")}

	if len(records) != len(want) {
		t.Fatalf("records = %q, want %q", records, want)
	}
}

func TestReaderStreamCRLF(t *testing.T) {
	data := []byte("one\r\ntwo\r\n")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamCRLF,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAllRecords(t, r)
	want := [][]byte{[]byte("one"), []byte("two")}

	if len(records) != len(want) {
		t.Fatalf("records = %q, want %q", records, want)
	}

	for i := range want {
		if !bytes.Equal(records[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, records[i], want[i])
		}
	}
}

// TestReaderStreamCRLFTerminators: a STREAM record ends at CR LF, LF,
// VT, or FF, the last three kept as the record's last byte; null bytes
// before a record are skipped; a CR not followed by LF is data (the
// OpenVMS RMS Reference Manual, FAB$C_STM and $GET; VMS 7.3 returns a
// lone LF as a record of its own).
func TestReaderStreamCRLFTerminators(t *testing.T) {
	data := []byte("A\r\n\nB\vC\f\x00\x00D\rE\r\nlast\r")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamCRLF,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	for _, want := range []string{"A", "\n", "B\v", "C\f", "D\rE", "last\r"} {
		rec, err := r.Next()
		if err != nil || string(rec) != want {
			t.Fatalf("Next() = %q, %v; want %q", rec, err, want)
		}
	}

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last record: %v, want io.EOF", err)
	}
}

func TestReaderStreamLastRecordWithoutDelimiter(t *testing.T) {
	// A stream file's final record has no requirement to end with a
	// delimiter -- this is normal, not corruption.
	data := []byte("one\ntwo")
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatStreamLF,
		EndOfFileBlock: 1,
		FirstFreeByte:  uint16(len(data)),
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAllRecords(t, r)
	want := [][]byte{[]byte("one"), []byte("two")}

	if len(records) != len(want) {
		t.Fatalf("records = %q, want %q", records, want)
	}
}

func TestReaderEmptyFile(t *testing.T) {
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format: ondisk.RecordFormatStreamLF,
		// EndOfFileBlock left at 0: no data at all.
	}, nil)

	r, err := NewReader(f)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next() on an empty file: err = %v, want io.EOF", err)
	}
}

func TestNewReaderRejectsUnsupportedFormat(t *testing.T) {
	f := newTestFile(t, odstest.FileHeaderFixture{
		Format: ondisk.RecordFormat(99),
	}, nil)

	if _, err := NewReader(f); err == nil {
		t.Fatal("NewReader with an unsupported format: want error, got nil")
	}
}
