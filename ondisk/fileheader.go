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

// fhVariableAreaStart is the WORD offset (see IdentOffset's doc comment
// for why these are word, not byte, offsets) where a freshly-encoded
// header's variable-position IDENT/map/ACL areas begin: immediately after
// every fixed field this package decodes (ClassProtection, the last of
// them, ends at byte offset 108 — fhOffClassProt+20 — i.e. word offset
// 54). A header read from a real volume can have its areas start
// elsewhere (older on-disk layouts left less of the header fixed), but
// EncodeFileHeader has no reason to: it's free to always start exactly
// where its own fixed-field layout ends.
const fhVariableAreaStart = (fhOffClassProt + 20) / 2

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

// FileHeaderAreas bundles the variable-position content EncodeFileHeader
// lays out after a header's fixed fields, in the conventional order VMS
// itself uses: IDENT, then the retrieval-pointer map, then ACL. Encoding a
// header is different from decoding one in exactly this respect — a
// decoded header's IdentOffset/MapOffset/AclOffset just replay whatever
// positions were already on disk, but building a header from scratch means
// *choosing* those positions (see IdentOffset's own doc comment), which is
// what this type and EncodeFileHeader exist to do.
type FileHeaderAreas struct {
	// Ident is this header's file identification content. Nil produces a
	// header with a zero-length IDENT area — a legitimate on-disk shape
	// (see FileHeader.IdentOffset's documentation on where the high-water
	// mark check requires one), though every header this project's own
	// write path constructs is expected to set one.
	Ident *Ident

	// MapBytes is the already-encoded retrieval-pointer map area (see the
	// map-area encoder added in a later subtask); nil for a header with no
	// data extents of its own, e.g. one that only chains to an extension
	// segment via ExtensionFid. Must have an even length, since retrieval
	// pointer entries are always a whole number of 16-bit words.
	MapBytes []byte

	// AclBytes is the already-encoded access-control-list area. This
	// project never constructs ACLs, so every caller today passes nil; the
	// field exists so the layout logic below has one consistent place to
	// account for all three variable areas, symmetric with how the on-disk
	// format itself treats them. Must have an even length.
	AclBytes []byte
}

// EncodeFileHeader encodes h into its 512-byte on-disk representation,
// laying out areas' IDENT/map/ACL content immediately after h's fixed
// fields and computing IdentOffset, MapOffset, AclOffset, EndOffset,
// MapWordsInUse, and Checksum from that layout. Whatever values h itself
// carries in those six fields are ignored: they only make sense as the
// OUTPUT of this layout decision, unlike h's other fields (Fid,
// RecordAttributes, HighWaterMark, and so on), which really are just
// replayed onto disk as given.
//
// Returns an error if areas' content doesn't fit in the 402 bytes
// available between the end of h's fixed fields and the checksum field,
// or if MapBytes/AclBytes has an odd length.
func EncodeFileHeader(h FileHeader, areas FileHeaderAreas) ([]byte, error) {
	if len(areas.MapBytes)%2 != 0 {
		return nil, fmt.Errorf("ondisk: FileHeaderAreas.MapBytes has odd length %d", len(areas.MapBytes))
	}
	if len(areas.AclBytes)%2 != 0 {
		return nil, fmt.Errorf("ondisk: FileHeaderAreas.AclBytes has odd length %d", len(areas.AclBytes))
	}

	var identBytes []byte
	identWords := 0
	if areas.Ident != nil {
		var err error
		identBytes, err = EncodeIdent(*areas.Ident)
		if err != nil {
			return nil, fmt.Errorf("ondisk: encoding FileHeader IDENT area: %w", err)
		}
		identWords = len(identBytes) / 2
	}
	mapWords := len(areas.MapBytes) / 2
	aclWords := len(areas.AclBytes) / 2

	identOffsetWords := fhVariableAreaStart
	mapOffsetWords := identOffsetWords + identWords
	aclOffsetWords := mapOffsetWords + mapWords
	endOffsetWords := aclOffsetWords + aclWords

	if endOffsetWords*2 > fhOffChecksum {
		available := fhOffChecksum - fhVariableAreaStart*2
		needed := endOffsetWords*2 - fhVariableAreaStart*2
		return nil, fmt.Errorf(
			"ondisk: FileHeader IDENT+map+ACL areas need %d bytes, only %d available before the checksum",
			needed, available)
	}

	b := make([]byte, FileHeaderSize)

	b[fhOffIdOffset] = uint8(identOffsetWords)
	b[fhOffMpOffset] = uint8(mapOffsetWords)
	b[fhOffAcOffset] = uint8(aclOffsetWords)
	b[fhOffRsOffset] = uint8(endOffsetWords)
	binary.LittleEndian.PutUint16(b[fhOffSegNum:], h.SegmentNumber)
	binary.LittleEndian.PutUint16(b[fhOffStrucLevel:], h.StructureLevel)
	copy(b[fhOffFid:fhOffFid+FidSize], EncodeFid(h.Fid))
	copy(b[fhOffExtFid:fhOffExtFid+FidSize], EncodeFid(h.ExtensionFid))
	copy(b[fhOffRecAttr:fhOffRecAttr+RecAttrSize], EncodeRecAttr(h.RecordAttributes))
	binary.LittleEndian.PutUint32(b[fhOffFileChar:], h.FileCharacteristics)
	b[fhOffMapInUse] = uint8(mapWords)
	b[fhOffAccMode] = h.AccessMode
	copy(b[fhOffFileOwner:fhOffFileOwner+UicSize], EncodeUic(h.Owner))
	binary.LittleEndian.PutUint16(b[fhOffFileProt:], h.FileProtection)
	copy(b[fhOffBacklink:fhOffBacklink+FidSize], EncodeFid(h.Backlink))
	b[fhOffJournal] = h.Journaling
	b[fhOffRuActive] = h.RecoveryUnitActive
	binary.LittleEndian.PutUint32(b[fhOffHighwater:], h.HighWaterMark)
	copy(b[fhOffClassProt:fhOffClassProt+20], h.ClassProtection[:])

	if len(identBytes) > 0 {
		copy(b[identOffsetWords*2:identOffsetWords*2+len(identBytes)], identBytes)
	}
	if len(areas.MapBytes) > 0 {
		copy(b[mapOffsetWords*2:mapOffsetWords*2+len(areas.MapBytes)], areas.MapBytes)
	}
	if len(areas.AclBytes) > 0 {
		copy(b[aclOffsetWords*2:aclOffsetWords*2+len(areas.AclBytes)], areas.AclBytes)
	}

	sum, err := Checksum(b)
	if err != nil {
		// Unreachable given b's fixed length above.
		return nil, err
	}
	binary.LittleEndian.PutUint16(b[fhOffChecksum:], sum)

	return b, nil
}
