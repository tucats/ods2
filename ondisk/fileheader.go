package ondisk

import (
	"encoding/binary"
	"fmt"
)

// File characteristic bit flags, found in FileHeader.FileCharacteristics.
const (
	FchNoBackup  uint32 = 0x2     // exclude this file from routine backups
	FchContigB   uint32 = 0x20    // "contiguous-best-try": prefer contiguous allocation, but don't require it
	FchLocked    uint32 = 0x40    // file is locked against deletion
	FchContig    uint32 = 0x80    // file's data is guaranteed to occupy one contiguous run of blocks
	FchDirectory uint32 = 0x2000  // this file IS a directory (its data is a sequence of directory records, not arbitrary user data)
	FchMarkDel   uint32 = 0x8000  // file is marked for deletion (its space can be reclaimed once nothing has it open)
	FchErase     uint32 = 0x20000 // overwrite the file's data with a fixed pattern before releasing its blocks
)

// FileHeaderSize is the number of bytes a FileHeader occupies on disk.
const FileHeaderSize = BlockSize

// FileHeader describes one file (or directory, or one of a volume's own
// bookkeeping files — on ODS-2 those are all just files with particular
// characteristics) via a single 512-byte block within the volume's index
// file, INDEXF.SYS. It's the ODS-2 equivalent of a Unix inode: it doesn't
// contain the file's actual data, but it does contain everything needed to
// FIND that data — most importantly the retrieval pointers (see
// RetrievalPointers) that map the file's virtual blocks onto logical
// blocks on the device.
//
// A single FileHeader has limited room for retrieval pointers. A file
// large enough, or fragmented enough, to need more can spill into
// additional "extension header" segments, chained together via
// ExtensionFid; see RetrievalPointers' documentation for how a caller is
// expected to walk that chain (this package only decodes one segment at a
// time — following the chain is package volume's job, since it requires
// reading additional blocks from the index file).
type FileHeader struct {
	// IdentOffset, MapOffset, and AclOffset are WORD offsets (i.e.
	// multiply by 2 to get a byte offset) from the start of this header
	// to three variable-position areas that follow the fixed fields
	// decoded into this struct: the file identification area (name,
	// dates), the retrieval-pointer map area, and the access control list
	// area. Their positions vary from file to file because those areas
	// are variable-length, so nothing after the fixed portion of a header
	// has a constant offset — you always have to consult these fields
	// first. EndOffset marks the end of the header's meaningfully-used
	// data (everything past it, up to the checksum, is unused filler).
	IdentOffset uint8
	MapOffset   uint8
	AclOffset   uint8
	EndOffset   uint8

	// SegmentNumber is 0 for a file's primary header, and 1, 2, 3, ... for
	// each successive extension segment reached by following ExtensionFid.
	SegmentNumber  uint16
	StructureLevel uint16

	// Fid is this header's own file ID. A caller who looked this header
	// up by a specific Fid should compare it against this field (and
	// against Seq in particular) to confirm the header slot wasn't
	// reused by a different file since the Fid was obtained — this
	// package doesn't perform that check itself, since it requires
	// knowing what Fid the caller was looking for.
	Fid Fid

	// ExtensionFid is the Fid of the next header segment in the chain, or
	// the zero Fid (see Fid.IsZero) if this is the last (or only) segment.
	ExtensionFid Fid

	RecordAttributes RecAttr

	// FileCharacteristics is a bitmask of the Fch* constants above —
	// notably FchDirectory, which is how a directory is distinguished
	// from an ordinary file.
	FileCharacteristics uint32

	// MapWordsInUse is how many 16-bit words, starting at MapOffset, are
	// actually populated with retrieval-pointer data (as opposed to
	// unused filler past the end of the map).
	MapWordsInUse uint8
	AccessMode    uint8

	Owner          Uic
	FileProtection uint16

	// Backlink is the Fid of this file's parent directory — the reverse
	// of the (directory -> file) link a directory entry records.
	Backlink Fid

	Journaling         uint8
	RecoveryUnitActive uint8

	// HighWaterMark is the virtual block number below which the file is
	// guaranteed to contain previously-written data; blocks at or beyond
	// it have been allocated but never written, and reading them should
	// produce zeros rather than whatever leftover data happens to occupy
	// those blocks on the physical device (which could belong to a
	// different, previously-deleted file). Package volume is responsible
	// for enforcing this when reading file data.
	HighWaterMark uint32

	ClassProtection [20]byte

	// Checksum is the checksum of the whole 512-byte header (see
	// Checksum), computed over everything except this field itself.
	Checksum uint16

	// raw retains the entire on-disk header, because the variable-position
	// IDENT/map/ACL areas described by IdentOffset/MapOffset/AclOffset
	// above can only be located and decoded with the full block in hand.
	// RetrievalPointers uses this; nothing outside this package needs to.
	raw [FileHeaderSize]byte
}

// Byte offsets of each FileHeader field within its 512-byte on-disk block.
const (
	fhOffIdOffset   = 0
	fhOffMpOffset   = 1
	fhOffAcOffset   = 2
	fhOffRsOffset   = 3
	fhOffSegNum     = 4
	fhOffStrucLevel = 6
	fhOffFid        = 8  // 6 bytes
	fhOffExtFid     = 14 // 6 bytes
	fhOffRecAttr    = 20 // 32 bytes
	fhOffFileChar   = 52
	// 2 reserved bytes at offset 56
	fhOffMapInUse  = 58
	fhOffAccMode   = 59
	fhOffFileOwner = 60 // 4 bytes (Uic)
	fhOffFileProt  = 64
	fhOffBacklink  = 66 // 6 bytes
	fhOffJournal   = 72
	fhOffRuActive  = 73
	// 2 reserved bytes at offset 74
	fhOffHighwater = 76
	// 8 reserved bytes at offset 80
	fhOffClassProt = 88 // 20 bytes
	// 402 bytes of IDENT/map/ACL area at offset 108, decoded elsewhere
	fhOffChecksum = 510
)

// IsDirectory reports whether this header describes a directory rather
// than an ordinary file. On ODS-2, a directory's contents are a sequence
// of directory records (see package volume) rather than arbitrary data,
// but is otherwise a file like any other — same header structure, same
// retrieval-pointer mapping.
func (h FileHeader) IsDirectory() bool {
	return h.FileCharacteristics&FchDirectory != 0
}

// IsMarkedForDeletion reports whether this file has been deleted but its
// space not yet reclaimed (typically because something still has it open).
func (h FileHeader) IsMarkedForDeletion() bool {
	return h.FileCharacteristics&FchMarkDel != 0
}

// DecodeFileHeader decodes a FileHeader from its 512-byte on-disk
// representation and validates its checksum. It does not check that Fid
// matches any particular expected value — package volume performs that
// check, since only it knows which Fid it was looking up.
//
// The decoded FileHeader is returned even when the checksum check fails,
// in case a caller wants to inspect it anyway; check the returned error to
// know whether it's trustworthy.
func DecodeFileHeader(b []byte) (FileHeader, error) {
	if len(b) != FileHeaderSize {
		return FileHeader{}, fmt.Errorf("ondisk: FileHeader requires exactly %d bytes, got %d", FileHeaderSize, len(b))
	}

	fid, err := DecodeFid(b[fhOffFid:])
	if err != nil {
		return FileHeader{}, fmt.Errorf("ondisk: decoding FileHeader.Fid: %w", err)
	}
	extFid, err := DecodeFid(b[fhOffExtFid:])
	if err != nil {
		return FileHeader{}, fmt.Errorf("ondisk: decoding FileHeader.ExtensionFid: %w", err)
	}
	recAttr, err := DecodeRecAttr(b[fhOffRecAttr:])
	if err != nil {
		return FileHeader{}, fmt.Errorf("ondisk: decoding FileHeader.RecordAttributes: %w", err)
	}
	owner, err := DecodeUic(b[fhOffFileOwner:])
	if err != nil {
		return FileHeader{}, fmt.Errorf("ondisk: decoding FileHeader.Owner: %w", err)
	}
	backlink, err := DecodeFid(b[fhOffBacklink:])
	if err != nil {
		return FileHeader{}, fmt.Errorf("ondisk: decoding FileHeader.Backlink: %w", err)
	}

	h := FileHeader{
		IdentOffset:         b[fhOffIdOffset],
		MapOffset:           b[fhOffMpOffset],
		AclOffset:           b[fhOffAcOffset],
		EndOffset:           b[fhOffRsOffset],
		SegmentNumber:       binary.LittleEndian.Uint16(b[fhOffSegNum:]),
		StructureLevel:      binary.LittleEndian.Uint16(b[fhOffStrucLevel:]),
		Fid:                 fid,
		ExtensionFid:        extFid,
		RecordAttributes:    recAttr,
		FileCharacteristics: binary.LittleEndian.Uint32(b[fhOffFileChar:]),
		MapWordsInUse:       b[fhOffMapInUse],
		AccessMode:          b[fhOffAccMode],
		Owner:               owner,
		FileProtection:      binary.LittleEndian.Uint16(b[fhOffFileProt:]),
		Backlink:            backlink,
		Journaling:          b[fhOffJournal],
		RecoveryUnitActive:  b[fhOffRuActive],
		HighWaterMark:       binary.LittleEndian.Uint32(b[fhOffHighwater:]),
		Checksum:            binary.LittleEndian.Uint16(b[fhOffChecksum:]),
	}
	copy(h.ClassProtection[:], b[fhOffClassProt:fhOffClassProt+20])
	copy(h.raw[:], b)

	sum, err := Checksum(b)
	if err != nil {
		// Unreachable given the length check above.
		return h, err
	}
	if sum != h.Checksum {
		return h, fmt.Errorf("ondisk: file header checksum mismatch: computed %#04x, stored %#04x", sum, h.Checksum)
	}

	return h, nil
}
