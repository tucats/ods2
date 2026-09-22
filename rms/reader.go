package rms

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// ErrCorruptRecord is returned by Reader.Next when a record's on-disk
// framing is self-inconsistent — e.g. a VAR/VFC record's length prefix
// claims more bytes than the file actually has left, or a STREAM (CRLF)
// file has a lone '\r' with no following '\n'. This means the file's data
// is truncated or corrupted, which is a different situation from a
// successful read simply running out of records: that case returns
// io.EOF instead, with no error wrapped in it.
//
// Callers that want to recover as much of a corrupt file as possible
// (mirroring the reference implementation's own "switch to raw/binary
// mode and keep going" behavior) can catch this with errors.Is and fall
// back to reading the file's remaining data directly via volume.File.
// ReadBlock; Reader itself doesn't do this automatically, since that
// recovery policy — and how to report it to a user — belongs with the
// caller (ultimately, the `copy` command), not with this package.
var ErrCorruptRecord = errors.New("rms: corrupt record")

// Reader reads successive records from an open volume.File, dispatching
// on the file's record format (ondisk.RecordFormat) the way VMS's RMS
// $GET service would: Fixed records are constant-size chunks with no
// framing at all; Variable and VFC records are prefixed with a 2-byte
// length; the Stream formats have no length prefix and are instead
// delimited by scanning for a line-ending byte sequence, like an ordinary
// Unix or Windows text file.
type Reader struct {
	stream *blockStream
	format ondisk.RecordFormat

	fixedSize int // for RecordFormatFixed/Undefined: the size of every record
}

// NewReader creates a Reader over f's contents, starting at its first
// record.
func NewReader(f *volume.File) (*Reader, error) {
	attr := f.Header.RecordAttributes

	switch attr.Format {
	case ondisk.RecordFormatFixed, ondisk.RecordFormatUndefined,
		ondisk.RecordFormatVariable, ondisk.RecordFormatVFC,
		ondisk.RecordFormatStreamCRLF, ondisk.RecordFormatStreamLF, ondisk.RecordFormatStreamCR:
		// recognized

	default:
		return nil, fmt.Errorf("rms: unsupported record format %v", attr.Format)
	}

	return &Reader{
		stream:    newBlockStream(f, FileByteLength(attr)),
		format:    attr.Format,
		fixedSize: int(attr.MaxRecordSize),
	}, nil
}

// FileByteLength computes a file's exact valid length in bytes from its
// record attributes. EndOfFileBlock is the (1-based) virtual block number
// of the last block containing real data, and FirstFreeByte is how far
// into that block the real data extends; a file with EndOfFileBlock 0 has
// no data at all. This is generally shorter than the file's full
// allocated size in whole blocks (volume.File.Blocks()), since the last
// allocated block is typically only partly used.
//
// Exported for callers that need a file's exact byte length without
// going through per-format record parsing at all — a raw/binary copy
// mode, for instance, which reads a file's bytes directly via
// volume.File.ReadBlock rather than via this package's Reader.
func FileByteLength(attr ondisk.RecAttr) int64 {
	if attr.EndOfFileBlock == 0 {
		return 0
	}

	return int64(attr.EndOfFileBlock-1)*ondisk.BlockSize + int64(attr.FirstFreeByte)
}

// Next reads and returns the next record's raw bytes. For VFC records,
// this includes the record's leading carriage-control bytes (see
// FormatVFCRecord to interpret them) — the number of such bytes is given
// by the file's RecordAttributes.VfcSize.
//
// Next returns io.EOF, with no other error, once every record has been
// read; see ErrCorruptRecord for how a truncated or corrupt trailing
// record is reported instead.
func (r *Reader) Next() ([]byte, error) {
	switch r.format {
	case ondisk.RecordFormatFixed, ondisk.RecordFormatUndefined:
		return r.nextFixed()
	case ondisk.RecordFormatVariable, ondisk.RecordFormatVFC:
		return r.nextVariable()
	case ondisk.RecordFormatStreamLF:
		return r.nextStream(streamDelimLF)
	case ondisk.RecordFormatStreamCR:
		return r.nextStream(streamDelimCR)
	case ondisk.RecordFormatStreamCRLF:
		return r.nextStream(streamDelimCRLF)
	default:
		// Unreachable: NewReader already rejected any other format.
		return nil, fmt.Errorf("rms: unsupported record format %v", r.format)
	}
}

func (r *Reader) nextFixed() ([]byte, error) {
	if r.fixedSize <= 0 {
		// A zero (or nonsensical negative) fixed record size can never
		// be satisfied by a real record, but blockStream.ReadFull(0)
		// trivially "succeeds" with an empty read every time — with no
		// way to ever detect end of file, that would make Next() loop
		// forever producing empty records instead of terminating. This
		// happens in practice for a file whose header was never fully
		// populated (RecordAttributes.MaxRecordSize left at its zero
		// value), which in turn means it has no data to read anyway.
		return nil, io.EOF
	}

	data, err := r.stream.ReadFull(r.fixedSize)
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}

		return nil, fmt.Errorf("%w: fixed-length record: %v", ErrCorruptRecord, err)
	}

	return data, nil
}

// nextVariable reads one Variable or VFC record: a 2-byte little-endian
// length, that many bytes of data, and — if the length is odd — one
// filler byte to keep the next record's length prefix on an even offset
// (the same padding-to-even convention ondisk's directory records use).
// VFC's leading carriage-control bytes are simply part of the record's
// declared length, from this function's point of view; splitting them
// out is left to the caller.
func (r *Reader) nextVariable() ([]byte, error) {
	lengthBytes, err := r.stream.ReadFull(2)
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}

		return nil, fmt.Errorf("%w: record length prefix: %v", ErrCorruptRecord, err)
	}

	length := int(binary.LittleEndian.Uint16(lengthBytes))

	data, err := r.stream.ReadFull(length)
	if err != nil {
		return nil, fmt.Errorf("%w: record body (declared length %d): %v", ErrCorruptRecord, length, err)
	}

	if length%2 != 0 {
		if _, err := r.stream.ReadFull(1); err != nil {
			return nil, fmt.Errorf("%w: alignment padding byte: %v", ErrCorruptRecord, err)
		}
	}

	return data, nil
}

// streamDelim identifies which byte sequence terminates a record in one
// of the three Stream record formats.
type streamDelim int

const (
	streamDelimLF   streamDelim = iota // RecordFormatStreamLF: '\n'
	streamDelimCR                      // RecordFormatStreamCR: '\r'
	streamDelimCRLF                    // RecordFormatStreamCRLF ("STREAM"): '\r' '\n'
)

// nextStream reads one record from a Stream-format file: bytes up to (but
// not including) the next delimiter. Unlike Variable/VFC, there is no
// length prefix at all — a stream file is just ordinary delimited text,
// the same shape as a Unix or Windows text file.
func (r *Reader) nextStream(kind streamDelim) ([]byte, error) {
	var record []byte

	for {
		b, err := r.stream.ReadByte()
		if err != nil {
			if err == io.EOF {
				if len(record) == 0 {
					return nil, io.EOF
				}
				// The file's last record has no trailing delimiter.
				// That's normal for a stream file — nothing requires a
				// final line ending — not corruption.
				return record, nil
			}

			return nil, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
		}

		switch kind {
		case streamDelimLF:
			if b == '\n' {
				return record, nil
			}
		case streamDelimCR:
			if b == '\r' {
				return record, nil
			}
		case streamDelimCRLF:
			if b == '\r' {
				next, err := r.stream.ReadByte()
				if err != nil || next != '\n' {
					return nil, fmt.Errorf("%w: '\\r' not followed by '\\n' in a STREAM file", ErrCorruptRecord)
				}

				return record, nil
			}
		}

		record = append(record, b)
	}
}
