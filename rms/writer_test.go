package rms

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// newWritableTestVolume builds a freshly initialized, mountable volume via
// volume.Initialize (docs/PHASE-02.md subtask 12) on a real temporary disk
// image -- this package's Writer tests need a genuinely writable
// volume.File, and package volume's own writable test fixtures
// (newWritableHeaderTestVolume and friends) are unexported and live in a
// different package, so aren't reachable from here. Everything this helper
// does is public API any other writer of this project would also use.
func newWritableTestVolume(t *testing.T) *volume.Volume {
	t.Helper()

	path := filepath.Join(t.TempDir(), "writer_test.dsk")

	c, err := diskimage.Create(path, 400)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "RMSTEST"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	return vol
}

// newWritableTestFile creates one new file named name on vol's master file
// directory with the given record attributes, returning it already armed
// for writing (volume.Volume.CreateFile's own contract) -- ready to hand
// straight to NewWriter.
func newWritableTestFile(t *testing.T, vol *volume.Volume, name string, recAttr ondisk.RecAttr) *volume.File {
	t.Helper()

	dir, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	dev := vol.Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap: %v", err)
	}

	f, err := vol.CreateFile(dir, name, recAttr, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile(%s): %v", name, err)
	}

	return f
}

// reopenTestFile re-opens fid through vol's own OpenFID, independent of
// whatever in-memory volume.File a test has already been writing through --
// so a round-trip test actually confirms the data reached disk, not just
// that it's still sitting in memory unchanged.
func reopenTestFile(t *testing.T, vol *volume.Volume, fid ondisk.Fid) *volume.File {
	t.Helper()

	f, err := vol.OpenFID(fid)
	if err != nil {
		t.Fatalf("OpenFID(%v): %v", fid, err)
	}

	return f
}

func TestWriterFixedRoundTrip(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "FIXED.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, MaxRecordSize: 4})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	want := [][]byte{[]byte("AAAA"), []byte("BBBB"), []byte("CCCC")}
	for _, rec := range want {
		if err := w.Put(rec); err != nil {
			t.Fatalf("Put(%q): %v", rec, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 12 bytes of real content doesn't end on a block boundary -- confirm
	// FirstFreeByte lands precisely, not rounded up to a whole block the
	// way volume.File.Close's own convention would.
	if got, want := f.Header.RecordAttributes.EndOfFileBlock, uint32(1); got != want {
		t.Errorf("EndOfFileBlock = %d, want %d", got, want)
	}

	if got, want := f.Header.RecordAttributes.FirstFreeByte, uint16(12); got != want {
		t.Errorf("FirstFreeByte = %d, want %d", got, want)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}

	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriterFixedRejectsWrongSizedRecord(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "BADSIZE.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, MaxRecordSize: 4})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if err := w.Put([]byte("TOOLONG")); err == nil {
		t.Fatal("Put with a wrong-sized fixed record: want error, got nil")
	}
}

func TestWriterFixedZeroRecordSizeRejected(t *testing.T) {
	vol := newWritableTestVolume(t)
	// MaxRecordSize left at 0 -- a misconfigured header, mirroring
	// TestReaderFixedZeroSizeDoesNotLoopForever's fixture on the read
	// side, except here Put must fail loudly rather than silently
	// accepting a record it has no well-defined way to frame.
	f := newWritableTestFile(t, vol, "ZEROSIZE.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if err := w.Put([]byte("X")); err == nil {
		t.Fatal("Put on a file with MaxRecordSize 0: want error, got nil")
	}
}

func TestWriterVariableRoundTrip(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "VAR.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatVariable})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	want := [][]byte{[]byte("HELLO"), []byte("WORLD!"), {}}
	for _, rec := range want {
		if err := w.Put(rec); err != nil {
			t.Fatalf("Put(%q): %v", rec, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}

	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriterVariableRejectsOverlongRecord(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "OVERLONG.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatVariable})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if err := w.Put(make([]byte, 0x10000)); err == nil {
		t.Fatal("Put with a 65536-byte record: want error, got nil")
	}
}

func TestWriterVFCRoundTrip(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "VFC.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatVFC, VfcSize: 2})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// vfc0=' ' (single leading newline), vfc1=CR (a plain trailing '\r'),
	// text="HELLO" -- the same fixture TestReaderVFCIncludesControlBytes
	// uses on the read side.
	vfcAndText := append([]byte{' ', 0x0D}, []byte("HELLO")...)
	if err := w.Put(vfcAndText); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
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

	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next() after the only record: err = %v, want io.EOF", err)
	}
}

func TestWriterStreamLFRoundTrip(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "LF.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	want := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	for _, rec := range want {
		if err := w.Put(rec); err != nil {
			t.Fatalf("Put(%q): %v", rec, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}

	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriterStreamCRRoundTrip(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "CR.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatStreamCR})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	want := [][]byte{[]byte("one"), []byte("two")}
	for _, rec := range want {
		if err := w.Put(rec); err != nil {
			t.Fatalf("Put(%q): %v", rec, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}

	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriterStreamCRLFRoundTrip(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "CRLF.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatStreamCRLF})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	want := [][]byte{[]byte("one"), []byte("two")}
	for _, rec := range want {
		if err := w.Put(rec); err != nil {
			t.Fatalf("Put(%q): %v", rec, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}

	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("record %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWriterSpansMultipleBlocks writes enough fixed-size records to force
// append's mid-Put block-flush path (subtask 13's counterpart to subtask
// 10's out-of-order-write auto-extend coverage): 200 4-byte records is 800
// bytes, more than one 512-byte block, ending partway through the second.
func TestWriterSpansMultipleBlocks(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "SPAN.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, MaxRecordSize: 4})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	const count = 200

	var want [][]byte

	for i := 0; i < count; i++ {
		rec := []byte{byte(i), byte(i >> 8), 0, 0}
		want = append(want, rec)

		if err := w.Put(rec); err != nil {
			t.Fatalf("Put(#%d): %v", i, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	const totalBytes = count * 4 // 800

	if got, want := f.Header.RecordAttributes.EndOfFileBlock, uint32(2); got != want {
		t.Errorf("EndOfFileBlock = %d, want %d", got, want)
	}

	if got, want := f.Header.RecordAttributes.FirstFreeByte, uint16(totalBytes-ondisk.BlockSize); got != want {
		t.Errorf("FirstFreeByte = %d, want %d", got, want)
	}

	if got, want := f.Blocks(), uint32(2); got != want {
		t.Errorf("Blocks() = %d, want %d", got, want)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := readAllRecords(t, r)
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}

	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("record %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestWriterDataEndingExactlyOnBlockBoundary confirms Writer produces the
// same whole-block convention volume.File.Close itself uses (FirstFreeByte
// 0, EndOfFileBlock one past the last full block) when the real content
// happens to end exactly on a block boundary -- the finalByte-0 case
// CloseWithFinalByte treats identically to plain Close.
func TestWriterDataEndingExactlyOnBlockBoundary(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "EXACT.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, MaxRecordSize: 4})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// 128 4-byte records = 512 bytes = exactly one block.
	for i := 0; i < ondisk.BlockSize/4; i++ {
		if err := w.Put([]byte{byte(i), 0, 0, 0}); err != nil {
			t.Fatalf("Put(#%d): %v", i, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := f.Header.RecordAttributes.EndOfFileBlock, uint32(2); got != want {
		t.Errorf("EndOfFileBlock = %d, want %d", got, want)
	}

	if got, want := f.Header.RecordAttributes.FirstFreeByte, uint16(0); got != want {
		t.Errorf("FirstFreeByte = %d, want %d", got, want)
	}
}

func TestWriterEmptyFileClose(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "EMPTY.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := f.Header.RecordAttributes.EndOfFileBlock, uint32(0); got != want {
		t.Errorf("EndOfFileBlock = %d, want %d (no data ever written)", got, want)
	}

	reopened := reopenTestFile(t, vol, f.Header.Fid)

	r, err := NewReader(reopened)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next() on an empty file: err = %v, want io.EOF", err)
	}
}

func TestWriterCloseIsIdempotentAndBlocksFurtherPut(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "IDEM.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, MaxRecordSize: 4})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if err := w.Put([]byte("AAAA")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if err := w.Put([]byte("BBBB")); err == nil {
		t.Fatal("Put after Close: want error, got nil")
	}
}

func TestNewWriterRejectsUnsupportedFormat(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "BADFMT.DAT", ondisk.RecAttr{Format: ondisk.RecordFormat(99)})

	if _, err := NewWriter(f); err == nil {
		t.Fatal("NewWriter with an unsupported format: want error, got nil")
	}
}

// TestWriterVariableEndOfRecords: a Variable or VFC file's last, partly
// filled block has FFFF after its last record, as VMS's RMS leaves it
// (VMS 7.3's DUMP/BLOCK of a file DCL's OPEN/WRITE made: "abc", "",
// "defgh" in a VFC file, then FFFF), and an appender writes over it.
func TestWriterVariableEndOfRecords(t *testing.T) {
	vol := newWritableTestVolume(t)
	f := newWritableTestFile(t, vol, "VFC.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatVFC, VfcSize: 2})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}

	for _, rec := range []string{"\x01\x8Dabc", "\x01\x8D", "\x01\x8Ddefgh"} {
		if err := w.Put([]byte(rec)); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	block := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(1, block); err != nil {
		t.Fatal(err)
	}

	want := []byte{5, 0, 1, 0x8D, 'a', 'b', 'c', 0, 2, 0, 1, 0x8D, 7, 0, 1, 0x8D, 'd', 'e', 'f', 'g', 'h', 0, 0xFF, 0xFF, 0}
	if !bytes.Equal(block[:len(want)], want) {
		t.Errorf("block 1 = % X, want % X", block[:len(want)], want)
	}

	if ffb := f.Header.RecordAttributes.FirstFreeByte; ffb != 22 {
		t.Errorf("FirstFreeByte = %d, want 22, before the FFFF", ffb)
	}

	// An appender's first record goes over the FFFF, and the block
	// ends with one again.
	reopened := reopenTestFile(t, vol, f.Header.Fid)
	bm, _ := reopened.Device.Bitmap()
	ib, _ := reopened.Device.IndexBitmap()

	if err := reopened.OpenForWrite(bm, ib); err != nil {
		t.Fatal(err)
	}

	a, err := NewAppender(reopened)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Put([]byte("\x01\x8Dxy")); err != nil {
		t.Fatal(err)
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	if err := reopened.ReadBlock(1, block); err != nil {
		t.Fatal(err)
	}

	want = append(want[:22:22], 4, 0, 1, 0x8D, 'x', 'y', 0xFF, 0xFF)
	if !bytes.Equal(block[:len(want)], want) {
		t.Errorf("block 1 after the append = % X, want % X", block[:len(want)], want)
	}

	r, err := NewReader(reopenTestFile(t, vol, f.Header.Fid))
	if err != nil {
		t.Fatal(err)
	}

	if got := readAllRecords(t, r); len(got) != 4 || string(got[3]) != "\x01\x8Dxy" {
		t.Errorf("records = %q", got)
	}
}

// TestReaderSkipsEndOfRecords: an FFFF record length within the file says
// its block has no more records, as in a file whose records don't span
// blocks; the next record is at the next block, with its own offset.
func TestReaderSkipsEndOfRecords(t *testing.T) {
	data := make([]byte, 2*ondisk.BlockSize)
	copy(data, []byte{3, 0, 'o', 'n', 'e', 0, 0xFF, 0xFF})
	copy(data[ondisk.BlockSize:], []byte{3, 0, 't', 'w', 'o', 0})

	f := newTestFile(t, odstest.FileHeaderFixture{
		Format:         ondisk.RecordFormatVariable,
		EndOfFileBlock: 2,
		FirstFreeByte:  6,
	}, data)

	r, err := NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	first, err := r.Next()
	if err != nil || string(first) != "one" {
		t.Fatalf("first record %q, %v", first, err)
	}

	second, err := r.Next()
	if err != nil || string(second) != "two" || r.RecordOffset() != ondisk.BlockSize {
		t.Fatalf("second record %q at %d, %v; want \"two\" at 512", second, r.RecordOffset(), err)
	}

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last record: %v, want io.EOF", err)
	}
}
