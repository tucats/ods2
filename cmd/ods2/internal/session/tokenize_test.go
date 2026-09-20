package session

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	args, quals, err := tokenize("FOO.TXT BAR.TXT /full /before:today")
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
	_, quals, err := tokenize("/before=today")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if got := quals.Value("before"); got != "today" {
		t.Errorf("quals.Value(before) = %q, want %q", got, "today")
	}
}

func TestTokenizeQualifierNameIsCaseInsensitive(t *testing.T) {
	_, quals, err := tokenize("/FULL")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if !quals.Has("full") {
		t.Error(`quals.Has("full") = false after tokenizing "/FULL", want true`)
	}
}

func TestTokenizeNoArgs(t *testing.T) {
	args, quals, err := tokenize("")
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

func TestTokenizeRejectsEmptyQualifier(t *testing.T) {
	if _, _, err := tokenize("FOO.TXT /"); err == nil {
		t.Fatal("tokenize with a bare '/': want error, got nil")
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
