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
