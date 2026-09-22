package session

import (
	"strings"
	"testing"
)

func TestCmdSearchFindsMatch(t *testing.T) {
	s, out := newTypeTestSession(t)

	if err := cmdSearch(s, []string{"STREAM.TXT", "two"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdSearch: %v", err)
	}

	got := out.String()

	if !strings.Contains(got, "STREAM.TXT") {
		t.Errorf("output = %q, want it to name the matching file", got)
	}

	if !strings.Contains(got, "line two") {
		t.Errorf("output = %q, want it to contain the matching line", got)
	}

	if strings.Contains(got, "line one") {
		t.Errorf("output = %q, want it to NOT contain the non-matching line", got)
	}
}

func TestCmdSearchCaseInsensitive(t *testing.T) {
	s, out := newTypeTestSession(t)

	if err := cmdSearch(s, []string{"STREAM.TXT", "LINE"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdSearch: %v", err)
	}

	if !strings.Contains(out.String(), "line one") {
		t.Errorf("output = %q, want a case-insensitive match to succeed", out.String())
	}
}

func TestCmdSearchNoMatch(t *testing.T) {
	s, out := newTypeTestSession(t)

	if err := cmdSearch(s, []string{"STREAM.TXT", "nosuchtext"}, Qualifiers{}); err != nil {
		t.Fatalf("cmdSearch: %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("output = %q, want empty output when nothing matches", out.String())
	}
}

func TestSearchIntegrationViaExecute(t *testing.T) {
	s, out := newTypeTestSession(t)

	if _, err := s.Execute("search STREAM.TXT one"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	
	if !strings.Contains(out.String(), "line one") {
		t.Errorf("output = %q, want it to contain the matching line", out.String())
	}
}
