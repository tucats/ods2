package session

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

const defaultDeviceName = "DUA0"

func TestCmdAnalyzeRequiresDiskQualifier(t *testing.T) {
	s := New()
	if err := cmdAnalyze(s, []string{defaultDeviceName}, Qualifiers{}); err == nil {
		t.Fatal("cmdAnalyze without /DISK: want error, got nil")
	}
}

func TestCmdAnalyzeUnknownDevice(t *testing.T) {
	s := New()
	if err := cmdAnalyze(s, []string{defaultDeviceName}, Qualifiers{"disk": ""}); err == nil {
		t.Fatal("cmdAnalyze on an unmounted device: want error, got nil")
	}
}

// TestCmdAnalyzeRejectsMultiDeviceVolume confirms ANALYZE/DISK refuses a
// multi-device volume set outright rather than silently analyzing (or,
// worse, /REPAIR-ing) only its first member — matching docs/PHASE-02.md's
// own scoping of ANALYZE/DISK to single-device volumes. The two Devices
// here don't need to be real/mountable: cmdAnalyze's own device-count
// check runs before either one would ever be touched.
func TestCmdAnalyzeRejectsMultiDeviceVolume(t *testing.T) {
	s := New()
	s.Volumes[defaultDeviceName] = &volume.Volume{Devices: []*volume.Device{{}, {}}}

	if err := cmdAnalyze(s, []string{defaultDeviceName}, Qualifiers{"disk": ""}); err == nil {
		t.Fatal("cmdAnalyze on a multi-device volume: want error, got nil")
	}
}

// newAnalyzeSessionFixture builds a freshly initialized, mounted-/WRITE
// volume via the ordinary cmdInitialize/cmdMount commands (exactly what an
// interactive user would type), then creates and flushes one ordinary file
// on it directly through package volume — there's no CLI-level command to
// do that yet (subtask 16, COPY host->volume, is a stretch goal that
// hasn't shipped) — so cmdAnalyze has real, on-disk, correctly-flushed
// allocation state to check.
func newAnalyzeSessionFixture(t *testing.T) (s *Session, key string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "analyze.dsk")
	s = New()
	s.Stdout = &bytes.Buffer{}

	if err := cmdInitialize(s, []string{path, "400", "ANALYZE"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdInitialize: %v", err)
	}

	key = defaultDeviceName

	if err := cmdMount(s, []string{key, path}, Qualifiers{"write": ""}); err != nil {
		t.Fatalf("cmdMount /write: %v", err)
	}

	vol := s.Volumes[key]

	t.Cleanup(func() {
		for _, dev := range vol.Devices {
			_ = dev.Container.Close()
		}
	})

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
	// See volume/analyze_test.go's own newAnalyzeTestVolume for why this
	// explicit flush is needed: CreateFile/Extend mark the bitmap dirty
	// but don't flush it themselves.
	if err := bm.Flush(); err != nil {
		t.Fatalf("Bitmap.Flush: %v", err)
	}

	if err := ib.Flush(); err != nil {
		t.Fatalf("IndexBitmap.Flush: %v", err)
	}

	return s, key
}

func TestCmdAnalyzeCleanVolumeReportsClean(t *testing.T) {
	s, key := newAnalyzeSessionFixture(t)
	out := s.Stdout.(*bytes.Buffer)
	out.Reset()

	if err := cmdAnalyze(s, []string{key}, Qualifiers{"disk": ""}); err != nil {
		t.Fatalf("cmdAnalyze: %v", err)
	}

	if !strings.Contains(out.String(), "ANALYZE-I-CLEAN") {
		t.Errorf("cmdAnalyze output = %q, want it to report clean", out.String())
	}
}

// TestCmdAnalyzeRepairFlow drives ANALYZE/DISK's full report/repair/
// re-verify cycle through the command layer, matching docs/PHASE-02.md
// subtask 15's own acceptance scenario: a hand-corrupted BITMAP.SYS is
// first reported (without /REPAIR, leaving it untouched), then corrected
// (with /REPAIR), then a follow-up ANALYZE/DISK with no /REPAIR reports
// clean. The underlying analysis/repair logic itself is already covered in
// detail by volume/analyze_test.go; this only needs to confirm cmdAnalyze
// wires qualifiers and output through correctly.
func TestCmdAnalyzeRepairFlow(t *testing.T) {
	s, key := newAnalyzeSessionFixture(t)
	dev := s.Volumes[key].Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}

	freeExtent, err := bm.FindFree(1)
	if err != nil {
		t.Fatalf("FindFree: %v", err)
	}

	if err := bm.MarkAllocated(freeExtent); err != nil {
		t.Fatalf("MarkAllocated: %v", err)
	}

	if err := bm.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	out := s.Stdout.(*bytes.Buffer)

	out.Reset()

	if err := cmdAnalyze(s, []string{key}, Qualifiers{"disk": ""}); err != nil {
		t.Fatalf("cmdAnalyze (report only): %v", err)
	}

	if !strings.Contains(out.String(), "ANALYZE-W-DISCREP") {
		t.Errorf("cmdAnalyze output = %q, want it to report a discrepancy", out.String())
	}

	out.Reset()

	if err := cmdAnalyze(s, []string{key}, Qualifiers{"disk": "", "repair": ""}); err != nil {
		t.Fatalf("cmdAnalyze /repair: %v", err)
	}

	if !strings.Contains(out.String(), "ANALYZE-W-DISCREP") || !strings.Contains(out.String(), "repaired") {
		t.Errorf("cmdAnalyze /repair output = %q, want it to report a repaired discrepancy", out.String())
	}

	out.Reset()
	
	if err := cmdAnalyze(s, []string{key}, Qualifiers{"disk": ""}); err != nil {
		t.Fatalf("cmdAnalyze (follow-up): %v", err)
	}

	if !strings.Contains(out.String(), "ANALYZE-I-CLEAN") {
		t.Errorf("cmdAnalyze follow-up output = %q, want it to report clean", out.String())
	}
}
