package rms

import (
	"io"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// blockStream reads a volume.File's data as one continuous byte stream,
// ignoring the underlying 512-byte block boundaries. This is necessary
// because RMS record framing (a VAR/VFC record's length prefix, or a
// stream file's delimiter-terminated lines) doesn't respect block
// boundaries — a single record is free to start near the end of one block
// and finish in the next. (ODS-2 does define a "no span" record attribute
// bit for files where records are guaranteed not to cross a boundary, but
// nothing in this project currently needs to take advantage of that as an
// optimization; reading everything as a plain stream is correct either
// way.)
//
// It also knows the file's exact valid length in bytes — which is
// generally NOT the same as its allocated size in whole blocks, since the
// last allocated block is typically only partly used — so that reading
// never returns trailing zero-padding left over in that last block as if
// it were real data.
type blockStream struct {
	file  *volume.File
	limit int64 // total valid bytes in the file

	nextVBN uint32 // next virtual block to fetch from file, when buf runs low
	buf     []byte // bytes fetched but not yet consumed
	bufOff  int    // read position within buf
	fetched int64  // total bytes fetched from file so far (consumed + still buffered)
}

// newBlockStream creates a blockStream over f, treating it as having
// exactly limit valid bytes regardless of how many whole blocks it
// occupies.
func newBlockStream(f *volume.File, limit int64) *blockStream {
	return &blockStream{file: f, limit: limit, nextVBN: 1}
}

// fill ensures at least n unread bytes are available in buf (or as many
// as the file actually has left, if fewer than n remain), fetching
// further blocks from the underlying file as needed. It returns io.EOF
// once the file's exact byte limit has been reached, even if that leaves
// fewer than n bytes available — callers distinguish "nothing at all
// left" from "a truncated trailing record" by checking how much ended up
// available after fill returns an error (see ReadFull).
func (s *blockStream) fill(n int) error {
	for len(s.buf)-s.bufOff < n {
		remainingInFile := s.limit - s.fetched
		if remainingInFile <= 0 {
			return io.EOF
		}

		block := make([]byte, ondisk.BlockSize)
		if err := s.file.ReadBlock(s.nextVBN, block); err != nil {
			return err
		}

		s.nextVBN++

		if int64(len(block)) > remainingInFile {
			// This is the file's last block, and only part of it holds
			// real data — the rest is unused allocated space, not
			// something a reader should ever see.
			block = block[:remainingInFile]
		}

		s.fetched += int64(len(block))
		unread := s.buf[s.bufOff:]
		s.buf = append(append([]byte{}, unread...), block...)
		s.bufOff = 0
	}

	return nil
}

// ReadByte reads and consumes the next single byte, returning io.EOF once
// the file's exact byte limit has been reached.
func (s *blockStream) ReadByte() (byte, error) {
	if err := s.fill(1); err != nil {
		return 0, err
	}

	b := s.buf[s.bufOff]
	s.bufOff++

	return b, nil
}

// ReadFull reads and consumes exactly n bytes. It returns io.EOF if zero
// bytes were available at all (a clean end of file — no record was
// started), or io.ErrUnexpectedEOF if between 1 and n-1 bytes were
// available (the file ends mid-record: truncated or corrupt data, not a
// normal end of file).
func (s *blockStream) ReadFull(n int) ([]byte, error) {
	fillErr := s.fill(n)
	available := len(s.buf) - s.bufOff

	if fillErr != nil {
		if available == 0 {
			return nil, io.EOF
		}

		return nil, io.ErrUnexpectedEOF
	}

	out := make([]byte, n)
	copy(out, s.buf[s.bufOff:s.bufOff+n])
	s.bufOff += n

	return out, nil
}
