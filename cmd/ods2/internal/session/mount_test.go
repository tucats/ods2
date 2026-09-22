package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/internal/odstest"
	"github.com/tucats/ods2/ondisk"
)

func TestSplitDeviceList(t *testing.T) {
	got := splitDeviceList("DUA0:, DUA1: ,DUA2:")
	want := []string{"DUA0:", "DUA1:", "DUA2:"}
	if len(got) != len(want) {
		t.Fatalf("splitDeviceList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitDeviceList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// newMountableTestContainer builds a minimal valid ODS-2 volume in
// memory, the same layout volume package's own tests use.
func newMountableTestContainer(t *testing.T) *odstest.MemContainer {
	t.Helper()
	return odstest.NewMountableContainer(t, 20, odstest.HomeBlockFixture{Rvn: 1})
}

func TestMountContainersRegistersVolume(t *testing.T) {
	s := New()
	var out bytes.Buffer
	s.Stdout = &out

	c := newMountableTestContainer(t)
	if err := mountContainers(s, []string{"DUA0:"}, []diskimage.Container{c}); err != nil {
		t.Fatalf("mountContainers: %v", err)
	}

	if _, ok := s.Volumes["DUA0"]; !ok {
		t.Errorf("Volumes = %v, want a DUA0 entry", s.Volumes)
	}
	if out.Len() == 0 {
		t.Error("mountContainers printed no confirmation message")
	}
}

func TestMountContainersSetsDefaultOnFirstMount(t *testing.T) {
	s := New()
	s.Stdout = &bytes.Buffer{}

	c := newMountableTestContainer(t)
	if err := mountContainers(s, []string{"DUA0:"}, []diskimage.Container{c}); err != nil {
		t.Fatalf("mountContainers: %v", err)
	}

	if s.Default.Device != "DUA0" {
		t.Errorf("Default.Device = %q, want %q", s.Default.Device, "DUA0")
	}
}

func TestMountContainersDoesNotOverrideExistingDefault(t *testing.T) {
	s := New()
	s.Stdout = &bytes.Buffer{}
	s.Default.Device = "DUB1"

	c := newMountableTestContainer(t)
	if err := mountContainers(s, []string{"DUA0:"}, []diskimage.Container{c}); err != nil {
		t.Fatalf("mountContainers: %v", err)
	}

	if s.Default.Device != "DUB1" {
		t.Errorf("Default.Device = %q, want unchanged %q", s.Default.Device, "DUB1")
	}
}

func TestDismountUnknownDevice(t *testing.T) {
	s := New()
	if err := cmdDismount(s, []string{"DUA0:"}, nil); err == nil {
		t.Fatal("cmdDismount on an unmounted device: want error, got nil")
	}
}

func TestMountThenDismount(t *testing.T) {
	s := New()
	s.Stdout = &bytes.Buffer{}

	c := newMountableTestContainer(t)
	if err := mountContainers(s, []string{"DUA0:"}, []diskimage.Container{c}); err != nil {
		t.Fatalf("mountContainers: %v", err)
	}

	if err := cmdDismount(s, []string{"DUA0:"}, nil); err != nil {
		t.Fatalf("cmdDismount: %v", err)
	}
	if _, ok := s.Volumes["DUA0"]; ok {
		t.Error("Volumes still contains DUA0 after dismount")
	}
}

// dumpContainer reads every block of an in-memory container into one
// contiguous byte slice, for writing out to a real file so cmdMount's
// actual disk-image-opening path can be exercised end to end.
func dumpContainer(t *testing.T, c *odstest.MemContainer) []byte {
	t.Helper()
	buf := make([]byte, 0, int(c.Blocks())*ondisk.BlockSize)
	block := make([]byte, ondisk.BlockSize)
	for i := uint32(0); i < c.Blocks(); i++ {
		if err := c.ReadBlock(i, block); err != nil {
			t.Fatalf("ReadBlock(%d): %v", i, err)
		}
		buf = append(buf, block...)
	}
	return buf
}

func TestCmdMountEndToEnd(t *testing.T) {
	c := newMountableTestContainer(t)
	path := filepath.Join(t.TempDir(), "test.img")
	if err := os.WriteFile(path, dumpContainer(t, c), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := New()
	var out bytes.Buffer
	s.Stdout = &out

	if err := cmdMount(s, []string{"DUA0", path}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount: %v", err)
	}
	// cmdMount leaves the image file open for as long as the volume stays
	// mounted, matching real usage — but on Windows, unlike Unix, a file
	// still open by this process can't be deleted, and t.TempDir()'s own
	// cleanup would otherwise fail trying to remove it. Close it out
	// explicitly once the test itself is done with it.
	t.Cleanup(func() {
		for _, vol := range s.Volumes {
			for _, dev := range vol.Devices {
				_ = dev.Container.Close()
			}
		}
	})

	if _, ok := s.Volumes["DUA0"]; !ok {
		t.Errorf("Volumes = %v, want a DUA0 entry", s.Volumes)
	}
}

func TestCmdMountNonexistentFile(t *testing.T) {
	s := New()
	if err := cmdMount(s, []string{"DUA0", filepath.Join(t.TempDir(), "nope.img")}, Qualifiers{}); err == nil {
		t.Fatal("cmdMount on a nonexistent file: want error, got nil")
	}
}

func TestCmdMountMismatchedDeviceAndContainerCounts(t *testing.T) {
	s := New()
	if err := cmdMount(s, []string{"DUA0,DUA1", "single.img"}, Qualifiers{}); err == nil {
		t.Fatal("cmdMount with 2 devices but 1 container: want error, got nil")
	}
}

// writeVolumeSetImages builds n mountable test images, one per relative
// volume number 1..n (the order volume.Mount requires a volume set's
// member containers to be given in), and writes each out to its own host
// file, returning their paths in that same order.
func writeVolumeSetImages(t *testing.T, n int) []string {
	t.Helper()
	paths := make([]string, n)
	for i := range n {
		c := odstest.NewMountableContainer(t, 20, odstest.HomeBlockFixture{Rvn: uint16(i + 1)})
		paths[i] = writeTestImage(t, fmt.Sprintf("member%d.img", i), dumpContainer(t, c))
	}
	return paths
}

// TestCmdMountSynthesizesDeviceNamesFromBaseUnit confirms that giving a
// single device name with several containers expands the device name into
// one per container, numbering from the unit already on the given name —
// "mount DUA1 foo.dsk,bar.dsk" mounts foo.dsk as DUA1 and bar.dsk as DUA2.
func TestCmdMountSynthesizesDeviceNamesFromBaseUnit(t *testing.T) {
	paths := writeVolumeSetImages(t, 2)

	s := New()
	s.Stdout = &bytes.Buffer{}
	if err := cmdMount(s, []string{"DUA1", strings.Join(paths, ",")}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount: %v", err)
	}
	t.Cleanup(func() {
		for _, vol := range s.Volumes {
			for _, dev := range vol.Devices {
				_ = dev.Container.Close()
			}
		}
	})

	if _, ok := s.Volumes["DUA1"]; !ok {
		t.Errorf("Volumes = %v, want a DUA1 entry (foo.dsk mounted as DUA1)", s.Volumes)
	}
	if _, ok := s.Volumes["DUA2"]; ok {
		t.Error("Volumes has a DUA2 entry, but only the first device name of a volume set is registered as a key")
	}
}

// TestCmdMountSynthesizesDeviceNamesFromZero confirms that a base device
// name with no unit number at all (e.g. "DKA") synthesizes unit numbers
// starting at 0.
func TestCmdMountSynthesizesDeviceNamesFromZero(t *testing.T) {
	paths := writeVolumeSetImages(t, 2)

	s := New()
	s.Stdout = &bytes.Buffer{}
	if err := cmdMount(s, []string{"DKA", strings.Join(paths, ",")}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount: %v", err)
	}
	t.Cleanup(func() {
		for _, vol := range s.Volumes {
			for _, dev := range vol.Devices {
				_ = dev.Container.Close()
			}
		}
	})

	if _, ok := s.Volumes["DKA0"]; !ok {
		t.Errorf("Volumes = %v, want a DKA0 entry (first container mounted as DKA0)", s.Volumes)
	}
}

// TestCmdMountSynthesizesDeviceNamesMultiDigitUnit confirms the base
// name's unit number is parsed as the full run of trailing digits, not
// just its last digit — "DUA10" must start numbering at unit 10, not
// wrap around to a single stray trailing digit.
func TestCmdMountSynthesizesDeviceNamesMultiDigitUnit(t *testing.T) {
	paths := writeVolumeSetImages(t, 2)

	s := New()
	s.Stdout = &bytes.Buffer{}
	if err := cmdMount(s, []string{"DUA10", strings.Join(paths, ",")}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount: %v", err)
	}
	t.Cleanup(func() {
		for _, vol := range s.Volumes {
			for _, dev := range vol.Devices {
				_ = dev.Container.Close()
			}
		}
	})

	if _, ok := s.Volumes["DUA10"]; !ok {
		t.Errorf("Volumes = %v, want a DUA10 entry", s.Volumes)
	}
}

// writeTestImage dumps an in-memory test container out to a real host file,
// the same way TestCmdMountEndToEnd does, so /WRITE can be exercised
// through cmdMount's actual diskimage.OpenWritable path rather than an
// in-memory fake (odstest.MemContainer doesn't implement WriteBlock, so it
// can't stand in for a WritableContainer).
func writeTestImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestCmdMountWriteQualifierOpensWritableContainer(t *testing.T) {
	c := newMountableTestContainer(t)
	path := writeTestImage(t, "test.img", dumpContainer(t, c))

	s := New()
	s.Stdout = &bytes.Buffer{}
	if err := cmdMount(s, []string{"DUA0", path}, Qualifiers{"write": ""}); err != nil {
		t.Fatalf("cmdMount with /write: %v", err)
	}
	t.Cleanup(func() {
		for _, vol := range s.Volumes {
			for _, dev := range vol.Devices {
				_ = dev.Container.Close()
			}
		}
	})

	vol := s.Volumes["DUA0"]
	if vol == nil {
		t.Fatalf("Volumes = %v, want a DUA0 entry", s.Volumes)
	}
	if _, ok := vol.Devices[0].Container.(diskimage.WritableContainer); !ok {
		t.Errorf("Devices[0].Container = %T, want a diskimage.WritableContainer (mounted /write)", vol.Devices[0].Container)
	}
}

func TestCmdMountWithoutWriteQualifierIsReadOnly(t *testing.T) {
	c := newMountableTestContainer(t)
	path := writeTestImage(t, "test.img", dumpContainer(t, c))

	s := New()
	s.Stdout = &bytes.Buffer{}
	if err := cmdMount(s, []string{"DUA0", path}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount: %v", err)
	}
	t.Cleanup(func() {
		for _, vol := range s.Volumes {
			for _, dev := range vol.Devices {
				_ = dev.Container.Close()
			}
		}
	})

	vol := s.Volumes["DUA0"]
	if vol == nil {
		t.Fatalf("Volumes = %v, want a DUA0 entry", s.Volumes)
	}
	if _, ok := vol.Devices[0].Container.(diskimage.WritableContainer); ok {
		t.Error("Devices[0].Container unexpectedly implements diskimage.WritableContainer without /write")
	}
}

// rawCDSectorSize/rawCDSyncPattern mirror package diskimage's own unexported
// rawSectorSize/rawSyncPattern constants (diskimage/rawcd.go): the physical
// size of one raw CD-ROM sector, and the fixed 12-byte sync pattern every
// such sector begins with. Duplicated here (rather than exported from
// diskimage, which has no other reason to expose them) so this test can
// build a minimal raw-CD-shaped host file without a real disc image,
// confirming /WRITE rejects that format with a clear error instead of
// mounting successfully and failing later on the first actual write.
const rawCDSectorSize = 2352

var rawCDSyncPattern = [12]byte{
	0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00,
}

func TestCmdMountWriteQualifierRejectsRawCD(t *testing.T) {
	sector := make([]byte, rawCDSectorSize)
	copy(sector, rawCDSyncPattern[:])
	path := writeTestImage(t, "raw.img", sector)

	s := New()
	s.Stdout = &bytes.Buffer{}
	if err := cmdMount(s, []string{"DUA0", path}, Qualifiers{"write": ""}); err == nil {
		t.Fatal("cmdMount with /write on a raw CD-ROM image: want error, got nil")
	}
	if _, ok := s.Volumes["DUA0"]; ok {
		t.Error("cmdMount registered a volume despite failing to open it /write")
	}
}
