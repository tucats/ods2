package rms

import (
	"bytes"
	"io"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

func TestBlockStreamReadByte(t *testing.T) {
	data := []byte("HELLO")
	f := newTestFile(t, odstest.FileHeaderFixture{}, data)
	s := newBlockStream(f, int64(len(data)))

	for i, want := range data {
		got, err := s.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte() #%d: %v", i, err)
		}

		if got != want {
			t.Fatalf("ReadByte() #%d = %q, want %q", i, got, want)
		}
	}

	if _, err := s.ReadByte(); err != io.EOF {
		t.Fatalf("ReadByte() past the end: err = %v, want io.EOF", err)
	}
}

func TestBlockStreamReadFull(t *testing.T) {
	data := []byte("HELLO WORLD")
	f := newTestFile(t, odstest.FileHeaderFixture{}, data)
	s := newBlockStream(f, int64(len(data)))

	got, err := s.ReadFull(5)
	if err != nil {
		t.Fatalf("ReadFull(5): %v", err)
	}

	if !bytes.Equal(got, []byte("HELLO")) {
		t.Fatalf("ReadFull(5) = %q, want %q", got, "HELLO")
	}

	got, err = s.ReadFull(6)
	if err != nil {
		t.Fatalf("ReadFull(6): %v", err)
	}

	if !bytes.Equal(got, []byte(" WORLD")) {
		t.Fatalf("ReadFull(6) = %q, want %q", got, " WORLD")
	}
}

func TestBlockStreamReadFullAcrossBlockBoundary(t *testing.T) {
	// A record that starts a few bytes before the end of the first block
	// and finishes in the second, to confirm blockStream stitches blocks
	// together transparently.
	data := make([]byte, ondisk.BlockSize+20)
	for i := range data {
		data[i] = byte(i % 256)
	}

	f := newTestFile(t, odstest.FileHeaderFixture{}, data)
	s := newBlockStream(f, int64(len(data)))

	if _, err := s.ReadFull(ondisk.BlockSize - 10); err != nil {
		t.Fatalf("ReadFull(BlockSize-10): %v", err)
	}

	got, err := s.ReadFull(30) // 10 bytes from block 1, 20 from block 2
	if err != nil {
		t.Fatalf("ReadFull(30) spanning the block boundary: %v", err)
	}

	want := data[ondisk.BlockSize-10 : ondisk.BlockSize+20]
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFull(30) spanning the block boundary = %v, want %v", got, want)
	}
}

func TestBlockStreamStopsAtLimitNotBlockSize(t *testing.T) {
	// The file's on-disk block is a full 512 bytes, but the file's actual
	// valid content is much shorter; blockStream must never expose the
	// leftover padding as if it were real data.
	block := make([]byte, ondisk.BlockSize)
	copy(block, "SHORT")

	for i := 5; i < len(block); i++ {
		block[i] = 0xFF // deliberately non-zero "garbage" padding
	}

	f := newTestFile(t, odstest.FileHeaderFixture{}, block)
	s := newBlockStream(f, 5) // limit says only 5 bytes are valid

	got, err := s.ReadFull(5)
	if err != nil {
		t.Fatalf("ReadFull(5): %v", err)
	}

	if !bytes.Equal(got, []byte("SHORT")) {
		t.Fatalf("ReadFull(5) = %q, want %q", got, "SHORT")
	}

	if _, err := s.ReadByte(); err != io.EOF {
		t.Fatalf("ReadByte() past the limit: err = %v, want io.EOF (not the padding garbage)", err)
	}
}

func TestBlockStreamReadFullCleanEOF(t *testing.T) {
	data := []byte("ABC")
	f := newTestFile(t, odstest.FileHeaderFixture{}, data)
	s := newBlockStream(f, int64(len(data)))

	if _, err := s.ReadFull(3); err != nil {
		t.Fatalf("ReadFull(3): %v", err)
	}

	// Nothing at all is left: a clean io.EOF, not ErrUnexpectedEOF.
	if _, err := s.ReadFull(2); err != io.EOF {
		t.Fatalf("ReadFull past a clean end: err = %v, want io.EOF", err)
	}
}

func TestBlockStreamReadFullUnexpectedEOF(t *testing.T) {
	data := []byte("ABC")
	f := newTestFile(t, odstest.FileHeaderFixture{}, data)
	s := newBlockStream(f, int64(len(data)))

	// Only 3 bytes exist; asking for 5 leaves a partial (1-4 byte) read
	// dangling, which is a truncated/corrupt record, not a clean EOF.
	if _, err := s.ReadFull(5); err != io.ErrUnexpectedEOF {
		t.Fatalf("ReadFull(5) with only 3 bytes available: err = %v, want io.ErrUnexpectedEOF", err)
	}
}
