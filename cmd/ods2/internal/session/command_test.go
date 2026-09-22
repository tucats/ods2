package session

import "testing"

func TestMatchesAbbrev(t *testing.T) {
	cases := []struct {
		input, full string
		minLen      int
		want        bool
	}{
		{"dir", "directory", 3, true},
		{"di", "directory", 3, false}, // shorter than minLen
		{"directory", "directory", 3, true},
		{"directoryx", "directory", 3, false}, // longer than full
		{"DIR", "directory", 3, true},         // case-insensitive
		{"dor", "directory", 3, false},        // not a prefix
	}

	for _, c := range cases {
		if got := matchesAbbrev(c.input, c.full, c.minLen); got != c.want {
			t.Errorf("matchesAbbrev(%q, %q, %d) = %v, want %v", c.input, c.full, c.minLen, got, c.want)
		}
	}
}

func TestExecuteBlankLineAndComment(t *testing.T) {
	s := New()

	for _, line := range []string{"", "   ", "! this is a comment"} {
		keepGoing, err := s.Execute(line)
		if err != nil {
			t.Errorf("Execute(%q): %v", line, err)
		}

		if !keepGoing {
			t.Errorf("Execute(%q): keepGoing = false, want true", line)
		}
	}
}

func TestExecuteExit(t *testing.T) {
	s := New()

	for _, line := range []string{"exit", "quit", "ex", "qu"} {
		keepGoing, err := s.Execute(line)
		if err != nil {
			t.Errorf("Execute(%q): %v", line, err)
		}

		if keepGoing {
			t.Errorf("Execute(%q): keepGoing = true, want false", line)
		}
	}
}

func TestExecuteUnrecognizedCommand(t *testing.T) {
	s := New()

	keepGoing, err := s.Execute("frobnicate")
	if err == nil {
		t.Fatal("Execute(frobnicate): want error, got nil")
	}

	if !keepGoing {
		t.Error("Execute(frobnicate): keepGoing = false, want true (a bad command doesn't end the session)")
	}
}

func TestExecuteAmbiguousAbbreviation(t *testing.T) {
	// Add two commands sharing a prefix, temporarily, to exercise the
	// ambiguity check without depending on Table's real, evolving
	// contents.
	saved := Table
	defer func() { Table = saved }()

	Table = []Command{
		{Name: "search", MinAbbrev: 2, Run: func(*Session, []string, Qualifiers) error { return nil }},
		{Name: "set", MinAbbrev: 2, Run: func(*Session, []string, Qualifiers) error { return nil }},
	}

	s := New()

	if _, err := s.Execute("se"); err == nil {
		t.Fatal(`Execute("se") ambiguous between "search"/"set": want error, got nil`)
	}
}

func TestExecuteArgumentCountValidation(t *testing.T) {
	saved := Table

	defer func() { Table = saved }()

	called := false
	Table = []Command{
		{Name: "foo", MinAbbrev: 3, MinArgs: 1, MaxArgs: 2, Run: func(*Session, []string, Qualifiers) error {
			called = true

			return nil
		}},
	}

	s := New()
	if _, err := s.Execute("foo"); err == nil {
		t.Fatal("Execute(foo) with too few args: want error, got nil")
	}

	if called {
		t.Error("Run was called despite failing argument-count validation")
	}

	if _, err := s.Execute("foo a b c"); err == nil {
		t.Fatal("Execute(foo a b c) with too many args: want error, got nil")
	}

	if _, err := s.Execute("foo a"); err != nil {
		t.Fatalf("Execute(foo a): %v", err)
	}

	if !called {
		t.Error("Run was not called for a valid argument count")
	}
}

func TestExecuteUnsupportedQualifierBecomesExtraArgument(t *testing.T) {
	// An unrecognized "/..." token isn't rejected outright as a bad
	// qualifier -- see tokenize's own documentation for why (absolute
	// Unix paths). It becomes an ordinary positional argument instead,
	// so it still surfaces as an error here, just via the argument-count
	// check rather than a qualifier-specific one, since this command
	// accepts none.
	saved := Table
	defer func() { Table = saved }()

	Table = []Command{
		{Name: "foo", MinAbbrev: 3, MaxArgs: 0, Qualifiers: []string{"full"}, Run: func(*Session, []string, Qualifiers) error { return nil }},
	}

	s := New()
	if _, err := s.Execute("foo /nosuchqualifier"); err == nil {
		t.Fatal("Execute with an unrecognized /qualifier and no room for an extra argument: want error, got nil")
	}

	if _, err := s.Execute("foo /full"); err != nil {
		t.Fatalf("Execute with a supported qualifier: %v", err)
	}
}

func TestExecutePassesArgsAndQualsToRun(t *testing.T) {
	saved := Table
	defer func() { Table = saved }()

	var (
		gotArgs  []string
		gotQuals Qualifiers
	)

	Table = []Command{
		{Name: "foo", MinAbbrev: 3, MinArgs: 1, MaxArgs: 1, Qualifiers: []string{"full"}, Run: func(s *Session, args []string, quals Qualifiers) error {
			gotArgs = args
			gotQuals = quals

			return nil
		}},
	}

	s := New()
	if _, err := s.Execute("foo BAR.TXT /full"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(gotArgs) != 1 || gotArgs[0] != "BAR.TXT" {
		t.Errorf("gotArgs = %v, want [BAR.TXT]", gotArgs)
	}

	if !gotQuals.Has("full") {
		t.Error("gotQuals.Has(full) = false, want true")
	}
}
