package session

import (
	"fmt"
	"sort"
)

func init() {
	Table = append(Table,
		Command{Name: "help", MinAbbrev: 2, MinArgs: 0, MaxArgs: 1, Run: cmdHelp},
	)
}

// cmdHelp implements `help`: lists every recognized command, in
// alphabetical order (Table itself is ordered however each command
// happened to register itself via init(), which isn't a meaningful
// ordering to show a user).
func cmdHelp(s *Session, args []string, quals Qualifiers) error {
	names := make([]string, 0, len(Table))
	for _, c := range Table {
		names = append(names, c.Name)
	}

	sort.Strings(names)

	fmt.Fprintln(s.Stdout, "Commands:")

	for _, n := range names {
		fmt.Fprintf(s.Stdout, "  %s\n", n)
	}
	
	return nil
}
