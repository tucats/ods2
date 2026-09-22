package filespec

import (
	"sort"
	"testing"

	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// This file's tests build a small volume with a directory tree:
//
//	[000000]                 (the master file directory, file #4)
//	  README.TXT;1           (file #20)
//	  DATA.DAT;1              (file #21)
//	  SUBDIR.DIR;1            (file #10)
//	    NESTED.TXT;1          (file #11)
//	    DEEPER.DIR;1          (file #12)
//	      LEAF.TXT;1          (file #13)
//
// The index bitmap sits at LBN 5, is 3 blocks long, and INDEXF.SYS
// describes one big extent starting there -- the same layout
// volume/file_test.go's newTestVolume uses, which works out to file N's
// header living at LBN N+7 (see globFileHeaderLBN). Directory data blocks
// are placed at arbitrary LBNs comfortably outside that header range.
const (
	globTestIdxBitmapLBN  = 5
	globTestIdxBitmapVBN  = 1
	globTestIdxBitmapSize = 3
)

func globFileHeaderLBN(fileNum uint16) uint32 {
	idxblk := uint32(fileNum) - 1 + globTestIdxBitmapVBN + globTestIdxBitmapSize

	return globTestIdxBitmapLBN + (idxblk - 1)
}

func newGlobTestVolume(t *testing.T) *volume.Volume {
	t.Helper()

	c := odstest.NewMemContainer(500)
	c.PutBlock(1, odstest.BuildHomeBlockBytes(t, odstest.HomeBlockFixture{
		HomeLBN:       1,
		Rvn:           1,
		IdxBitmapVBN:  globTestIdxBitmapVBN,
		IdxBitmapLBN:  globTestIdxBitmapLBN,
		IdxBitmapSize: globTestIdxBitmapSize,
	}))

	// INDEXF.SYS itself (file #1): one big extent, starting at the index
	// bitmap's own LBN, comfortably covering every file header used below.
	c.PutBlock(globFileHeaderLBN(ondisk.IndexFileFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            ondisk.IndexFileFid,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(300, globTestIdxBitmapLBN),
	}))

	fid := func(n uint16) ondisk.Fid { return ondisk.Fid{Num: n, Seq: 1} }

	// [000000] (file #4, the MFD): README.TXT, DATA.DAT, SUBDIR.DIR
	mfdFid := ondisk.MasterFileDirectoryFid

	const mfdDataLBN = 200

	c.PutBlock(globFileHeaderLBN(mfdFid.Num), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            mfdFid,
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, mfdDataLBN),
	}))
	c.PutBlock(mfdDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("README.TXT", []uint16{1}, []ondisk.Fid{fid(20)}),
		odstest.BuildDirRecordBytes("DATA.DAT", []uint16{1}, []ondisk.Fid{fid(21)}),
		odstest.BuildDirRecordBytes("SUBDIR.DIR", []uint16{1}, []ondisk.Fid{fid(10)}),
	))
	c.PutBlock(globFileHeaderLBN(20), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: fid(20)}))
	c.PutBlock(globFileHeaderLBN(21), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: fid(21)}))

	// SUBDIR.DIR (file #10): NESTED.TXT, DEEPER.DIR
	const subdirDataLBN = 201

	c.PutBlock(globFileHeaderLBN(10), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            fid(10),
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, subdirDataLBN),
	}))
	c.PutBlock(subdirDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("NESTED.TXT", []uint16{1}, []ondisk.Fid{fid(11)}),
		odstest.BuildDirRecordBytes("DEEPER.DIR", []uint16{1}, []ondisk.Fid{fid(12)}),
	))
	c.PutBlock(globFileHeaderLBN(11), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: fid(11)}))

	// DEEPER.DIR (file #12): LEAF.TXT
	const deeperDataLBN = 202

	c.PutBlock(globFileHeaderLBN(12), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{
		Fid:            fid(12),
		FileChar:       ondisk.FchDirectory,
		HighestBlock:   1,
		MapOffsetWords: 55,
		MapBytes:       odstest.EncodeExtentFormat2(1, deeperDataLBN),
	}))
	c.PutBlock(deeperDataLBN, odstest.BuildDirBlock(
		odstest.BuildDirRecordBytes("LEAF.TXT", []uint16{1}, []ondisk.Fid{fid(13)}),
	))
	c.PutBlock(globFileHeaderLBN(13), odstest.BuildFileHeaderBytes(t, odstest.FileHeaderFixture{Fid: fid(13)}))

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	return vol
}

func matchNames(matches []Match) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name + "." + m.Type
	}

	sort.Strings(names)

	return names
}

func TestGlobRootWildcard(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Name: "*", Type: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	got := matchNames(matches)
	want := []string{"DATA.DAT", "README.TXT", "SUBDIR.DIR"}

	if len(got) != len(want) {
		t.Fatalf("Glob() = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Glob() = %v, want %v", got, want)

			break
		}
	}
}

func TestGlobEmptyNameTypeDefaultsToWildcard(t *testing.T) {
	vol := newGlobTestVolume(t)

	// Name/Type left unset entirely -- Glob should behave as if "*.*"
	// were given, the same way DCL lists a whole directory when you don't
	// type a file name.
	matches, err := Glob(vol, Spec{})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(matches) != 3 {
		t.Fatalf("Glob() with empty Name/Type found %d matches, want 3", len(matches))
	}
}

func TestGlobExactName(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Name: "README", Type: "TXT"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(matches) != 1 || matches[0].Name != "README" {
		t.Fatalf("Glob() = %+v, want a single README.TXT match", matches)
	}
}

func TestGlobNamePattern(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Name: "*", Type: "TXT"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	got := matchNames(matches)
	want := []string{"README.TXT"}

	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Glob(*.TXT) = %v, want %v", got, want)
	}
}

func TestGlobIntoSubdirectory(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Dirs: []string{"SUBDIR"}, Name: "*", Type: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	got := matchNames(matches)
	want := []string{"DEEPER.DIR", "NESTED.TXT"}

	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Glob([SUBDIR]*.*) = %v, want %v", got, want)
	}

	if len(matches[0].Dirs) == 0 || matches[0].Dirs[0] != "SUBDIR" {
		t.Errorf("Match.Dirs = %v, want to start with SUBDIR", matches[0].Dirs)
	}
}

func TestGlobWildcardDirectoryComponent(t *testing.T) {
	vol := newGlobTestVolume(t)

	// "SUB*" should match SUBDIR just like an exact name would.
	matches, err := Glob(vol, Spec{Dirs: []string{"SUB*"}, Name: "*", Type: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(matches) != 2 {
		t.Fatalf("Glob([SUB*]*.*) found %d matches, want 2", len(matches))
	}
}

func TestGlobRecursive(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Dirs: []string{"SUBDIR"}, Recursive: true, Name: "*", Type: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	got := matchNames(matches)
	want := []string{"DEEPER.DIR", "LEAF.TXT", "NESTED.TXT"}

	if len(got) != len(want) {
		t.Fatalf("Glob([SUBDIR...]*.*) = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Glob([SUBDIR...]*.*) = %v, want %v", got, want)

			break
		}
	}
}

func TestGlobRecursiveFromRoot(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Recursive: true, Name: "*", Type: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	
	got := matchNames(matches)
	want := []string{"DATA.DAT", "DEEPER.DIR", "LEAF.TXT", "NESTED.TXT", "README.TXT", "SUBDIR.DIR"}
	
	if len(got) != len(want) {
		t.Fatalf("Glob([...]*.*) = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Glob([...]*.*) = %v, want %v", got, want)

			break
		}
	}
}

func TestGlobNoMatches(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Name: "NOSUCHFILE", Type: "TXT"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(matches) != 0 {
		t.Errorf("Glob() = %+v, want no matches", matches)
	}
}

func TestGlobNonexistentDirectory(t *testing.T) {
	vol := newGlobTestVolume(t)

	matches, err := Glob(vol, Spec{Dirs: []string{"NOSUCHDIR"}, Name: "*", Type: "*"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(matches) != 0 {
		t.Errorf("Glob([NOSUCHDIR]*.*) = %+v, want no matches", matches)
	}
}

func TestGlobInvalidVersionSelector(t *testing.T) {
	vol := newGlobTestVolume(t)

	if _, err := Glob(vol, Spec{Name: "*", Type: "*", Version: "garbage"}); err == nil {
		t.Fatal("Glob with an invalid version selector: want error, got nil")
	}
}

func TestResolveDirectoryRoot(t *testing.T) {
	vol := newGlobTestVolume(t)

	dir, err := ResolveDirectory(vol, nil)
	if err != nil {
		t.Fatalf("ResolveDirectory(nil): %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("root directory has %d entries, want 3", len(entries))
	}
}

func TestResolveDirectoryNested(t *testing.T) {
	vol := newGlobTestVolume(t)

	dir, err := ResolveDirectory(vol, []string{"SUBDIR", "DEEPER"})
	if err != nil {
		t.Fatalf("ResolveDirectory([SUBDIR.DEEPER]): %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 1 || entries[0].Name != "LEAF.TXT" {
		t.Fatalf("ResolveDirectory([SUBDIR.DEEPER]) entries = %+v, want just LEAF.TXT", entries)
	}
}

func TestResolveDirectoryIsCaseInsensitive(t *testing.T) {
	vol := newGlobTestVolume(t)

	dir, err := ResolveDirectory(vol, []string{"subdir"})
	if err != nil {
		t.Fatalf("ResolveDirectory([subdir]): %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("ResolveDirectory([subdir]) entries = %+v, want 2", entries)
	}
}

func TestResolveDirectoryNotFound(t *testing.T) {
	vol := newGlobTestVolume(t)

	if _, err := ResolveDirectory(vol, []string{"NOSUCHDIR"}); err == nil {
		t.Fatal("ResolveDirectory([NOSUCHDIR]): want error, got nil")
	}
}

func TestResolveDirectoryRejectsAFile(t *testing.T) {
	vol := newGlobTestVolume(t)

	// README.TXT is an ordinary file, not a directory -- SUBDIR.DIR is
	// what walkDirs' own subdirectory match looks for (names ending in
	// ".DIR"), so a plain file name never resolves to anything here.
	if _, err := ResolveDirectory(vol, []string{"README"}); err == nil {
		t.Fatal("ResolveDirectory([README]) naming a plain file: want error, got nil")
	}
}
