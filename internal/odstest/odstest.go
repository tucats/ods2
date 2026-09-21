// Package odstest provides shared test fixtures for building synthetic
// ODS-2 volumes in memory: an in-memory diskimage.Container, and
// byte-level builders for the home block, file header, and directory
// record structures.
//
// This lives under internal/ (rather than being folded into, say, package
// ondisk itself) because more than one package's tests need to fabricate
// these bytes (package volume, to build a mountable in-memory volume;
// package filespec, to build a small directory tree to glob against), and
// duplicating the same fixture-assembly logic in each package's own test
// files would be exactly the kind of copy-paste this project otherwise
// tries to avoid. Every package within this module can import an
// internal/ package, so this one place serves all of them, while still
// being invisible to (and unusable by) anyone importing this module from
// outside it.
//
// Historical note: until ondisk gained its own Encode* functions (see
// docs/PHASE-02.md subtask 2), this package hand-rolled every byte offset
// itself, duplicating ondisk's private decode-side knowledge. Its builders
// now delegate the mechanical byte-layout work to ondisk.Encode* wherever
// their fixture shape allows it (BuildHomeBlockBytes, most fully); what
// remains here is fixture-specific assembly ondisk's own encoders don't
// (and, in FileHeaderFixture's case, structurally can't — see its
// IdentOffset field) attempt to generalize.
package odstest

import (
	"encoding/binary"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
)

// MemContainer is an in-memory diskimage.Container backed by a plain
// slice of blocks, letting tests build a synthetic ODS-2 volume without
// needing a real disk image file on disk.
type MemContainer struct {
	blocks [][]byte
}

// NewMemContainer creates a MemContainer with the given number of blocks,
// all initially zeroed.
func NewMemContainer(numBlocks int) *MemContainer {
	m := &MemContainer{blocks: make([][]byte, numBlocks)}
	for i := range m.blocks {
		m.blocks[i] = make([]byte, ondisk.BlockSize)
	}
	return m
}

// PutBlock installs raw block data at the given LBN, replacing whatever
// was there. block must be exactly ondisk.BlockSize bytes.
func (m *MemContainer) PutBlock(lbn uint32, block []byte) {
	copy(m.blocks[lbn], block)
}

func (m *MemContainer) ReadBlock(lbn uint32, buf []byte) error {
	if int(lbn) >= len(m.blocks) {
		return diskimage.ErrBlockOutOfRange
	}
	copy(buf, m.blocks[lbn])
	return nil
}

func (m *MemContainer) Blocks() uint32 { return uint32(len(m.blocks)) }
func (m *MemContainer) Close() error   { return nil }

// The following byte offsets duplicate the on-disk file header layout
// documented (and privately defined) on ondisk.FileHeader. See this file's
// package comment for why they're repeated here rather than imported. (The
// equivalent home-block offsets no longer need repeating here now that
// ondisk.EncodeHomeBlock exists -- see BuildHomeBlockBytes.)
const (
	fhOffIdOffset  = 0
	fhOffMpOffset  = 1
	fhOffFid       = 8
	fhOffExtFid    = 14
	fhOffRecAttr   = 20
	fhOffFileChar  = 52
	fhOffMapInUse  = 58
	fhOffHighwater = 76
	fhOffChecksum  = 510

	dirRecHeaderSize = 6
	dirEntSize       = 8
)

// HomeBlockFixture collects the handful of ODS-2 home block fields this
// project's tests actually need to control; every other field is left
// zeroed.
type HomeBlockFixture struct {
	HomeLBN       uint32
	Rvn           uint16
	ClusterSize   uint16
	IdxBitmapVBN  uint16
	IdxBitmapLBN  uint32
	IdxBitmapSize uint16
	MaxFiles      uint32
	ReservedFiles uint16
}

// BuildHomeBlockBytes assembles a syntactically valid, correctly
// checksummed 512-byte ODS-2 home block from a HomeBlockFixture, ready to
// install into a MemContainer with PutBlock. This is a thin wrapper around
// ondisk.EncodeHomeBlock -- it exists only to translate this package's
// narrower test-fixture shape into a full ondisk.HomeBlock, and to fail
// the test (rather than return an error) if that somehow doesn't encode.
func BuildHomeBlockBytes(t testing.TB, f HomeBlockFixture) []byte {
	t.Helper()

	b, err := ondisk.EncodeHomeBlock(ondisk.HomeBlock{
		HomeLBN:              f.HomeLBN,
		ClusterSize:          f.ClusterSize,
		IndexBitmapVBN:       f.IdxBitmapVBN,
		IndexBitmapLBN:       f.IdxBitmapLBN,
		MaxFiles:             f.MaxFiles,
		IndexBitmapSize:      f.IdxBitmapSize,
		ReservedFiles:        f.ReservedFiles,
		RelativeVolumeNumber: f.Rvn,
		Format:               ondisk.HomeBlockFormatID,
	})
	if err != nil {
		t.Fatalf("odstest: encoding home block: %v", err)
	}

	return b
}

// FileHeaderFixture collects the handful of ODS-2 file header fields this
// project's tests actually need to control; every other field is left
// zeroed. A zero ExtensionFid (the default) means "no extension segment".
type FileHeaderFixture struct {
	Fid          ondisk.Fid
	ExtensionFid ondisk.Fid

	// IdentOffset must be > 39 for volume.File.ReadBlock to honor
	// HighWaterMark at all (see ondisk.FileHeader.IdentOffset's
	// documentation); leave this 0 (the default) for a fixture that
	// doesn't care about high-water behavior, which disables the check
	// entirely. It also positions the file identification (IDENT) area
	// RevisionDate below is written into -- set it to a real,
	// non-colliding word offset (e.g. 40, the same value commonly used to
	// enable high-water-mark behavior) whenever RevisionDate is used.
	IdentOffset uint8

	// RevisionDate, if nonzero, is written into the file's IDENT area
	// (see ondisk.FileHeader.Ident) at IdentOffset's position, for tests
	// exercising behavior that reads a file's dates (such as `copy`'s
	// /time qualifier).
	RevisionDate vmstime.VMSTime

	FileChar      uint32
	HighWaterMark uint32

	// HighestBlock becomes RecordAttributes.HighestBlock, i.e. volume.
	// File.Blocks() -- the file's length in *allocated* virtual blocks.
	// Anything that reads a file's full extent needs it set to match how
	// much data MapBytes describes.
	HighestBlock uint32

	// The remaining fields become the rest of RecordAttributes, needed by
	// package rms's tests to control a file's record format: Format is
	// the record format (Fixed/Variable/VFC/Stream*); EndOfFileBlock and
	// FirstFreeByte together give the file's exact valid length in bytes
	// (see ondisk.RecAttr's documentation) -- volume.Directory.List relies
	// on these, not HighestBlock, to know how much of a directory's
	// allocation is actually real content (see volume.File.UsedBlocks).
	// For a directory fixture (FileChar includes ondisk.FchDirectory)
	// that leaves EndOfFileBlock at its zero default, BuildFileHeaderBytes
	// fills it in from HighestBlock instead, treating every allocated
	// block as real, fully-written data -- the assumption every directory
	// fixture in this codebase already made before List() distinguished
	// the two. A test that specifically wants a directory with unwritten
	// trailing allocation (allocated beyond what it actually uses --
	// see volume's TestDirectoryListSkipsUnwrittenTrailingBlocks) must
	// set EndOfFileBlock explicitly to opt out of this default; it does
	// not apply to a non-directory fixture at all, since a real,
	// deliberately-empty file (EndOfFileBlock 0 despite a nonzero
	// HighestBlock -- see rms's TestReaderEmptyFile) is a legitimate case
	// there that this default would otherwise silently break.
	Format         ondisk.RecordFormat
	EndOfFileBlock uint32
	FirstFreeByte  uint16
	MaxRecordSize  uint16
	VfcSize        uint8

	// MapOffsetWords/MapBytes place a retrieval-pointer map area (as raw,
	// already-encoded bytes -- see EncodeExtentFormat2) at a word offset
	// within the header. Leave both zero for a header with no data
	// extents of its own (e.g. one that only chains to an extension
	// segment via ExtensionFid).
	MapOffsetWords uint8
	MapBytes       []byte
}

// putFidAt writes fid's 6-byte on-disk encoding into b at offset, via
// ondisk.EncodeFid (this package's own former hand-rolled duplicate of
// that logic was retired once EncodeFid could produce identical bytes --
// see docs/PHASE-02.md subtask 2).
func putFidAt(b []byte, offset int, fid ondisk.Fid) {
	copy(b[offset:offset+ondisk.FidSize], ondisk.EncodeFid(fid))
}

// EncodeExtentFormat2 encodes one retrieval-pointer extent using the
// on-disk "word format" (format 2 of 4 -- see ondisk.FileHeader.
// RetrievalPointers): a 14-bit block count plus a full 32-bit starting
// LBN, three words total. This is the simplest format that can represent
// an arbitrary count/LBN combination without needing to split a value
// across multiple words, which is why test fixtures use it exclusively
// even though a real volume would pick whichever format is most compact
// for the actual values involved.
func EncodeExtentFormat2(count, lbn uint32) []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint16(b[0:2], uint16(0x8000|((count-1)&0x3FFF)))
	binary.LittleEndian.PutUint16(b[2:4], uint16(lbn&0xFFFF))
	binary.LittleEndian.PutUint16(b[4:6], uint16(lbn>>16))
	return b
}

// BuildFileHeaderBytes assembles a syntactically valid, correctly
// checksummed 512-byte ODS-2 file header from a FileHeaderFixture, ready
// to install into a MemContainer with PutBlock.
func BuildFileHeaderBytes(t testing.TB, f FileHeaderFixture) []byte {
	t.Helper()
	b := make([]byte, ondisk.BlockSize)

	b[fhOffIdOffset] = f.IdentOffset
	b[fhOffMpOffset] = f.MapOffsetWords
	b[fhOffMapInUse] = byte(len(f.MapBytes) / 2)

	putFidAt(b, fhOffFid, f.Fid)
	putFidAt(b, fhOffExtFid, f.ExtensionFid)

	binary.LittleEndian.PutUint32(b[fhOffFileChar:], f.FileChar)
	binary.LittleEndian.PutUint32(b[fhOffHighwater:], f.HighWaterMark)

	// See FileHeaderFixture.EndOfFileBlock's doc comment: a directory
	// fixture that doesn't care about the used-vs-allocated distinction
	// gets EndOfFileBlock filled in from HighestBlock automatically,
	// using the same "data ends exactly on a block boundary" on-disk
	// convention real ODS-2 headers use (the following block, at offset
	// 0) rather than requiring every call site to compute this by hand.
	if f.FileChar&ondisk.FchDirectory != 0 && f.EndOfFileBlock == 0 && f.HighestBlock > 0 {
		f.EndOfFileBlock = f.HighestBlock + 1
	}

	// The RecAttr sub-structure begins at fhOffRecAttr; ondisk.EncodeRecAttr
	// (added alongside DecodeRecAttr once this package needed to write, not
	// just read, ODS-2 volumes -- see docs/PHASE-02.md subtask 2) handles
	// its internal layout, including HighestBlock/EndOfFileBlock's
	// "swapped longword" encoding.
	recAttr := ondisk.EncodeRecAttr(ondisk.RecAttr{
		Format:         f.Format,
		HighestBlock:   f.HighestBlock,
		EndOfFileBlock: f.EndOfFileBlock,
		FirstFreeByte:  f.FirstFreeByte,
		VfcSize:        f.VfcSize,
		MaxRecordSize:  f.MaxRecordSize,
	})
	copy(b[fhOffRecAttr:fhOffRecAttr+ondisk.RecAttrSize], recAttr)

	if f.MapBytes != nil {
		start := int(f.MapOffsetWords) * 2
		copy(b[start:start+len(f.MapBytes)], f.MapBytes)
	}

	if f.RevisionDate != 0 {
		// identOffRevDate mirrors ondisk/ident.go's own (private) offset
		// of RevisionDate within the IDENT area: 30 bytes in, after the
		// 20-byte filename, 2-byte revision counter, and 8-byte creation
		// date that precede it.
		const identOffRevDate = 30
		identStart := int(f.IdentOffset) * 2
		binary.LittleEndian.PutUint64(b[identStart+identOffRevDate:], uint64(f.RevisionDate))
	}

	sum, err := ondisk.Checksum(b)
	if err != nil {
		t.Fatalf("odstest: computing file header checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[fhOffChecksum:], sum)

	return b
}

// NewMountableContainer builds a MemContainer with a valid home block at
// LBN 1 and a minimal (zero-extent) INDEXF.SYS bootstrap header installed
// at the location that home block describes, which is enough for
// volume.Mount to succeed. Tests that need INDEXF.SYS to describe real
// extents (to open additional files) should build on top of this with
// their own calls to PutBlock rather than starting from scratch.
func NewMountableContainer(t testing.TB, numBlocks int, home HomeBlockFixture) *MemContainer {
	t.Helper()
	home.HomeLBN = 1
	c := NewMemContainer(numBlocks)
	c.PutBlock(1, BuildHomeBlockBytes(t, home))

	indexHeaderLBN := home.IdxBitmapLBN + uint32(home.IdxBitmapSize)
	c.PutBlock(indexHeaderLBN, BuildFileHeaderBytes(t, FileHeaderFixture{
		Fid: ondisk.IndexFileFid,
	}))

	return c
}

// BuildDirRecordBytes assembles one directory name record (header, name
// text, padding, and version entries), the same on-disk shape
// ondisk.DecodeDirectoryBlock expects to parse.
func BuildDirRecordBytes(name string, versions []uint16, fids []ondisk.Fid) []byte {
	nameBytes := []byte(name)
	paddedNameLen := (len(nameBytes) + 1) &^ 1 // round up to even, same as ondisk's roundUpToEven
	entriesStart := dirRecHeaderSize + paddedNameLen
	totalLen := entriesStart + len(versions)*dirEntSize

	b := make([]byte, totalLen)
	binary.LittleEndian.PutUint16(b[0:2], uint16(totalLen-2)) // dir$size = total record length - 2
	b[5] = byte(len(nameBytes))                               // dir$namecount
	copy(b[dirRecHeaderSize:dirRecHeaderSize+len(nameBytes)], nameBytes)

	for i, v := range versions {
		off := entriesStart + i*dirEntSize
		binary.LittleEndian.PutUint16(b[off:off+2], v)
		putFidAt(b, off+2, fids[i])
	}
	return b
}

// BuildDirBlock assembles a full 512-byte directory data block out of the
// given records, followed by the 0xFFFF end-of-data sentinel.
func BuildDirBlock(records ...[]byte) []byte {
	block := make([]byte, ondisk.BlockSize)
	offset := 0
	for _, r := range records {
		copy(block[offset:], r)
		offset += len(r)
	}
	binary.LittleEndian.PutUint16(block[offset:offset+2], 0xFFFF)
	return block
}
