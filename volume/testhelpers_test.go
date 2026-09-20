package volume

import (
	"encoding/binary"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// memContainer is an in-memory diskimage.Container backed by a plain slice
// of blocks, letting tests build a synthetic ODS-2 volume without needing
// a real disk image file on disk.
type memContainer struct {
	blocks [][]byte
}

// newMemContainer creates a memContainer with the given number of blocks,
// all initially zeroed.
func newMemContainer(numBlocks int) *memContainer {
	m := &memContainer{blocks: make([][]byte, numBlocks)}
	for i := range m.blocks {
		m.blocks[i] = make([]byte, ondisk.BlockSize)
	}
	return m
}

// putBlock installs raw block data at the given LBN, replacing whatever
// was there. block must be exactly ondisk.BlockSize bytes.
func (m *memContainer) putBlock(lbn uint32, block []byte) {
	copy(m.blocks[lbn], block)
}

func (m *memContainer) ReadBlock(lbn uint32, buf []byte) error {
	if int(lbn) >= len(m.blocks) {
		return diskimage.ErrBlockOutOfRange
	}
	copy(buf, m.blocks[lbn])
	return nil
}

func (m *memContainer) Blocks() uint32 { return uint32(len(m.blocks)) }
func (m *memContainer) Close() error   { return nil }

// The following byte offsets duplicate a subset of the ODS-2 home block
// layout that is documented (and privately defined) on ondisk.HomeBlock.
// They're repeated here, rather than imported, because package ondisk
// deliberately exposes no encoder — this project only ever needs to READ
// ODS-2 volumes, so ondisk only implements decoding. Test code in other
// packages that needs to fabricate on-disk bytes has to know the wire
// format directly, the same way a test of a network protocol parser might
// hand-assemble a packet rather than calling the parser's own (nonexistent)
// encoder.
const (
	testHomeOffHomeLBN      = 0
	testHomeOffClusterSize  = 14
	testHomeOffIdxBitmapVBN = 22
	testHomeOffIdxBitmapLBN = 24
	testHomeOffMaxFiles     = 28
	testHomeOffIdxBitmapSz  = 32
	testHomeOffRvn          = 38
	testHomeOffFormat       = 496
	testHomeOffChecksum2    = 510
)

// homeBlockFixture collects the handful of home block fields that this
// package's tests actually need to control; every other field is left
// zeroed.
type homeBlockFixture struct {
	homeLBN       uint32
	rvn           uint16
	clusterSize   uint16
	idxBitmapVBN  uint16
	idxBitmapLBN  uint32
	idxBitmapSize uint16
	maxFiles      uint32
}

// buildHomeBlockBytes assembles a syntactically valid, correctly
// checksummed 512-byte ODS-2 home block from a homeBlockFixture, ready to
// install into a memContainer with putBlock.
func buildHomeBlockBytes(t *testing.T, f homeBlockFixture) []byte {
	t.Helper()
	b := make([]byte, ondisk.BlockSize)

	binary.LittleEndian.PutUint32(b[testHomeOffHomeLBN:], f.homeLBN)
	binary.LittleEndian.PutUint16(b[testHomeOffClusterSize:], f.clusterSize)
	binary.LittleEndian.PutUint16(b[testHomeOffIdxBitmapVBN:], f.idxBitmapVBN)
	binary.LittleEndian.PutUint32(b[testHomeOffIdxBitmapLBN:], f.idxBitmapLBN)
	binary.LittleEndian.PutUint32(b[testHomeOffMaxFiles:], f.maxFiles)
	binary.LittleEndian.PutUint16(b[testHomeOffIdxBitmapSz:], f.idxBitmapSize)
	binary.LittleEndian.PutUint16(b[testHomeOffRvn:], f.rvn)
	copy(b[testHomeOffFormat:testHomeOffFormat+12], "DECFILE11B  ")

	sum, err := ondisk.Checksum(b)
	if err != nil {
		t.Fatalf("computing test fixture checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[testHomeOffChecksum2:], sum)

	return b
}

// The following byte offsets duplicate a subset of the ODS-2 file header
// layout documented (and privately defined) on ondisk.FileHeader, for the
// same reason the home block offsets above are duplicated: package ondisk
// has no encoder to call instead.
const (
	testFhOffIdOffset  = 0
	testFhOffMpOffset  = 1
	testFhOffFid       = 8
	testFhOffExtFid    = 14
	testFhOffRecAttr   = 20
	testFhOffFileChar  = 52
	testFhOffMapInUse  = 58
	testFhOffHighwater = 76
	testFhOffChecksum  = 510
)

// fileHeaderFixture collects the handful of FileHeader fields this
// package's tests actually need to control; every other field is left
// zeroed. A zero extensionFid (the default) means "no extension segment".
type fileHeaderFixture struct {
	fid          ondisk.Fid
	extensionFid ondisk.Fid

	// identOffset must be > 39 for File.ReadBlock to honor highWaterMark
	// at all (see ondisk.FileHeader.IdentOffset's documentation); tests
	// that don't care about high-water behavior can leave this 0, in
	// which case buildFileHeaderBytes fills in 40.
	identOffset uint8

	fileChar      uint32
	highWaterMark uint32

	// mapOffsetWords/mapBytes place a retrieval-pointer map area (as
	// raw, already-encoded bytes — see encodeExtentFormat2) at a word
	// offset within the header. Leave both zero for a header with no
	// data extents of its own (e.g. one that only chains to an
	// extension segment via extensionFid).
	mapOffsetWords uint8
	mapBytes       []byte
}

// putFid writes a Fid's 6-byte on-disk representation at the given byte
// offset within b.
func putFidAt(b []byte, offset int, fid ondisk.Fid) {
	binary.LittleEndian.PutUint16(b[offset:], fid.Num)
	binary.LittleEndian.PutUint16(b[offset+2:], fid.Seq)
	b[offset+4] = fid.Rvn
	b[offset+5] = fid.Nmx
}

// putSwappedLongword writes val into b (which must be at least 4 bytes)
// using the VAX RMS "swapped longword" convention that RecAttr's
// HighestBlock/EndOfFileBlock fields use — see ondisk's
// decodeSwappedLongword for what this means and why it's necessary.
func putSwappedLongword(b []byte, val uint32) {
	firstWord := uint16(val >> 16)
	secondWord := uint16(val & 0xFFFF)
	binary.LittleEndian.PutUint16(b[0:2], firstWord)
	binary.LittleEndian.PutUint16(b[2:4], secondWord)
}

// encodeExtentFormat2 encodes one retrieval-pointer extent using the
// on-disk "word format" (format 2 of 4 — see ondisk.FileHeader.
// RetrievalPointers): a 14-bit block count plus a full 32-bit starting
// LBN, three words total. This is the simplest format that can represent
// an arbitrary count/LBN combination without needing to split a value
// across multiple words, which is why test fixtures use it exclusively
// even though a real volume would pick whichever format is most compact
// for the actual values involved.
func encodeExtentFormat2(count, lbn uint32) []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint16(b[0:2], uint16(0x8000|((count-1)&0x3FFF)))
	binary.LittleEndian.PutUint16(b[2:4], uint16(lbn&0xFFFF))
	binary.LittleEndian.PutUint16(b[4:6], uint16(lbn>>16))
	return b
}

// buildFileHeaderBytes assembles a syntactically valid, correctly
// checksummed 512-byte ODS-2 file header from a fileHeaderFixture, ready
// to install into a memContainer with putBlock.
func buildFileHeaderBytes(t *testing.T, f fileHeaderFixture) []byte {
	t.Helper()
	b := make([]byte, ondisk.BlockSize)

	// Unlike a real header, identOffset defaults to 0 here (rather than a
	// plausible real value like 40), so that File.ReadBlock's high-water
	// mark check is OFF by default for any fixture that doesn't
	// explicitly opt in by setting identOffset > 39 — otherwise every
	// fixture that doesn't care about high-water behavior, and so leaves
	// highWaterMark at its zero value, would have every one of its blocks
	// incorrectly treated as unwritten.
	b[testFhOffIdOffset] = f.identOffset
	b[testFhOffMpOffset] = f.mapOffsetWords
	b[testFhOffMapInUse] = byte(len(f.mapBytes) / 2)

	putFidAt(b, testFhOffFid, f.fid)
	putFidAt(b, testFhOffExtFid, f.extensionFid)

	binary.LittleEndian.PutUint32(b[testFhOffFileChar:], f.fileChar)
	binary.LittleEndian.PutUint32(b[testFhOffHighwater:], f.highWaterMark)

	if f.mapBytes != nil {
		start := int(f.mapOffsetWords) * 2
		copy(b[start:start+len(f.mapBytes)], f.mapBytes)
	}

	sum, err := ondisk.Checksum(b)
	if err != nil {
		t.Fatalf("computing test fixture checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[testFhOffChecksum:], sum)

	return b
}

// newMountableContainer builds a memContainer with a valid home block at
// LBN 1 and a minimal (zero-extent) INDEXF.SYS bootstrap header installed
// at the location that home block describes, which is enough for Mount to
// succeed. Tests that need INDEXF.SYS to describe real extents (to open
// additional files) should build on top of this with their own calls to
// putBlock rather than starting from scratch.
func newMountableContainer(t *testing.T, numBlocks int, home homeBlockFixture) *memContainer {
	t.Helper()
	home.homeLBN = 1
	c := newMemContainer(numBlocks)
	c.putBlock(1, buildHomeBlockBytes(t, home))

	indexHeaderLBN := home.idxBitmapLBN + uint32(home.idxBitmapSize)
	c.putBlock(indexHeaderLBN, buildFileHeaderBytes(t, fileHeaderFixture{
		fid: ondisk.IndexFileFid,
	}))

	return c
}
