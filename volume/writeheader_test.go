package volume

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

// This file's tests share yet another small writable-volume layout (see
// bitmap_test.go's newWritableTestVolume and indexbitmap_test.go's
// newWritableIndexBitmapTestVolume for the two this package already had):
// one with its OWN, larger BITMAP.SYS free-space region, since several
// tests below (particularly the extension-header-segment ones) need many
// more free clusters than those two fixtures' 10-cluster volumes provide.
// It reuses file_test.go's shared INDEXF.SYS/header-area layout
// (testIndexBitmapLBN etc., fileHeaderLBN) and indexbitmap_test.go's
// MaxFiles/ReservedFiles constants (testIdxMaxFiles/testIdxReservedFiles),
// same as every other writable fixture in this package.
const (
	whClusterSize   = 1   // 1 block per cluster -- keeps LBN arithmetic simple
	whVolumeSize    = 600 // StorageControlBlock.VolumeSize
	whBitmapSCBLBN  = 300
	whBitmapBitsLBN = 301

	// whFreeClustersStart is deliberately well past every fixed structure
	// this layout uses (the header area occupies roughly LBN 8-27 for
	// MaxFiles=20 -- see file_test.go's fileHeaderLBN -- and BITMAP.SYS
	// itself lives at whBitmapSCBLBN/whBitmapBitsLBN above), so tests can
	// mark a wide, safely-unused range free without computing exactly which
	// individual clusters collide with something else.
	whFreeClustersStart = 320
	whFreeClustersEnd   = whVolumeSize // exclusive
)

// newWritableHeaderTestVolume builds and mounts a minimal writable volume
// shaped for CreateHeader/Extend testing: a home block (with MaxFiles/
// ReservedFiles/ClusterSize/VolumeOwner/FileProtection all set, since
// CreateHeader's tests need every one of them) and INDEXF.SYS, but --
// like this package's other writable fixtures -- no BITMAP.SYS yet;
// installWideTestBitmap installs one.
func newWritableHeaderTestVolume(t *testing.T) (*Device, diskimage.WritableContainer) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.dsk")
	container, err := diskimage.Create(path, whVolumeSize+100)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })

	mustWrite := func(lbn uint32, b []byte) {
		t.Helper()
		if err := container.WriteBlock(lbn, b); err != nil {
			t.Fatalf("WriteBlock(%d): %v", lbn, err)
		}
	}

	mustWrite(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:        1,
		Rvn:            1,
		ClusterSize:    whClusterSize,
		IdxBitmapVBN:   testIndexBitmapVBN,
		IdxBitmapLBN:   testIndexBitmapLBN,
		IdxBitmapSize:  testIndexBitmapSize,
		MaxFiles:       testIdxMaxFiles,
		ReservedFiles:  testIdxReservedFiles,
		VolumeOwner:    ondisk.Uic{Group: 0o10, Member: 4},
		FileProtection: 0xFF00,
	}))

	mustWrite(testIndexBitmapLBN+testIndexBitmapSize, odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(250, testIndexBitmapLBN),
	}))

	vol, err := Mount(container)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return vol.Devices[0], container
}

// installWideTestBitmap writes a BITMAP.SYS describing a whVolumeSize-block
// volume (at whClusterSize) with every cluster in
// [whFreeClustersStart, whFreeClustersEnd) free and everything else
// allocated -- enough contiguous free space for tests that need to extend a
// file many times over (forcing an extension-header segment), unlike this
// package's other, deliberately tiny bitmap fixtures.
func installWideTestBitmap(t *testing.T, container diskimage.WritableContainer) {
	t.Helper()

	scb, err := ondisk.EncodeStorageControlBlock(ondisk.StorageControlBlock{
		ClusterSize: whClusterSize,
		VolumeSize:  whVolumeSize,
	})
	if err != nil {
		t.Fatalf("EncodeStorageControlBlock: %v", err)
	}
	if err := container.WriteBlock(whBitmapSCBLBN, scb); err != nil {
		t.Fatalf("WriteBlock(SCB): %v", err)
	}

	bits := make([]byte, ondisk.BlockSize)
	for c := uint32(whFreeClustersStart); c < whFreeClustersEnd; c++ {
		ondisk.BitmapSet(bits, c)
	}
	if err := container.WriteBlock(whBitmapBitsLBN, bits); err != nil {
		t.Fatalf("WriteBlock(bitmap bits): %v", err)
	}

	header := odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.BitmapFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(2, whBitmapSCBLBN),
	})
	if err := container.WriteBlock(fileHeaderLBN(ondisk.BitmapFileFid.Num), header); err != nil {
		t.Fatalf("WriteBlock(bitmap header): %v", err)
	}
}

func TestCreateHeaderAllocatesFreeSlotAndSetsFields(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3}) // reserved files 1-3 already accounted for

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	dirFid := ondisk.Fid{Num: 4, Seq: 4}
	f, err := CreateHeader(dev, ib, NewFileHeader{
		Name:            "TEST.DAT",
		Directory:       dirFid,
		Characteristics: 0,
		RecordAttributes: ondisk.RecAttr{
			Format: ondisk.RecordFormatFixed,
		},
	})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	// The first non-reserved slot is testIdxReservedFiles+1.
	if got, want := f.Header.Fid.Number(), uint32(testIdxReservedFiles+1); got != want {
		t.Errorf("new file's number = %d, want %d", got, want)
	}
	if f.Header.Fid.Seq != 1 {
		t.Errorf("new file's Seq = %d, want 1 (never-used slot)", f.Header.Fid.Seq)
	}
	if f.Header.Backlink != dirFid {
		t.Errorf("Backlink = %v, want %v", f.Header.Backlink, dirFid)
	}
	if f.Header.Owner != (ondisk.Uic{Group: 0o10, Member: 4}) {
		t.Errorf("Owner = %v, want the home block's VolumeOwner", f.Header.Owner)
	}
	if f.Header.FileProtection != 0xFF00 {
		t.Errorf("FileProtection = %#04x, want 0xff00 (the home block's FileProtection)", f.Header.FileProtection)
	}
	if f.Blocks() != 0 {
		t.Errorf("Blocks() = %d, want 0 (a freshly created file has no data yet)", f.Blocks())
	}
	if len(f.Extents) != 0 {
		t.Errorf("Extents = %+v, want none", f.Extents)
	}

	ident, err := f.Header.Ident()
	if err != nil {
		t.Fatalf("Ident: %v", err)
	}
	if ident.Filename != "TEST.DAT" {
		t.Errorf("Ident.Filename = %q, want %q", ident.Filename, "TEST.DAT")
	}

	// The slot CreateHeader just used must now be marked in-use in ib (in
	// memory -- CreateHeader itself calls MarkAllocated), so the next free
	// slot a caller finds must be the one AFTER it, not the same one again.
	next, err := ib.FindFreeSlot()
	if err != nil {
		t.Fatalf("FindFreeSlot after CreateHeader: %v", err)
	}
	if next != testIdxReservedFiles+2 {
		t.Errorf("FindFreeSlot after CreateHeader = %d, want %d (the slot after the one just allocated)", next, testIdxReservedFiles+2)
	}
}

func TestCreateHeaderRoundTripsThroughOpenFID(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	created, err := CreateHeader(dev, ib, NewFileHeader{
		Name:      "ROUND.TRP",
		Directory: ondisk.Fid{Num: 4, Seq: 4},
	})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	vol := &Volume{Devices: []*Device{dev}}
	opened, err := vol.OpenFID(created.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID(%v): %v", created.Header.Fid, err)
	}

	if opened.Header.Fid != created.Header.Fid {
		t.Errorf("OpenFID Fid = %v, want %v", opened.Header.Fid, created.Header.Fid)
	}
	if opened.Header.Backlink != created.Header.Backlink {
		t.Errorf("OpenFID Backlink = %v, want %v", opened.Header.Backlink, created.Header.Backlink)
	}
	gotIdent, err := opened.Header.Ident()
	if err != nil {
		t.Fatalf("Ident: %v", err)
	}
	if gotIdent.Filename != "ROUND.TRP" {
		t.Errorf("OpenFID Ident.Filename = %q, want %q", gotIdent.Filename, "ROUND.TRP")
	}
}

func TestCreateHeaderIncrementsSequenceFromPreviousOccupant(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})

	// Simulate a slot that once held a (since-deleted) file: Fid.Num and
	// Checksum are both zero (so IndexBitmap.FindFreeSlot's consistency
	// check accepts it as "looks unused"), but Seq is a leftover nonzero
	// generation counter -- the exact shape CreateHeader's own doc comment
	// describes. odstest.BuildFileHeaderBytes always computes a genuinely
	// matching checksum for whatever content it's given, so it can't
	// produce "Checksum stored as literal 0 despite nonzero other content"
	// on its own; this is built by hand instead.
	targetFileNumber := uint32(testIdxReservedFiles + 1)
	raw := make([]byte, ondisk.BlockSize)
	copy(raw[8:14], ondisk.EncodeFid(ondisk.Fid{Num: 0, Seq: 41})) // fhOffFid = 8 (ondisk/fileheader.go)
	if err := container.WriteBlock(fileHeaderLBN(uint16(targetFileNumber)), raw); err != nil {
		t.Fatalf("WriteBlock(previous occupant): %v", err)
	}

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "REUSE.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	if f.Header.Fid.Number() != targetFileNumber {
		t.Fatalf("new file's number = %d, want %d", f.Header.Fid.Number(), targetFileNumber)
	}
	if f.Header.Fid.Seq != 42 {
		t.Errorf("new file's Seq = %d, want 42 (previous occupant's 41, plus one)", f.Header.Fid.Seq)
	}
}

func TestExtendGrowsFileAndZeroFillsUnwrittenSpace(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "GROW.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	if err := Extend(f, bm, ib, 3); err != nil {
		t.Fatalf("Extend(3): %v", err)
	}

	if f.Blocks() != 3 {
		t.Errorf("Blocks() after Extend(3) = %d, want 3", f.Blocks())
	}
	if len(f.Extents) != 1 {
		t.Fatalf("Extents after Extend(3) = %+v, want exactly one extent", f.Extents)
	}
	if f.Extents[0].Count != 3 {
		t.Errorf("Extents[0].Count = %d, want 3", f.Extents[0].Count)
	}

	// The new space is allocated but never written: HighWaterMark was left
	// untouched by Extend (still 0, its value at creation), so every VBN
	// must still read back as zero.
	got := make([]byte, ondisk.BlockSize)
	if err := f.ReadBlock(1, got); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}
	if !bytes.Equal(got, make([]byte, ondisk.BlockSize)) {
		t.Error("ReadBlock(1) on freshly-extended, unwritten space returned non-zero data")
	}

	// The extent must round-trip through a completely fresh OpenFID too --
	// not just through the in-memory f this test already mutated.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if reopened.Blocks() != 3 || len(reopened.Extents) != 1 || reopened.Extents[0] != f.Extents[0] {
		t.Errorf("reopened file = Blocks %d, Extents %+v; want Blocks 3, Extents %+v", reopened.Blocks(), reopened.Extents, f.Extents)
	}
}

func TestExtendAccumulatesAcrossMultipleCalls(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "MULTI.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	if err := Extend(f, bm, ib, 2); err != nil {
		t.Fatalf("Extend(2): %v", err)
	}
	if err := Extend(f, bm, ib, 5); err != nil {
		t.Fatalf("Extend(5): %v", err)
	}

	if f.Blocks() != 7 {
		t.Errorf("Blocks() after Extend(2)+Extend(5) = %d, want 7", f.Blocks())
	}

	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if reopened.Blocks() != 7 {
		t.Errorf("reopened Blocks() = %d, want 7", reopened.Blocks())
	}
	var total uint32
	for _, e := range reopened.Extents {
		total += e.Count
	}
	if total != 7 {
		t.Errorf("reopened Extents total %d blocks, want 7 (Extents: %+v)", total, reopened.Extents)
	}
}

func TestExtendRejectsZeroBlocks(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}
	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "ZERO.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	if err := Extend(f, bm, ib, 0); err == nil {
		t.Fatal("Extend(0): want error, got nil")
	}
}

func TestExtendFailsCleanlyWhenVolumeIsFull(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}
	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "FULL.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	// Only whFreeClustersEnd-whFreeClustersStart clusters (at whClusterSize
	// 1, i.e. that many blocks) are free in this fixture; asking for more
	// than that (but not so much more that allocateExtents' own linear
	// smaller-request search takes unreasonably long -- see its doc
	// comment) must fail rather than partially succeed.
	if err := Extend(f, bm, ib, (whFreeClustersEnd-whFreeClustersStart)+50); err == nil {
		t.Fatal("Extend requesting more space than the volume has: want error, got nil")
	}

	// The failed request must not have left the file with any partial
	// allocation -- allocateExtents' own rollback (see its doc comment)
	// means the space it did manage to find before giving up is freed
	// again, and Extend never applies anything to the header until
	// allocation as a whole has succeeded.
	if f.Blocks() != 0 || len(f.Extents) != 0 {
		t.Errorf("after a failed Extend, Blocks() = %d, Extents = %+v; want no partial growth", f.Blocks(), f.Extents)
	}
}

func TestExtendAllocatesExtensionHeaderSegmentWhenMapIsFull(t *testing.T) {
	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	f, err := CreateHeader(dev, ib, NewFileHeader{Name: "SEG.DAT", Directory: ondisk.Fid{Num: 4, Seq: 4}})
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}

	// Extend one block at a time, deliberately never merged into the
	// previous extent by design (appendExtent always adds a new map entry,
	// never coalesces -- see its own doc comment), so each call consumes
	// map-area room until the primary header's map genuinely runs out and
	// Extend has to fall back to an extension segment. A single header has
	// room for on the order of 100 compact (format 1) entries; 150 calls
	// comfortably forces the fallback at least once regardless of exactly
	// how much room CreateHeader's own IDENT area left.
	const calls = 150
	for i := 0; i < calls; i++ {
		if err := Extend(f, bm, ib, 1); err != nil {
			t.Fatalf("Extend #%d: %v", i, err)
		}
	}

	if f.Blocks() != calls {
		t.Fatalf("Blocks() after %d 1-block Extend calls = %d, want %d", calls, f.Blocks(), calls)
	}
	if f.Header.ExtensionFid.IsZero() {
		t.Fatal("primary header's ExtensionFid is still zero after enough Extend calls to fill its map area")
	}

	// The complete extent list -- chased across the extension segment --
	// must still describe exactly `calls` blocks, and must round-trip
	// through a completely independent OpenFID.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenFID(f.Header.Fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if reopened.Header.ExtensionFid.IsZero() {
		t.Fatal("reopened primary header's ExtensionFid is zero -- the on-disk link was lost")
	}
	var total uint32
	for _, e := range reopened.Extents {
		total += e.Count
	}
	if total != calls {
		t.Errorf("reopened file's extents total %d blocks, want %d", total, calls)
	}

	// The extension segment itself must be independently readable and
	// correctly linked: its own Backlink points back to the primary header
	// it extends (not the file's directory -- see linkNewExtensionSegment's
	// doc comment), and its SegmentNumber follows the primary's.
	segHeader, err := readFileHeaderViaIndex(dev, dev.IndexFile.Extents, reopened.Header.ExtensionFid)
	if err != nil {
		t.Fatalf("reading extension segment header: %v", err)
	}
	if segHeader.Backlink != reopened.Header.Fid {
		t.Errorf("extension segment Backlink = %v, want the primary header's Fid %v", segHeader.Backlink, reopened.Header.Fid)
	}
	if segHeader.SegmentNumber != reopened.Header.SegmentNumber+1 {
		t.Errorf("extension segment SegmentNumber = %d, want %d", segHeader.SegmentNumber, reopened.Header.SegmentNumber+1)
	}
}
