package session

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestCmdInitializeCreatesLoadableVolume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.dsk")

	s := New()
	var out bytes.Buffer
	s.Stdout = &out

	if err := cmdInitialize(s, []string{path, "400", "TESTVOL"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdInitialize: %v", err)
	}
	if out.Len() == 0 {
		t.Error("cmdInitialize printed no confirmation message")
	}

	// A freshly initialized volume isn't mounted by cmdInitialize itself
	// (see its own doc comment) -- confirm the file it created really is
	// a mountable ODS-2 volume via an ordinary cmdMount, the same way a
	// user would follow up interactively.
	if err := cmdMount(s, []string{"DUA0", path}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount on freshly initialized volume: %v", err)
	}

	vol, ok := s.Volumes["DUA0"]
	if !ok {
		t.Fatalf("Volumes = %v, want a DUA0 entry", s.Volumes)
	}
	t.Cleanup(func() {
		for _, dev := range vol.Devices {
			_ = dev.Container.Close()
		}
	})
	if got, want := vol.Devices[0].Home.VolumeName, "TESTVOL"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
}

func TestCmdInitializeHonorsClusterQualifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clustered.dsk")

	s := New()
	s.Stdout = &bytes.Buffer{}

	if err := cmdInitialize(s, []string{path, "400"}, Qualifiers{"cluster": "2"}); err != nil {
		t.Fatalf("cmdInitialize: %v", err)
	}

	if err := cmdMount(s, []string{"DUA0", path}, Qualifiers{}); err != nil {
		t.Fatalf("cmdMount: %v", err)
	}
	vol := s.Volumes["DUA0"]
	t.Cleanup(func() {
		for _, dev := range vol.Devices {
			_ = dev.Container.Close()
		}
	})
	if got, want := vol.Devices[0].Home.ClusterSize, uint16(2); got != want {
		t.Errorf("ClusterSize = %d, want %d", got, want)
	}
}

func TestCmdInitializeInvalidSize(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "bad.dsk")
	if err := cmdInitialize(s, []string{path, "not-a-number"}, Qualifiers{}); err == nil {
		t.Error("cmdInitialize with a non-numeric size: want error, got nil")
	}
}

func TestCmdInitializeRejectsUndersizedVolume(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "tiny.dsk")
	if err := cmdInitialize(s, []string{path, "5"}, Qualifiers{}); err == nil {
		t.Error("cmdInitialize with size 5: want error, got nil")
	}
}
