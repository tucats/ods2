package filespec

import (
	"reflect"
	"testing"
)

func TestParseFullSpec(t *testing.T) {
	got, err := Parse("DUA0:[FOO.BAR]NAME.TYP;5", Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Spec{Device: "DUA0", Dirs: []string{"FOO", "BAR"}, Name: "NAME", Type: "TYP", Version: "5"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseAngleBrackets(t *testing.T) {
	got, err := Parse("DUA0:<FOO.BAR>NAME.TYP", Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Spec{Device: "DUA0", Dirs: []string{"FOO", "BAR"}, Name: "NAME", Type: "TYP"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseInheritsUnspecifiedComponents(t *testing.T) {
	def := Spec{Device: "DUA0", Dirs: []string{"FOO"}, Version: "3"}
	got, err := Parse("NAME.TYP", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Spec{Device: "DUA0", Dirs: []string{"FOO"}, Name: "NAME", Type: "TYP", Version: "3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParseNoBracketsInheritsDirsEntirely(t *testing.T) {
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A", "B"}) {
		t.Errorf("Dirs = %v, want [A B]", got.Dirs)
	}
}

func TestParseRelativeDescend(t *testing.T) {
	def := Spec{Dirs: []string{"A"}}
	got, err := Parse("[.SUB]FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A", "SUB"}) {
		t.Errorf("Dirs = %v, want [A SUB]", got.Dirs)
	}
}

func TestParseRelativeAscend(t *testing.T) {
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("[-]FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A"}) {
		t.Errorf("Dirs = %v, want [A]", got.Dirs)
	}
}

func TestParseRelativeAscendAndDescend(t *testing.T) {
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("[-.SUB]FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A", "SUB"}) {
		t.Errorf("Dirs = %v, want [A SUB]", got.Dirs)
	}
}

func TestParseDoubleAscend(t *testing.T) {
	def := Spec{Dirs: []string{"A", "B", "C"}}
	got, err := Parse("[--.SUB]FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A", "SUB"}) {
		t.Errorf("Dirs = %v, want [A SUB]", got.Dirs)
	}
}

func TestParseAscendAboveRootIsError(t *testing.T) {
	def := Spec{} // Dirs is nil: already at the master file directory
	if _, err := Parse("[-]FOO.TXT", def); err == nil {
		t.Fatal("Parse ascending above the root: want error, got nil")
	}
}

func TestParseExplicitRoot(t *testing.T) {
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("[000000]FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Dirs) != 0 {
		t.Errorf("Dirs = %v, want empty (the master file directory)", got.Dirs)
	}
}

func TestParseEmptyBracketsIsRoot(t *testing.T) {
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("[]FOO.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Dirs) != 0 {
		t.Errorf("Dirs = %v, want empty (the master file directory)", got.Dirs)
	}
}

func TestParseVersionWildcard(t *testing.T) {
	got, err := Parse("FOO.TXT;*", Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Version != "*" {
		t.Errorf("Version = %q, want %q", got.Version, "*")
	}
}

func TestParseRelativeVersion(t *testing.T) {
	got, err := Parse("FOO.TXT;-1", Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Version != "-1" {
		t.Errorf("Version = %q, want %q", got.Version, "-1")
	}
}

func TestParseUnterminatedBracket(t *testing.T) {
	if _, err := Parse("[FOO", Spec{}); err == nil {
		t.Fatal("Parse with an unterminated bracket: want error, got nil")
	}
}

func TestParseUnexpectedColonAfterDirectory(t *testing.T) {
	// A device delimiter must come before any directory bracket; a colon
	// appearing after one is a syntax error, not a second device spec.
	if _, err := Parse("[FOO]BAR:BAZ.TXT", Spec{}); err == nil {
		t.Fatal("Parse with a ':' after the directory bracket: want error, got nil")
	}
}

func TestParseInvalidRelativeDirectorySyntax(t *testing.T) {
	// A dash not immediately followed by either end-of-string or '.' is
	// not valid VMS relative-directory syntax.
	if _, err := Parse("[-FOO]BAR.TXT", Spec{Dirs: []string{"A"}}); err == nil {
		t.Fatal("Parse with invalid relative directory syntax: want error, got nil")
	}
}

func TestParseRecursiveAbsolute(t *testing.T) {
	got, err := Parse("[FOO...]BAR.TXT", Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.Recursive {
		t.Error("Recursive = false, want true")
	}
	if !reflect.DeepEqual(got.Dirs, []string{"FOO"}) {
		t.Errorf("Dirs = %v, want [FOO]", got.Dirs)
	}
}

func TestParseRecursiveRelative(t *testing.T) {
	// The sample from the reference project's own usage docs:
	// "dir [-.sys*...].%"
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("[-.SYS*...]BAR.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.Recursive {
		t.Error("Recursive = false, want true")
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A", "SYS*"}) {
		t.Errorf("Dirs = %v, want [A SYS*]", got.Dirs)
	}
}

func TestParseRecursiveBareDots(t *testing.T) {
	// "[...]" alone means "the default directory and everything beneath
	// it", not "the root and everything beneath it".
	def := Spec{Dirs: []string{"A", "B"}}
	got, err := Parse("[...]BAR.TXT", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.Recursive {
		t.Error("Recursive = false, want true")
	}
	if !reflect.DeepEqual(got.Dirs, []string{"A", "B"}) {
		t.Errorf("Dirs = %v, want [A B] (inherited from the default)", got.Dirs)
	}
}

func TestParseNonRecursiveDefaultsFalse(t *testing.T) {
	got, err := Parse("[FOO]BAR.TXT", Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Recursive {
		t.Error("Recursive = true, want false")
	}
}

func TestParseDeviceOnly(t *testing.T) {
	def := Spec{Dirs: []string{"A"}, Name: "OLD", Type: "OLD", Version: "1"}
	got, err := Parse("DUB1:", def)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Spec{Device: "DUB1", Dirs: []string{"A"}, Name: "OLD", Type: "OLD", Version: "1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}
