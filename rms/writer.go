package rms

import (
	"encoding/binary"
	"fmt"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// Writer writes successive records to an open, writable volume.File — the
// write-side counterpart to Reader, dispatching on the file's record
// format (ondisk.RecordFormat) the same way Reader.Next() does: Fixed
// records are written as constant-size chunks with no framing; Variable
// and VFC records are prefixed with a 2-byte length; the Stream formats
// have no length prefix and are instead delimited by appending a
// line-ending byte sequence after each record, matching how nextStream
// reads them back.
//
// A Writer only ever appends: it has no notion of overwriting an existing
// record, and it assumes it's writing a brand-new file (or replacing an
// existing file's content from its very first byte) — the same assumption
// volume.File.OpenForWrite's own maxWrittenVBN seeding documents for the
// underlying block-level write path this builds on.
type Writer struct {
	file   *volume.File
	format ondisk.RecordFormat

	fixedSize int // for RecordFormatFixed/Undefined: the required size of every record

	// buf holds bytes that have been framed (length-prefixed, delimited,
	// ...) but not yet written to disk -- always fewer than
	// ondisk.BlockSize bytes, since append flushes a full block out to the
	// underlying File as soon as one accumulates.
	buf []byte

	// vbn is the next virtual block Writer will hand to volume.File.
	// WriteBlock once buf next fills a whole block.
	vbn uint32

	closed bool
}

// NewWriter creates a Writer that appends records to f, starting at f's
// first virtual block. f must already be armed for writing (via
// volume.File.OpenForWrite or volume.Volume.CreateFile) — Writer itself
// never arms a File, matching how volume.File.WriteBlock reports a clear
// error rather than panicking if that step was skipped.
func NewWriter(f *volume.File) (*Writer, error) {
	attr := f.Header.RecordAttributes

	switch attr.Format {
	case ondisk.RecordFormatFixed, ondisk.RecordFormatUndefined,
		ondisk.RecordFormatVariable, ondisk.RecordFormatVFC,
		ondisk.RecordFormatStreamCRLF, ondisk.RecordFormatStreamLF, ondisk.RecordFormatStreamCR:
		// recognized

	default:
		return nil, fmt.Errorf("rms: unsupported record format %v", attr.Format)
	}

	return &Writer{
		file:      f,
		format:    attr.Format,
		fixedSize: int(attr.MaxRecordSize),
		vbn:       1,
	}, nil
}

// Put writes one record. Its framing (or lack of it) depends entirely on
// the file's record format, exactly mirroring how Reader.Next() would read
// the same bytes back: for RecordFormatVFC, record must include its
// leading carriage-control bytes (see FormatVFCRecord), the same
// convention Reader.Next() documents for what it returns.
//
// Put returns an error, and leaves the file's on-disk content unspecified
// for records already written by earlier successful Put calls in this
// Writer, if called after Close.
func (w *Writer) Put(record []byte) error {
	if w.closed {
		return fmt.Errorf("rms: Put called on a Writer that has already been Closed")
	}

	switch w.format {
	case ondisk.RecordFormatFixed, ondisk.RecordFormatUndefined:
		return w.putFixed(record)
	case ondisk.RecordFormatVariable, ondisk.RecordFormatVFC:
		return w.putVariable(record)
	case ondisk.RecordFormatStreamLF:
		return w.putStream(record, streamDelimLF)
	case ondisk.RecordFormatStreamCR:
		return w.putStream(record, streamDelimCR)
	case ondisk.RecordFormatStreamCRLF:
		return w.putStream(record, streamDelimCRLF)
	default:
		// Unreachable: NewWriter already rejected any other format.
		return fmt.Errorf("rms: unsupported record format %v", w.format)
	}
}

// putFixed writes one Fixed/Undefined-format record: exactly fixedSize
// bytes, with no framing at all, matching how Reader.nextFixed reads a
// record back.
func (w *Writer) putFixed(record []byte) error {
	if w.fixedSize <= 0 {
		return fmt.Errorf("rms: file's record attributes don't define a usable fixed record size (MaxRecordSize %d)", w.fixedSize)
	}

	if len(record) != w.fixedSize {
		return fmt.Errorf("rms: fixed-format record must be exactly %d bytes, got %d", w.fixedSize, len(record))
	}

	return w.append(record)
}

// putVariable writes one Variable or VFC record: a 2-byte little-endian
// length, the record's bytes, and — if the length is odd — one filler byte
// to keep the next record's length prefix on an even offset, exactly the
// framing Reader.nextVariable expects. VFC's leading carriage-control
// bytes are simply part of record's length here, matching how
// Reader.Next() documents them as part of what it returns for that
// format.
func (w *Writer) putVariable(record []byte) error {
	if len(record) > 0xFFFF {
		return fmt.Errorf("rms: variable-format record too long: %d bytes exceeds the 2-byte length prefix's 65535-byte limit", len(record))
	}

	length := make([]byte, 2)
	binary.LittleEndian.PutUint16(length, uint16(len(record)))

	if err := w.append(length); err != nil {
		return err
	}

	if err := w.append(record); err != nil {
		return err
	}

	if len(record)%2 != 0 {
		return w.append([]byte{0})
	}

	return nil
}

// putStream writes one Stream-format record: record's bytes followed by
// kind's delimiter, with no length prefix — the same shape
// Reader.nextStream scans for. A delimiter is appended after every record,
// including the last one Put is ever called with; Reader.nextStream
// doesn't require a trailing delimiter on a file's last record, so this is
// simply the plainest way to write one, not something a reader depends on.
func (w *Writer) putStream(record []byte, kind streamDelim) error {
	if err := w.append(record); err != nil {
		return err
	}

	return w.append(streamDelimBytes(kind))
}

// streamDelimBytes is the byte sequence putStream appends after a Stream-
// format record, the write-side mirror of the byte(s) nextStream scans
// for.
func streamDelimBytes(kind streamDelim) []byte {
	switch kind {
	case streamDelimLF:
		return []byte{'\n'}
	case streamDelimCR:
		return []byte{'\r'}
	case streamDelimCRLF:
		return []byte{'\r', '\n'}
	default:
		// Unreachable: kind is always one of the three constants above,
		// all handled.
		return nil
	}
}

// append accumulates data into buf, flushing every full ondisk.BlockSize
// chunk out to the underlying File via WriteBlock as soon as one is
// available — the same "write straight through, no caching for file data"
// policy volume.File.WriteBlock itself documents. Any leftover partial
// block stays buffered until either a later append fills it out or Close
// pads and flushes it.
func (w *Writer) append(data []byte) error {
	w.buf = append(w.buf, data...)

	for len(w.buf) >= ondisk.BlockSize {
		if err := w.file.WriteBlock(w.vbn, w.buf[:ondisk.BlockSize]); err != nil {
			return fmt.Errorf("rms: writing virtual block %d: %w", w.vbn, err)
		}

		w.vbn++

		remaining := len(w.buf) - ondisk.BlockSize
		copy(w.buf, w.buf[ondisk.BlockSize:])
		w.buf = w.buf[:remaining]
	}

	return nil
}

// Close flushes any buffered-but-not-yet-written partial final block (zero-
// padded out to ondisk.BlockSize, the same padding a slack block anywhere
// else in this project's files carries) and finalizes the underlying
// File's RecordAttributes.EndOfFileBlock/FirstFreeByte to the Writer's
// exact byte length — via volume.File.CloseWithFinalByte, not File.Close's
// own whole-block convention, since a record's framing routinely ends
// partway through a block.
//
// Close is idempotent — calling it again after it has already run is a
// no-op — so callers can defer Close unconditionally.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}

	w.closed = true

	finalByte := len(w.buf)
	if finalByte > 0 {
		block := make([]byte, ondisk.BlockSize)
		copy(block, w.buf)

		if err := w.file.WriteBlock(w.vbn, block); err != nil {
			return fmt.Errorf("rms: writing final virtual block %d: %w", w.vbn, err)
		}
	}

	if err := w.file.CloseWithFinalByte(uint16(finalByte)); err != nil {
		return fmt.Errorf("rms: closing file %v: %w", w.file.Header.Fid, err)
	}

	return nil
}
