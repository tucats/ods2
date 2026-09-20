package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tucats/ods2/cmd/ods2/internal/session"
)

func TestRunExecutesEachLine(t *testing.T) {
	s := session.New()
	var out bytes.Buffer
	s.Stdout = &out

	input := "set default [FOO]\nshow default\n"
	if err := Run(strings.NewReader(input), s); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "[FOO]") {
		t.Errorf("output = %q, want it to contain [FOO]", out.String())
	}
}

func TestRunStopsOnExit(t *testing.T) {
	s := session.New()
	var out bytes.Buffer
	s.Stdout = &out

	// The line after "exit" must never be executed.
	input := "exit\nset default [SHOULDNOTRUN]\n"
	if err := Run(strings.NewReader(input), s); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if s.Default.String() != "[000000]" {
		t.Errorf("Default = %v, want the command after exit to never have run", s.Default)
	}
}

func TestRunContinuesAfterCommandError(t *testing.T) {
	s := session.New()
	var out bytes.Buffer
	s.Stdout = &out

	// A bad command on the first line must not stop the second line from
	// running, the same way a DCL error doesn't end an interactive
	// session.
	input := "frobnicate\nset default [FOO]\n"
	if err := Run(strings.NewReader(input), s); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "ODS2-E-ERROR") {
		t.Errorf("output = %q, want the bad command's error to be reported", out.String())
	}
	if !strings.Contains(s.Default.String(), "[FOO]") {
		t.Errorf("Default = %v, want the second line to still have run", s.Default)
	}
}

func TestRunEmptyInput(t *testing.T) {
	s := session.New()
	s.Stdout = &bytes.Buffer{}

	if err := Run(strings.NewReader(""), s); err != nil {
		t.Fatalf("Run on empty input: %v", err)
	}
}
