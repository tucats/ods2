package volume

import (
	"reflect"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

func TestDirectoryRejectsNonDirectoryFile(t *testing.T) {
	vol, c := newTestVolume(t)

	fid := ondisk.Fid{Num: 30, Seq: 1}
	c.putBlock(fileHeaderLBN(fid.Num), buildFileHeaderBytes(t, fileHeaderFixture{fid: fid}))

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

	block := buildDirBlock(
		buildDirRecordBytes("README.TXT", []uint16{1, 2}, []ondisk.Fid{readmeFid1, readmeFid2}),
		buildDirRecordBytes("DATA.DAT", []uint16{1}, []ondisk.Fid{dataFid}),
	)
	c.putBlock(260, block)

	dirFid := ondisk.Fid{Num: 31, Seq: 1}
	c.putBlock(fileHeaderLBN(dirFid.Num), buildFileHeaderBytes(t, fileHeaderFixture{
		fid:            dirFid,
		fileChar:       ondisk.FchDirectory,
		highestBlock:   1,
		mapOffsetWords: 55,
		mapBytes:       encodeExtentFormat2(1, 260),
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
