package rms

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// sharedFixture is an initialized volume with one variable-length record
// file, LOG.DAT;1, holding records.
type sharedFixture struct {
	vol *volume.Volume
	fid ondisk.Fid
}

func newSharedFixture(t *testing.T, records ...string) sharedFixture {
	t.Helper()

	c, err := diskimage.Create(filepath.Join(t.TempDir(), "SHARED.dsk"), 2000)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if err := volume.Initialize(c, volume.InitializeOptions{Label: "SHARED"}); err != nil {
		t.Fatal(err)
	}

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatal(err)
	}

	dev := vol.Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatal(err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatal(err)
	}

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatal(err)
	}

	f, err := vol.CreateFile(mfd, "LOG.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatVariable}, bm, ib)
	if err != nil {
		t.Fatal(err)
	}

	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range records {
		if err := w.Put([]byte(r)); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return sharedFixture{vol: vol, fid: f.Header.Fid}
}

// access accesses the fixture's file with mode.
func (fx sharedFixture) access(t *testing.T, mode volume.AccessMode) *volume.Access {
	t.Helper()

	a, err := fx.vol.Access(fx.fid, mode)
	if err != nil {
		t.Fatal(err)
	}

	return a
}

// all reads every record of the file, from the disk.
func (fx sharedFixture) all(t *testing.T) []string {
	t.Helper()

	f, err := fx.vol.OpenFID(fx.fid)
	if err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	var out []string

	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}

		if err != nil {
			t.Fatalf("after %d records: %v", len(out), err)
		}

		out = append(out, string(rec))
	}
}

func put(t *testing.T, w *Writer, records ...string) {
	t.Helper()

	for _, r := range records {
		if err := w.Put([]byte(r)); err != nil {
			t.Fatalf("Put(%q): %v", r, err)
		}
	}
}

func wantAll(t *testing.T, got []string, want ...string) {
	t.Helper()

	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("records %q\nwant    %q", got, want)
	}
}

// TestAppender: an appender adds records after the existing ones, in the
// same partly used block, and closes with the end of file past them.
func TestAppender(t *testing.T) {
	fx := newSharedFixture(t, "one", "two")

	f, err := fx.vol.OpenFID(fx.fid)
	if err != nil {
		t.Fatal(err)
	}

	dev := f.Device
	bm, _ := dev.Bitmap()
	ib, _ := dev.IndexBitmap()

	if err := f.OpenForWrite(bm, ib); err != nil {
		t.Fatal(err)
	}

	w, err := NewAppender(f)
	if err != nil {
		t.Fatal(err)
	}

	put(t, w, "three")

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	wantAll(t, fx.all(t), "one", "two", "three")
}

// TestSharedWriters: two shared writers on one accessed file interleave
// their records, each going after the other's last; enough records to
// cross several blocks and extend the file.
func TestSharedWriters(t *testing.T) {
	fx := newSharedFixture(t, "R0")
	a := fx.access(t, volume.AccessMode{Write: true})
	b := fx.access(t, volume.AccessMode{Write: true})

	wa, err := NewAppender(a.File)
	if err != nil {
		t.Fatal(err)
	}

	wb, err := NewAppender(b.File)
	if err != nil {
		t.Fatal(err)
	}

	wa.SetShared(true)
	wb.SetShared(true)

	want := []string{"R0"}

	for i := range 30 {
		ra := fmt.Sprintf("A%02d%s", i, strings.Repeat("a", 60))
		rb := fmt.Sprintf("B%02d%s", i, strings.Repeat("b", 41))
		put(t, wa, ra)
		put(t, wb, rb)
		want = append(want, ra, rb)
	}

	if err := wa.Close(); err != nil {
		t.Fatal(err)
	}

	if err := a.Deaccess(); err != nil {
		t.Fatal(err)
	}

	put(t, wb, "last")
	want = append(want, "last")

	if err := wb.Close(); err != nil {
		t.Fatal(err)
	}

	if err := b.Deaccess(); err != nil {
		t.Fatal(err)
	}

	wantAll(t, fx.all(t), want...)
}

// TestReaderSeesAppends: a reader at the end of a shared file reads what
// a shared writer appends afterward, including records added to the
// block that was the reader's last.
func TestReaderSeesAppends(t *testing.T) {
	fx := newSharedFixture(t, "R0")
	ra := fx.access(t, volume.AccessMode{})
	wa := fx.access(t, volume.AccessMode{Write: true})

	r, err := NewReader(ra.File)
	if err != nil {
		t.Fatal(err)
	}

	next := func() string {
		t.Helper()

		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			return "EOF"
		}

		if err != nil {
			t.Fatal(err)
		}

		return string(rec)
	}

	if got := next(); got != "R0" {
		t.Fatalf("first record %q", got)
	}

	if got := next(); got != "EOF" {
		t.Fatalf("second read %q, want EOF", got)
	}

	w, err := NewAppender(wa.File)
	if err != nil {
		t.Fatal(err)
	}

	w.SetShared(true)
	put(t, w, "R1")

	if got := next(); got != "R1" {
		t.Errorf("after an append in the same block: %q", got)
	}

	long := strings.Repeat("x", 700)
	put(t, w, long, "R3")

	if got := next(); got != long {
		t.Errorf("a record across blocks: %d bytes", len(got))
	}

	if got := next(); got != "R3" {
		t.Errorf("then %q", got)
	}

	if got := next(); got != "EOF" {
		t.Errorf("then %q, want EOF", got)
	}

	_ = w.Close()
	_ = wa.Deaccess()
	_ = ra.Deaccess()
}

// TestFlush: Flush puts the partial block on the disk and moves the end
// of file, without closing; WriteAttributes then makes the header
// match, so a file read from the disk alone has the records.
func TestFlush(t *testing.T) {
	fx := newSharedFixture(t)
	a := fx.access(t, volume.AccessMode{Write: true, NoWrite: true, NoRead: true})

	w, err := NewAppender(a.File)
	if err != nil {
		t.Fatal(err)
	}

	put(t, w, "kept")

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	if err := a.File.WriteAttributes(); err != nil {
		t.Fatal(err)
	}

	// Read the header and data from the disk, as another program
	// mounting the volume would, without the shared File.
	h, err := ondiskHeader(fx.vol, fx.fid)
	if err != nil {
		t.Fatal(err)
	}

	if got := FileByteLength(h.RecordAttributes); got != 6 {
		t.Errorf("on-disk length %d, want 6 (a word of length and four bytes)", got)
	}

	put(t, w, "more")
	_ = w.Close()
	_ = a.Deaccess()

	wantAll(t, fx.all(t), "kept", "more")
}

// ondiskHeader reads fid's header as the disk has it, past any shared
// File: from a second mount of the same container.
func ondiskHeader(vol *volume.Volume, fid ondisk.Fid) (ondisk.FileHeader, error) {
	other, err := volume.Mount(vol.Devices[0].Container)
	if err != nil {
		return ondisk.FileHeader{}, err
	}

	f, err := other.OpenFID(fid)
	if err != nil {
		return ondisk.FileHeader{}, err
	}

	return f.Header, nil
}

// TestRecordOffsets: Reader and Writer report where each record starts,
// its length word included; an odd-length Variable record's pad byte
// goes before the next one's start.
func TestRecordOffsets(t *testing.T) {
	fx := newSharedFixture(t)
	a := fx.access(t, volume.AccessMode{Write: true})

	w, err := NewAppender(a.File)
	if err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("y", 600)
	want := []int64{0, 6, 16, 618}

	for i, r := range []string{"abc", "defghijk", long, "z"} {
		put(t, w, r)

		if got := w.RecordOffset(); got != want[i] {
			t.Errorf("Put %d: offset %d, want %d", i, got, want[i])
		}
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(a.File)
	if err != nil {
		t.Fatal(err)
	}

	for i := range want {
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}

		if got := r.RecordOffset(); got != want[i] {
			t.Errorf("Next %d: offset %d, want %d", i, got, want[i])
		}
	}

	_ = a.Deaccess()
}
