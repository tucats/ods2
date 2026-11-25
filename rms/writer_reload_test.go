package rms

import (
	"bytes"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

// TestWriterReload: a record rewritten in place in the block the Writer holds
// stays rewritten after Reload and Close; without Reload, Close would put the
// Writer's copy of the block back.
func TestWriterReload(t *testing.T) {
	f := newWritableTestFile(t, newWritableTestVolume(t), "RELOAD.DAT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed, MaxRecordSize: 4})

	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range []string{"AAAA", "BBBB"} {
		if err := w.Put([]byte(r)); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	block := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(1, block); err != nil {
		t.Fatal(err)
	}

	copy(block, "NEW!")

	if err := f.WriteBlock(1, block); err != nil {
		t.Fatal(err)
	}

	if err := w.Reload(); err != nil {
		t.Fatal(err)
	}

	if err := w.Put([]byte("CCCC")); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if err := f.ReadBlock(1, block); err != nil {
		t.Fatal(err)
	}

	if want := []byte("NEW!BBBBCCCC"); !bytes.Equal(block[:12], want) {
		t.Errorf("block 1 = %q, want %q", block[:12], want)
	}
}
