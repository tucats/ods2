package volume

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

func TestDirectoryRejectsNonDirectoryFile(t *testing.T) {
	vol, c := newTestVolume(t)

	fid := ondisk.Fid{Num: 30, Seq: 1}
	c.PutBlock(fileHeaderLBN(fid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: fid}))

	f, err := vol.OpenFID(fid)
	if err != nil {
		t.Fatalf("OpenFID: %v", err)
	}
	if _, err := f.Directory(); err == nil {
		t.Fatal("Directory() on a non-directory file: want error, got nil")
	}
}

func TestDirectoryListAndLookup(t *testing.T) {
	vol, c := newTestVolume(t)

	readmeFid1 := ondisk.Fid{Num: 40, Seq: 1}
	readmeFid2 := ondisk.Fid{Num: 40, Seq: 2}
	dataFid := ondisk.Fid{Num: 41, Seq: 1}

	block := odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("README.TXT", []uint16{1, 2}, []ondisk.Fid{readmeFid1, readmeFid2}),
		odstest.BuildDirRecordBytes("DATA.DAT", []uint16{1}, []ondisk.Fid{dataFid}),
	)
	c.PutBlock(260, block)

	dirFid := ondisk.Fid{Num: 31, Seq: 1}
	c.PutBlock(fileHeaderLBN(dirFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            dirFid,
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, 260),
	}))

	dir, err := vol.OpenDirectory(dirFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []ondisk.DirEntry{
		{Name: "README.TXT", Version: 1, Fid: readmeFid1},
		{Name: "README.TXT", Version: 2, Fid: readmeFid2},
		{Name: "DATA.DAT", Version: 1, Fid: dataFid},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("List() = %+v, want %+v", entries, want)
	}

	t.Run("exact version", func(t *testing.T) {
		got, err := dir.Lookup("README.TXT", 1)
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if got.Fid != readmeFid1 {
			t.Errorf("Fid = %v, want %v", got.Fid, readmeFid1)
		}
	})

	t.Run("highest version", func(t *testing.T) {
		got, err := dir.Lookup("README.TXT", 0)
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if got.Fid != readmeFid2 {
			t.Errorf("Fid = %v, want %v (the higher version)", got.Fid, readmeFid2)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		got, err := dir.Lookup("data.dat", 1)
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if got.Fid != dataFid {
			t.Errorf("Fid = %v, want %v", got.Fid, dataFid)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := dir.Lookup("NOSUCHFILE.TXT", 0); err == nil {
			t.Fatal("Lookup for a nonexistent name: want error, got nil")
		}
	})

	t.Run("wrong version not found", func(t *testing.T) {
		if _, err := dir.Lookup("README.TXT", 99); err == nil {
			t.Fatal("Lookup for a nonexistent version: want error, got nil")
		}
	})
}

// TestDirectoryListSkipsUnwrittenTrailingBlocks reproduces the shape found
// on a real, actively-used OpenVMS volume's root directory: more blocks
// allocated (HighestBlock) than ever actually written (HighWaterMark),
// because VMS pre-extends a directory by HomeBlock.DefaultExtendSize
// blocks at a time and leaves the extra space unwritten until it's
// actually needed. List must stop at the high-water mark rather than
// walking every allocated block — a block at or beyond it is guaranteed
// to read back as all-zero (see File.ReadBlock), which does not decode as
// a valid (even if empty) directory block.
func TestDirectoryListSkipsUnwrittenTrailingBlocks(t *testing.T) {
	vol, c := newTestVolume(t)

	readmeFid := ondisk.Fid{Num: 40, Seq: 1}
	block := odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("README.TXT", []uint16{1}, []ondisk.Fid{readmeFid}),
	)
	c.PutBlock(260, block)

	// Install garbage -- not zeroed, and not a valid directory block -- at
	// the two trailing "allocated but unwritten" blocks. If List() ever
	// tried to read and decode these (instead of stopping at the
	// high-water mark), it would fail with a decode error; a fix that
	// merely happened to rely on unwritten blocks defaulting to zero
	// wouldn't be caught by that, which is why this test doesn't leave
	// them zeroed.
	garbage := make([]byte, ondisk.BlockSize)
	for i := range garbage {
		garbage[i] = 0xAA
	}
	c.PutBlock(261, garbage)
	c.PutBlock(262, garbage)

	dirFid := ondisk.Fid{Num: 32, Seq: 1}
	c.PutBlock(fileHeaderLBN(dirFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            dirFid,
		FileChar:       ondisk.FchDirectory,
		IdentOffset:    40, // required for HighWaterMark to take effect at all
		HighWaterMark:  2,  // only VBN 1 is guaranteed written
		HighestBlock:   3,  // but 3 blocks are allocated (pre-extended slack)
		EndOfFileBlock: 2,  // and only 1 block's worth of data is real --
		FirstFreeByte:  0,  // matching this test's real-world source exactly (see above), not relying on BuildFileHeaderBytes' own EndOfFileBlock default
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(3, 260),
	}))

	dir, err := vol.OpenDirectory(dirFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []ondisk.DirEntry{
		{Name: "README.TXT", Version: 1, Fid: readmeFid},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("List() = %+v, want %+v", entries, want)
	}
}

// Directory mutation (Insert/NextVersion) tests need an actually writable
// volume, unlike the read-only newTestVolume fixture the tests above use --
// they reuse writeheader_test.go's newWritableHeaderTestVolume/
// installWideTestBitmap, the same writable fixture that file's own
// CreateHeader/Extend tests build on.

// newWritableTestDirectory creates a brand-new, empty directory file (via
// CreateHeader, with the directory characteristic set) on dev, ready for
// Insert calls.
func newWritableTestDirectory(t *testing.T, dev *Device, ib *IndexBitmap, name string) *Directory {
	t.Helper()

	f, err := CreateHeader(dev, ib, NewFileHeader{
		Name:            name,
		Directory:       ondisk.Fid{Num: 4, Seq: 4},
		Characteristics: ondisk.FchDirectory,
	})
	if err != nil {
		t.Fatalf("CreateHeader(%s): %v", name, err)
	}
	dir, err := f.Directory()
	if err != nil {
		t.Fatalf("Directory(): %v", err)
	}
	return dir
}

func TestDirectoryInsertIntoEmptyDirectory(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "EMPTY.DIR")
	if dir.Blocks() != 0 {
		t.Fatalf("Blocks() of a freshly created directory = %d, want 0", dir.Blocks())
	}

	fid := ondisk.Fid{Num: 50, Seq: 1}
	if err := dir.Insert("README.TXT", 1, fid, bm, ib); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if dir.Blocks() != 1 {
		t.Errorf("Blocks() after first Insert = %d, want 1 (the directory had to be extended from 0)", dir.Blocks())
	}

	want := []ondisk.DirEntry{{Name: "README.TXT", Version: 1, Fid: fid}}
	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("List() = %+v, want %+v", entries, want)
	}

	// Confirm this round-trips through a completely independent reopen, not
	// just the in-memory dir this test already mutated.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenDirectory(dir.Header.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	reentries, err := reopened.List()
	if err != nil {
		t.Fatalf("List (reopened): %v", err)
	}
	if !reflect.DeepEqual(reentries, want) {
		t.Errorf("List() after reopen = %+v, want %+v", reentries, want)
	}
}

func TestDirectoryInsertSecondVersionAndNextVersion(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "VERS.DIR")

	// A name with no existing entries at all predicts version 1.
	firstVersion, err := dir.NextVersion("DATA.DAT")
	if err != nil {
		t.Fatalf("NextVersion (no existing entries): %v", err)
	}
	if firstVersion != 1 {
		t.Errorf("NextVersion(DATA.DAT) with nothing inserted yet = %d, want 1", firstVersion)
	}

	fid1 := ondisk.Fid{Num: 50, Seq: 1}
	if err := dir.Insert("DATA.DAT", firstVersion, fid1, bm, ib); err != nil {
		t.Fatalf("Insert v%d: %v", firstVersion, err)
	}

	secondVersion, err := dir.NextVersion("DATA.DAT")
	if err != nil {
		t.Fatalf("NextVersion: %v", err)
	}
	if secondVersion != 2 {
		t.Errorf("NextVersion(DATA.DAT) after inserting v1 = %d, want 2", secondVersion)
	}

	fid2 := ondisk.Fid{Num: 51, Seq: 1}
	if err := dir.Insert("DATA.DAT", secondVersion, fid2, bm, ib); err != nil {
		t.Fatalf("Insert v%d: %v", secondVersion, err)
	}

	// Versions come back highest-first, matching EncodeDirectoryBlock's own
	// on-disk convention (see its doc comment) and Lookup's "version 0 means
	// highest" rule.
	want := []ondisk.DirEntry{
		{Name: "DATA.DAT", Version: 2, Fid: fid2},
		{Name: "DATA.DAT", Version: 1, Fid: fid1},
	}
	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("List() = %+v, want %+v", entries, want)
	}

	if got, err := dir.Lookup("DATA.DAT", 0); err != nil || got.Fid != fid2 {
		t.Errorf("Lookup(DATA.DAT, 0) = %+v, %v; want Fid %v, no error", got, err, fid2)
	}
}

// TestDirectoryInsertForcesDirectoryExtension inserts enough distinctly
// named entries that they can't all fit in the directory's first block,
// forcing Insert to grow the directory's own allocation via Extend --
// exactly the case the reference implementation's insert_ent() simply
// crashes on (see docs/PHASE-02.md's "what we're deliberately not porting"
// table) and this project handles as an ordinary case instead.
func TestDirectoryInsertForcesDirectoryExtension(t *testing.T) {
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

	dir := newWritableTestDirectory(t, dev, ib, "BIG.DIR")

	// Each "FILEnnnn.TXT;1" record needs 6 (header) + 12 (name) + 8 (one
	// version entry) = 26 bytes, so a single 512-byte block holds roughly
	// 19 of them; 60 comfortably forces at least a second (and likely a
	// third) block.
	const count = 60
	want := make([]ondisk.DirEntry, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("FILE%04d.TXT", i)
		fid := ondisk.Fid{Num: uint16(100 + i), Seq: 1}
		if err := dir.Insert(name, 1, fid, bm, ib); err != nil {
			t.Fatalf("Insert(%s) (#%d): %v", name, i, err)
		}
		want = append(want, ondisk.DirEntry{Name: name, Version: 1, Fid: fid})
	}

	if dir.Blocks() <= 1 {
		t.Fatalf("Blocks() after %d inserts = %d, want more than 1 (the directory should have needed to extend)", count, dir.Blocks())
	}

	byName := func(entries []ondisk.DirEntry) []ondisk.DirEntry {
		sorted := append([]ondisk.DirEntry(nil), entries...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
		return sorted
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := byName(entries), byName(want); !reflect.DeepEqual(got, want) {
		t.Errorf("List() after %d inserts = %+v, want %+v", count, got, want)
	}

	// Independent reopen must see exactly the same entries, across every
	// block the directory ended up using.
	vol := &Volume{Devices: []*Device{dev}}
	reopened, err := vol.OpenDirectory(dir.Header.Fid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}
	reentries, err := reopened.List()
	if err != nil {
		t.Fatalf("List (reopened): %v", err)
	}
	if got, want := byName(reentries), byName(want); !reflect.DeepEqual(got, want) {
		t.Errorf("List() after reopen = %+v, want %+v", got, want)
	}
}
