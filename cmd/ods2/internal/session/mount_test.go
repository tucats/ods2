package session

import (
	"bytes"
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

	if err := cmdMount(s, []string{path}, Qualifiers{}); err != nil {
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

	key := strings.ToUpper(path)
	if _, ok := s.Volumes[key]; !ok {
		t.Errorf("Volumes = %v, want an entry for %q", s.Volumes, key)
	}
}

func TestCmdMountNonexistentFile(t *testing.T) {
	s := New()
	if err := cmdMount(s, []string{filepath.Join(t.TempDir(), "nope.img")}, Qualifiers{}); err == nil {
		t.Fatal("cmdMount on a nonexistent file: want error, got nil")
	}
}
