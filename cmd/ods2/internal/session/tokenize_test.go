package session

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	args, quals, err := tokenize("FOO.TXT BAR.TXT /full /before:today", []string{"full", "before"})
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	wantArgs := []string{"FOO.TXT", "BAR.TXT"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}

	if !quals.Has("full") {
		t.Error(`quals.Has("full") = false, want true`)
	}

	if quals.Value("full") != "" {
		t.Errorf(`quals.Value("full") = %q, want ""`, quals.Value("full"))
	}

	if got, want := quals.Value("before"), "today"; got != want {
		t.Errorf("quals.Value(before) = %q, want %q", got, want)
	}
}

func TestTokenizeEqualsAsValueSeparator(t *testing.T) {
	_, quals, err := tokenize("/before=today", []string{"before"})
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	if got := quals.Value("before"); got != "today" {
		t.Errorf("quals.Value(before) = %q, want %q", got, "today")
	}
}

func TestTokenizeQualifierNameIsCaseInsensitive(t *testing.T) {
	_, quals, err := tokenize("/FULL", []string{"full"})
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	if !quals.Has("full") {
		t.Error(`quals.Has("full") = false after tokenizing "/FULL", want true`)
	}
}

func TestTokenizeNoArgs(t *testing.T) {
	args, quals, err := tokenize("", nil)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}

	if len(quals) != 0 {
		t.Errorf("quals = %v, want empty", quals)
	}
}

func TestTokenizeUnrecognizedSlashTokenIsPositional(t *testing.T) {
	// This is the behavior that makes absolute Unix paths work as
	// ordinary positional arguments (see tokenize's own documentation):
	// a "/..." token that doesn't match one of the command's own
	// recognized qualifiers is passed through as a plain argument rather
	// than rejected or silently swallowed as a qualifier.
	args, quals, err := tokenize("/tmp/some/file.txt", []string{"full"})
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	if len(args) != 1 || args[0] != "/tmp/some/file.txt" {
		t.Errorf("args = %v, want [/tmp/some/file.txt]", args)
	}

	if len(quals) != 0 {
		t.Errorf("quals = %v, want empty", quals)
	}
}

func TestTokenizeBareSlashIsPositional(t *testing.T) {
	args, _, err := tokenize("FOO.TXT /", []string{"full"})
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}

	want := []string{"FOO.TXT", "/"}

	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestQualifiersHasVsValue(t *testing.T) {
	q := Qualifiers{"full": ""}
	if !q.Has("full") {
		t.Error(`Has("full") = false, want true`)
	}
	
	if q.Has("missing") {
		t.Error(`Has("missing") = true, want false`)
	}
}
