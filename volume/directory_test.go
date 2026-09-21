package volume

import (
	"reflect"
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
