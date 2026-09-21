package volume

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// analyzeTestVolume bundles what analyze_test.go's tests need from a
// freshly built, freshly written-to volume: the mounted Device to run
// AnalyzeDisk/RepairDisk against, the file just created on it (so a test
// can corrupt a cluster it genuinely owns), and the host path it lives on
// (so a test can reopen it independently, e.g. read-only).
type analyzeTestVolume struct {
	path string
	vol  *Volume
	dev  *Device
	file *File
}

// newAnalyzeTestVolume builds a fresh volume via Initialize (subtask 12)
// and creates one ordinary file on it via CreateFile (subtask 10) -- the
// standard write-path fixture-construction path this phase's own testing
// strategy calls for (docs/PHASE-02.md), rather than one-off hand-rolled
// bytes. The volume is left mounted /WRITE (the returned Device's
// container is a diskimage.WritableContainer) with the just-created file's
// single data block genuinely allocated -- exactly the shape
// AnalyzeDisk/RepairDisk's own tests need to corrupt a real cluster
// against.
func newAnalyzeTestVolume(t *testing.T) analyzeTestVolume {
	t.Helper()

	path := filepath.Join(t.TempDir(), "analyze.dsk")
	c, err := diskimage.Create(path, 400)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := Initialize(c, InitializeOptions{Label: "ANALYZE"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	dev := vol.Devices[0]

	dir, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory: %v", err)
	}

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}
	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap: %v", err)
	}

	f, err := vol.CreateFile(dir, "HELLO.TXT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := f.WriteBlock(1, bytes.Repeat([]byte{0x42}, ondisk.BlockSize)); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// CreateFile/Extend mark bm/ib dirty in memory but don't flush them
	// themselves (see docs/PHASE-02.md's "Caching strategy" -- that's each
	// CALLER's own responsibility once it's done mutating; today the only
	// caller that actually does this is Dismount, since subtask 16's
	// CLI-level write command is a stretch goal that hasn't shipped).
	// AnalyzeDisk compares against BITMAP.SYS's real on-disk bits, so an
	// un-flushed allocation would otherwise look exactly like the
	// corruption this package's own tests are trying to detect on
	// purpose -- flush explicitly here so the fixture starts genuinely
	// clean.
	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}
	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	return analyzeTestVolume{path: path, vol: vol, dev: dev, file: f}
}

// TestAnalyzeDiskCleanOnFreshlyInitializedVolume is this subtask's primary
// acceptance test (docs/PHASE-02.md subtask 15's own test plan): a
// known-good INITIALIZEd-and-written-to volume reports zero discrepancies.
func TestAnalyzeDiskCleanOnFreshlyInitializedVolume(t *testing.T) {
	tv := newAnalyzeTestVolume(t)

	report, err := AnalyzeDisk(tv.dev)
	if err != nil {
		t.Fatalf("AnalyzeDisk: %v", err)
	}
	if !report.Clean() {
		t.Errorf("AnalyzeDisk found %d discrepancies on a freshly initialized volume, want 0: %v", len(report.Discrepancies), report.Discrepancies)
	}
	if report.TotalClusters == 0 {
		t.Error("TotalClusters = 0, want the volume's actual cluster count")
	}
}

// TestAnalyzeDiskRequiresMountedDevice confirms AnalyzeDisk fails cleanly
// on a Device that was never mounted (dev.IndexFile is nil), rather than
// panicking on a nil dereference.
func TestAnalyzeDiskRequiresMountedDevice(t *testing.T) {
	_, err := AnalyzeDisk(&Device{})
	if err == nil {
		t.Fatal("AnalyzeDisk on an unmounted device: want an error, got nil")
	}
}

// corrupt hand-flips two clusters' bits directly through tv's already
// cached Bitmap (bypassing AnalyzeDisk/RepairDisk entirely, so the test
// fixture's corruption is independent of the code under test): the
// just-created file's own data cluster is marked incorrectly FREE (a
// corruption risk -- live data BITMAP.SYS claims is available), and one
// genuinely free cluster is marked incorrectly ALLOCATED (merely
// reclaimable). Returns the two clusters' 0-based cluster numbers.
func (tv analyzeTestVolume) corrupt(t *testing.T) (usedCluster, freeCluster uint32) {
	t.Helper()

	bm, err := tv.dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}

	// Find the free cluster to (wrongly) mark allocated BEFORE freeing the
	// used one below -- otherwise FindFree's first-fit scan could pick the
	// very cluster this function just freed, and the two edits would
	// cancel out into a net-zero, undetectable "corruption."
	freeExtent, err := bm.FindFree(1)
	if err != nil {
		t.Fatalf("FindFree: %v", err)
	}
	if err := bm.MarkAllocated(freeExtent); err != nil {
		t.Fatalf("MarkAllocated(free extent): %v", err)
	}

	usedExtent := tv.file.Extents[0].Extent
	if err := bm.MarkFree(usedExtent); err != nil {
		t.Fatalf("MarkFree(used extent): %v", err)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	return usedExtent.StartLBN, freeExtent.StartLBN // ClusterSize is 1 (Initialize's default), so LBN == cluster number here.
}

// TestAnalyzeDiskDetectsCorruption hand-corrupts BITMAP.SYS in both
// directions the subtask's own test plan calls for (a bit flipped to
// "free" under a block a real file uses, and a bit left "allocated" under
// space nothing uses) and confirms AnalyzeDisk reports exactly those two
// discrepancies, correctly classified.
func TestAnalyzeDiskDetectsCorruption(t *testing.T) {
	tv := newAnalyzeTestVolume(t)
	usedCluster, freeCluster := tv.corrupt(t)
	fileNumber := tv.file.Header.Fid.Number()

	report, err := AnalyzeDisk(tv.dev)
	if err != nil {
		t.Fatalf("AnalyzeDisk: %v", err)
	}
	if len(report.Discrepancies) != 2 {
		t.Fatalf("AnalyzeDisk found %d discrepancies, want 2: %v", len(report.Discrepancies), report.Discrepancies)
	}

	byCluster := make(map[uint32]BitmapDiscrepancy, 2)
	for _, d := range report.Discrepancies {
		byCluster[d.Cluster] = d
	}

	used, ok := byCluster[usedCluster]
	if !ok {
		t.Fatalf("no discrepancy reported for the used cluster %d", usedCluster)
	}
	if !used.ShouldBeAllocated {
		t.Errorf("used cluster %d: ShouldBeAllocated = false, want true", usedCluster)
	}
	if used.ClaimedByFile != fileNumber {
		t.Errorf("used cluster %d: ClaimedByFile = %d, want %d", usedCluster, used.ClaimedByFile, fileNumber)
	}

	free, ok := byCluster[freeCluster]
	if !ok {
		t.Fatalf("no discrepancy reported for the free cluster %d", freeCluster)
	}
	if free.ShouldBeAllocated {
		t.Errorf("free cluster %d: ShouldBeAllocated = true, want false", freeCluster)
	}
}

// TestRepairDiskFixesDiscrepancies confirms /REPAIR's acceptance scenario:
// after RepairDisk, a follow-up AnalyzeDisk (a completely independent
// on-disk re-read, not just an in-memory check) reports clean.
func TestRepairDiskFixesDiscrepancies(t *testing.T) {
	tv := newAnalyzeTestVolume(t)
	tv.corrupt(t)

	repaired, err := RepairDisk(tv.dev)
	if err != nil {
		t.Fatalf("RepairDisk: %v", err)
	}
	if len(repaired.Discrepancies) != 2 {
		t.Fatalf("RepairDisk's own report found %d discrepancies, want 2", len(repaired.Discrepancies))
	}

	followUp, err := AnalyzeDisk(tv.dev)
	if err != nil {
		t.Fatalf("AnalyzeDisk (follow-up): %v", err)
	}
	if !followUp.Clean() {
		t.Errorf("AnalyzeDisk after RepairDisk found %d discrepancies, want 0: %v", len(followUp.Discrepancies), followUp.Discrepancies)
	}
}

// TestRepairDiskOnCleanVolumeIsNoop confirms RepairDisk doesn't touch
// BITMAP.SYS at all when there's nothing to fix -- it never even needs
// write access in that case, since it returns before calling dev.Bitmap().
func TestRepairDiskOnCleanVolumeIsNoop(t *testing.T) {
	tv := newAnalyzeTestVolume(t)

	report, err := RepairDisk(tv.dev)
	if err != nil {
		t.Fatalf("RepairDisk: %v", err)
	}
	if !report.Clean() {
		t.Errorf("RepairDisk on an already-clean volume found %d discrepancies, want 0", len(report.Discrepancies))
	}
}

// TestRepairDiskRequiresWrite confirms RepairDisk fails with a clear error
// on a read-only-mounted device, rather than silently doing nothing or
// panicking -- AnalyzeDisk itself must still succeed read-only (it's
// exercised first, internally, by RepairDisk), so this needs a volume that
// actually has a discrepancy to correct, or RepairDisk would short-circuit
// before ever touching write-only state (see TestRepairDiskOnCleanVolumeIsNoop).
func TestRepairDiskRequiresWrite(t *testing.T) {
	tv := newAnalyzeTestVolume(t)
	tv.corrupt(t)

	ro, err := diskimage.Open(tv.path)
	if err != nil {
		t.Fatalf("diskimage.Open: %v", err)
	}
	defer func() { _ = ro.Close() }()

	roVol, err := Mount(ro)
	if err != nil {
		t.Fatalf("Mount (read-only): %v", err)
	}

	if _, err := RepairDisk(roVol.Devices[0]); err == nil {
		t.Fatal("RepairDisk on a read-only-mounted device: want an error, got nil")
	}
}
