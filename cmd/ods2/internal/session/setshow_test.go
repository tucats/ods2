package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tucats/ods2/filespec"
)

func TestCmdSetDefault(t *testing.T) {
	s := New()
	s.Default = filespec.Spec{Device: "DUA0", Dirs: []string{"FOO"}}

	if err := cmdSet(s, []string{"default", "[.BAR]"}, nil); err != nil {
		t.Fatalf("cmdSet: %v", err)
	}

	want := []string{"FOO", "BAR"}
	if len(s.Default.Dirs) != 2 || s.Default.Dirs[0] != want[0] || s.Default.Dirs[1] != want[1] {
		t.Errorf("Default.Dirs = %v, want %v", s.Default.Dirs, want)
	}
	if s.Default.Device != "DUA0" {
		t.Errorf("Default.Device = %q, want %q (should be preserved)", s.Default.Device, "DUA0")
	}
}

func TestCmdSetUnrecognizedAttribute(t *testing.T) {
	s := New()
	if err := cmdSet(s, []string{"protection", "SOMETHING"}, nil); err == nil {
		t.Fatal(`cmdSet("protection", ...): want error, got nil`)
	}
}

func TestCmdSetDefaultAbbreviated(t *testing.T) {
	s := New()
	if err := cmdSet(s, []string{"def", "[FOO]"}, nil); err != nil {
		t.Fatalf("cmdSet with abbreviated sub-verb: %v", err)
	}
}

func TestCmdShowDefault(t *testing.T) {
	s := New()
	var out bytes.Buffer
	s.Stdout = &out
	s.Default = filespec.Spec{Device: "DUA0", Dirs: []string{"FOO"}}

	if err := cmdShow(s, []string{"default"}, nil); err != nil {
		t.Fatalf("cmdShow: %v", err)
	}
	if !strings.Contains(out.String(), "DUA0:[FOO]") {
		t.Errorf("show default output = %q, want it to contain %q", out.String(), "DUA0:[FOO]")
	}
}

func TestCmdShowTime(t *testing.T) {
	s := New()
	var out bytes.Buffer
	s.Stdout = &out

	if err := cmdShow(s, []string{"time"}, nil); err != nil {
		t.Fatalf("cmdShow: %v", err)
	}
	// Just confirm something plausible was printed; the exact instant is
	// inherently untestable here.
	if out.Len() < len("01-JAN-1970 00:00:00.00") {
		t.Errorf("show time output = %q, looks too short to be a timestamp", out.String())
	}
}

func TestCmdShowUnrecognizedAttribute(t *testing.T) {
	s := New()
	if err := cmdShow(s, []string{"protection"}, nil); err == nil {
		t.Fatal(`cmdShow("protection"): want error, got nil`)
	}
}

func TestSetShowIntegrationViaExecute(t *testing.T) {
	s := New()
	var out bytes.Buffer
	s.Stdout = &out

	if _, err := s.Execute("set default [FOO.BAR]"); err != nil {
		t.Fatalf("Execute(set default): %v", err)
	}
	if _, err := s.Execute("show default"); err != nil {
		t.Fatalf("Execute(show default): %v", err)
	}
	if !strings.Contains(out.String(), "[FOO.BAR]") {
		t.Errorf("output = %q, want it to contain %q", out.String(), "[FOO.BAR]")
	}
}
