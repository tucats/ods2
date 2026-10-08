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
// It also knows where the file's valid data ends, which is generally NOT
// the end of its allocated blocks, since the last allocated block is
// typically only partly used — so that reading never returns trailing
// zero-padding left over in that last block as if it were real data. It
// asks the file each time it reaches that end, rather than once: a file
// shared with a writer (volume.Access) can grow while it's being read,
// and the reader then sees what was appended, even within the block that
// was its last.
type blockStream struct {
	file  *volume.File
	limit func() int64 // the file's valid length in bytes, now

	buf     []byte // bytes fetched but not yet consumed
	bufOff  int    // read position within buf
	fetched int64  // total bytes fetched from file so far (consumed + still buffered)
}

// newBlockStream creates a blockStream over f, whose valid length limit
// reports, reading from its first byte.
func newBlockStream(f *volume.File, limit func() int64) *blockStream {
	return &blockStream{file: f, limit: limit}
}

// fill makes sure at least n unconsumed bytes are buffered, fetching more
// blocks from the file as needed (returning io.EOF if the file runs out
// first). Each fetch reads the block holding the next unfetched byte and
// takes from that byte on, so a block fetched while it was the file's
// partly used last block is read again, from where the data ended, once
// the file has grown.
func (s *blockStream) fill(n int) error {
	for len(s.buf)-s.bufOff < n {
		remainingInFile := s.limit() - s.fetched
		if remainingInFile <= 0 {
			return io.EOF
		}

		block := make([]byte, ondisk.BlockSize)
		vbn := uint32(s.fetched/ondisk.BlockSize) + 1

		if err := s.file.ReadBlock(vbn, block); err != nil {
			return err
		}

		block = block[s.fetched%ondisk.BlockSize:]
		if int64(len(block)) > remainingInFile {
			block = block[:remainingInFile]
		}

		s.fetched += int64(len(block))
		unread := s.buf[s.bufOff:]
		s.buf = append(append([]byte{}, unread...), block...)
		s.bufOff = 0
	}

	return nil
}

// offset is the byte offset in the file of the next byte to be read.
func (s *blockStream) offset() int64 {
	return s.fetched - int64(len(s.buf)-s.bufOff)
}

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
