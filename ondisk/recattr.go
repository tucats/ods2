package ondisk

import (
	"encoding/binary"
	"fmt"
)

// RecAttrSize is the number of bytes a RecAttr occupies on disk.
const RecAttrSize = 32

// RecordFormat identifies how the records within a file are laid out on
// disk. VMS calls this a file's "record format" (RFM). It matters for
// reading a file's contents correctly: a text file and a binary file might
// both be "sequential", but the byte-level framing of one record versus
// the next differs completely between formats. Package rms implements the
// actual per-format reading logic; this package just decodes which format
// a file uses.
//
//   - Fixed: every record is exactly RecAttr.MaxRecordSize bytes, with no
//     length markers at all — you simply read fixed-size chunks.
//   - Variable: each record is preceded by a 2-byte little-endian length,
//     and padded with a filler byte if needed so the next record starts on
//     an even byte boundary.
//   - VFC ("Variable with Fixed Control"): like Variable, but each record
//     additionally starts with a small fixed number of "carriage control"
//     bytes (VfcSize, almost always 2) that describe how to lay the record
//     out as text (leading/trailing newlines, form feeds, and so on).
//   - The Stream formats (StreamCRLF, StreamLF, StreamCR) have no length
//     prefix at all; records are delimited by scanning for a line-ending
//     byte sequence, matching plain Unix/Windows text files.
type RecordFormat uint8

// The seven record formats ODS-2 files can use. These numeric values are
// significant: they're exactly the values stored on disk, matching the
// FAT$C_* constants in the original C implementation's header.h.
const (
	RecordFormatUndefined  RecordFormat = 0
	RecordFormatFixed      RecordFormat = 1
	RecordFormatVariable   RecordFormat = 2
	RecordFormatVFC        RecordFormat = 3
	RecordFormatStreamCRLF RecordFormat = 4 // "STREAM" in VMS terminology; CRLF-terminated
	RecordFormatStreamLF   RecordFormat = 5
	RecordFormatStreamCR   RecordFormat = 6
)

// String returns the format's conventional VMS abbreviation (as seen in a
// DIRECTORY/FULL listing), or a fallback like "RecordFormat(7)" for any
// value outside the defined range.
func (f RecordFormat) String() string {
	switch f {
	case RecordFormatUndefined:
		return "UNDEFINED"
	case RecordFormatFixed:
		return "FIXED"
	case RecordFormatVariable:
		return "VARIABLE"
	case RecordFormatVFC:
		return "VFC"
	case RecordFormatStreamCRLF:
		return "STREAM"
	case RecordFormatStreamLF:
		return "STREAMLF"
	case RecordFormatStreamCR:
		return "STREAMCR"
	default:
		return fmt.Sprintf("RecordFormat(%d)", uint8(f))
	}
}

// Record attribute bit flags, found in RecAttr.Attributes. These describe
// how a record's carriage control should be interpreted when the file is
// treated as text (see package rms for where these actually get used).
const (
	AttrFortranCC uint8 = 0x1  // first data byte is a legacy FORTRAN carriage-control character
	AttrImpliedCC uint8 = 0x2  // implied carriage control: a newline is implied before each record
	AttrPrintCC   uint8 = 0x4  // print file carriage control (form feed, etc.)
	AttrNoSpan    uint8 = 0x8  // records must not span across a "bucket" boundary
	AttrMsbRcw    uint8 = 0x10 // most-significant-bit-of-record-control-word variant (rarely seen)
)

// RecAttr ("record attributes") describes both the record structure of a
// file's contents (RecordFormat, MaxRecordSize, and so on — how to split
// the file's bytes into individual records) and, doing double duty in the
// original VMS RMS design, some whole-file bookkeeping like how many
// blocks the file currently occupies. It's embedded directly inside a
// FileHeader.
type RecAttr struct {
	Format     RecordFormat
	Attributes uint8
	RecordSize uint16 // typical/fixed record size, in bytes

	// HighestBlock is the highest virtual block number ever allocated to
	// this file, and EndOfFileBlock is the virtual block number containing
	// the last byte of actual data (EndOfFileBlock <= HighestBlock; the
	// file may have allocated, but not yet used, blocks beyond
	// EndOfFileBlock). Both fields use the VAX RMS "swapped longword"
	// on-disk encoding — see decodeSwappedLongword for why that's a
	// separate concern from ordinary little-endian byte order.
	HighestBlock   uint32
	EndOfFileBlock uint32

	FirstFreeByte     uint16 // offset, within the EndOfFileBlock, of the first byte past the file's actual data
	BucketSize        uint8  // bucket size in blocks, for indexed files (unused for the sequential files this project supports)
	VfcSize           uint8  // number of carriage-control bytes preceding each record, for RecordFormatVFC (almost always 2)
	MaxRecordSize     uint16 // maximum size, in bytes, of any one record
	DefaultExtend     uint16 // default number of blocks to grow the file by when it needs more space
	GlobalBufferCount uint16 // suggested RMS global buffer count (a caching hint; not needed by this read-only project)
	VersionLimit      uint16 // maximum number of versions of this file that may exist at once, 0 = unlimited
}

// DecodeRecAttr decodes a RecAttr from its 32-byte on-disk representation.
// b must be at least RecAttrSize bytes long; only the first RecAttrSize
// bytes are read. Eight reserved bytes between GlobalBufferCount and
// VersionLimit (originally used for indexed-file key definitions, which
// this project does not support) are skipped.
func DecodeRecAttr(b []byte) (RecAttr, error) {
	if len(b) < RecAttrSize {
		return RecAttr{}, fmt.Errorf("ondisk: RecAttr requires %d bytes, got %d", RecAttrSize, len(b))
	}
	return RecAttr{
		Format:            RecordFormat(b[0]),
		Attributes:        b[1],
		RecordSize:        binary.LittleEndian.Uint16(b[2:4]),
		HighestBlock:      decodeSwappedLongword(b[4:8]),
		EndOfFileBlock:    decodeSwappedLongword(b[8:12]),
		FirstFreeByte:     binary.LittleEndian.Uint16(b[12:14]),
		BucketSize:        b[14],
		VfcSize:           b[15],
		MaxRecordSize:     binary.LittleEndian.Uint16(b[16:18]),
		DefaultExtend:     binary.LittleEndian.Uint16(b[18:20]),
		GlobalBufferCount: binary.LittleEndian.Uint16(b[20:22]),
		// bytes [22:30) are reserved and intentionally skipped.
		VersionLimit: binary.LittleEndian.Uint16(b[30:32]),
	}, nil
}

// EncodeRecAttr encodes ra into its 32-byte on-disk representation, the
// exact inverse of DecodeRecAttr — including using the "swapped longword"
// convention (see decodeSwappedLongword) for HighestBlock and
// EndOfFileBlock. The 8 reserved bytes between GlobalBufferCount and
// VersionLimit are left zero.
func EncodeRecAttr(ra RecAttr) []byte {
	b := make([]byte, RecAttrSize)
	b[0] = byte(ra.Format)
	b[1] = ra.Attributes
	binary.LittleEndian.PutUint16(b[2:4], ra.RecordSize)
	encodeSwappedLongword(b[4:8], ra.HighestBlock)
	encodeSwappedLongword(b[8:12], ra.EndOfFileBlock)
	binary.LittleEndian.PutUint16(b[12:14], ra.FirstFreeByte)
	b[14] = ra.BucketSize
	b[15] = ra.VfcSize
	binary.LittleEndian.PutUint16(b[16:18], ra.MaxRecordSize)
	binary.LittleEndian.PutUint16(b[18:20], ra.DefaultExtend)
	binary.LittleEndian.PutUint16(b[20:22], ra.GlobalBufferCount)
	// bytes [22:30) are reserved and intentionally left zero.
	binary.LittleEndian.PutUint16(b[30:32], ra.VersionLimit)
	return b
}
