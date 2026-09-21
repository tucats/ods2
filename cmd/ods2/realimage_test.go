package main

// Optional validation against real, independently-created VMS disk
// images — not this project's own synthetic test fixtures, which could
// in principle share a systematic misunderstanding of the format with
// the code under test. There's no such image checked into this repo (see
// testdata/README.md), so these tests are skipped unless an environment
// variable points at one on the local machine; nothing here runs in CI.
//
// Set ODS2_TEST_IMAGE to a plain ODS-2 image file, and/or
// ODS2_TEST_IMAGE_RAWCD to a raw 2352-byte-sector CD-ROM dump, to enable
// the corresponding test. Both were confirmed, during this project's own
// development, against a real OpenVMS V5.5-2H4 installation CD: the
// master file directory's reserved system files (INDEXF.SYS, BITMAP.SYS,
// 000000.DIR, ...) decoded with exactly the Fids this project's design
// predicted from reading the reference C source, subdirectory navigation
// worked, and a real VFC-format file's carriage-control bytes (0x01 0x8D)
// decoded through FormatVFCRecord into correctly laid-out plain text.

import (
	"io"
	"os"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/rms"
	"github.com/tucats/ods2/volume"
)

func TestRealImagePlain(t *testing.T) {
	path := os.Getenv("ODS2_TEST_IMAGE")
	if path == "" {
		t.Skip("ODS2_TEST_IMAGE not set; skipping real-image validation")
	}
	validateRealImage(t, path)
}

func TestRealImageRawCD(t *testing.T) {
	path := os.Getenv("ODS2_TEST_IMAGE_RAWCD")
	if path == "" {
		t.Skip("ODS2_TEST_IMAGE_RAWCD not set; skipping real-image validation")
	}
	validateRealImage(t, path)
}

// validateRealImage mounts path, confirms the volume's master file
// directory contains the reserved system files every ODS-2 volume has
// (with the well-known Fids this project's design depends on), and reads
// at least one real file's content successfully, whatever record format
// it happens to be.
func validateRealImage(t *testing.T, path string) {
	t.Helper()

	c, err := diskimage.Open(path)
	if err != nil {
		t.Fatalf("diskimage.Open(%s): %v", path, err)
	}
	defer c.Close()

	vol, err := volume.Mount(c)
	if err != nil {
		t.Fatalf("volume.Mount: %v", err)
	}
	t.Logf("mounted volume label: %q", vol.Devices[0].Home.VolumeName)

	dir, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory(MasterFileDirectoryFid): %v", err)
	}

	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("master file directory is empty; expected at least the reserved system files")
	}

	wantReserved := map[string]ondisk.Fid{
		"INDEXF.SYS": ondisk.IndexFileFid,
		"BITMAP.SYS": {Num: 2, Seq: 2},
		"BADBLK.SYS": {Num: 3, Seq: 3},
		"000000.DIR": ondisk.MasterFileDirectoryFid,
	}
	found := make(map[string]bool)
	for _, e := range entries {
		if want, ok := wantReserved[e.Name]; ok {
			if e.Fid.Number() != want.Number() {
				t.Errorf("%s has file number %d, want %d", e.Name, e.Fid.Number(), want.Number())
			}
			found[e.Name] = true
		}
	}
	for name := range wantReserved {
		if !found[name] {
			t.Errorf("master file directory is missing the reserved file %s", name)
		}
	}

	// Read one real file's entire content, whatever format it turns out
	// to be, confirming rms.Reader doesn't error on genuine VMS data.
	for _, e := range entries {
		f, err := vol.OpenFID(e.Fid)
		if err != nil {
			t.Fatalf("OpenFID(%s): %v", e.Name, err)
		}
		if f.Header.IsDirectory() {
			continue
		}

		r, err := rms.NewReader(f)
		if err != nil {
			t.Fatalf("NewReader(%s): %v", e.Name, err)
		}
		records := 0
		for {
			_, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("reading %s: %v", e.Name, err)
			}
			records++
		}
		t.Logf("read %s (%v format): %d record(s)", e.Name, f.Header.RecordAttributes.Format, records)
		return
	}
	t.Fatal("found no ordinary (non-directory) file in the master file directory to read")
}
