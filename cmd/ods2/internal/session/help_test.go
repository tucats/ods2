package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestCmdHelpListsCommands(t *testing.T) {
	s := New()

	var out bytes.Buffer
	
	s.Stdout = &out

	if err := cmdHelp(s, nil, Qualifiers{}); err != nil {
		t.Fatalf("cmdHelp: %v", err)
	}

	for _, name := range []string{"mount", "directory", "type", "search", "exit"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("help output = %q, want it to mention %q", out.String(), name)
		}
	}
}
