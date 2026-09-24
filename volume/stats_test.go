package volume

import (
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// TestStatsOnFreshlyInitializedVolume confirms Stats reports zero files
// and a plausible free/total block count on a volume nothing has ever
// written a file to.
func TestStatsOnFreshlyInitializedVolume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.dsk")
	c, err := diskimage.Create(path, 400)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := Initialize(c, InitializeOptions{Label: "STATS"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	stats, err := Stats(vol.Devices[0])
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if stats.FileCount != 0 {
		t.Errorf("FileCount on a freshly initialized volume = %d, want 0", stats.FileCount)
	}
	if stats.TotalBlocks == 0 {
		t.Error("TotalBlocks = 0, want the volume's real size")
	}
	if stats.FreeBlocks == 0 || stats.FreeBlocks > stats.TotalBlocks {
		t.Errorf("FreeBlocks = %d, want a nonzero value no greater than TotalBlocks (%d)", stats.FreeBlocks, stats.TotalBlocks)
	}
	if stats.ClusterSize == 0 {
		t.Error("ClusterSize = 0, want the volume's real cluster size")
	}
	if stats.MaxFiles == 0 {
		t.Error("MaxFiles = 0, want the volume's real header-slot count")
	}
}

// TestStatsReflectsCreatedFile confirms Stats' FileCount/FreeBlocks track
// a real file actually created and written on the volume, using the same
// newAnalyzeTestVolume fixture (analyze_test.go) AnalyzeDisk's own tests
// build: an INITIALIZEd volume with one file (HELLO.TXT) created and one
// block written to it, bitmaps flushed.
func TestStatsReflectsCreatedFile(t *testing.T) {
	tv := newAnalyzeTestVolume(t)

	empty, err := Stats(tv.dev)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if empty.FileCount != 1 {
		t.Errorf("FileCount after creating one file = %d, want 1", empty.FileCount)
	}
	if empty.FreeBlocks >= empty.TotalBlocks {
		t.Errorf("FreeBlocks (%d) >= TotalBlocks (%d), want the created file's block(s) reflected as no longer free", empty.FreeBlocks, empty.TotalBlocks)
	}
}

// TestStatsWorksReadOnly confirms Stats succeeds against a device mounted
// read-only (plain diskimage.Open, not OpenWritable) -- the whole reason
// Stats is built on loadBitmap rather than OpenBitmap (see loadBitmap's
// own doc comment): a volume MOUNT'd /NOWRITE must still be able to
// report free-space/file-count statistics.
func TestStatsWorksReadOnly(t *testing.T) {
	tv := newAnalyzeTestVolume(t)

	ro, err := diskimage.Open(tv.path)
	if err != nil {
		t.Fatalf("diskimage.Open: %v", err)
	}
	defer func() { _ = ro.Close() }()

	roVol, err := Mount(ro)
	if err != nil {
		t.Fatalf("Mount (read-only): %v", err)
	}

	stats, err := Stats(roVol.Devices[0])
	if err != nil {
		t.Fatalf("Stats on a read-only-mounted device: %v", err)
	}

	if stats.FileCount != 1 {
		t.Errorf("FileCount (read-only) = %d, want 1", stats.FileCount)
	}
}

// TestStatsRequiresMount confirms Stats reports a clear error, rather than
// panicking, against a Device that was never actually Mount()ed (no
// IndexFile) -- matching AnalyzeDisk's own precondition check.
func TestStatsRequiresMount(t *testing.T) {
	if _, err := Stats(&Device{Home: ondisk.HomeBlock{}}); err == nil {
		t.Fatal("Stats on an unmounted device: want an error, got nil")
	}
}
