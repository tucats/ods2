package session

import (
	"fmt"
	"time"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/vmstime"
)

func init() {
	Table = append(Table,
		Command{Name: "set", MinAbbrev: 3, MinArgs: 2, MaxArgs: 2, Run: cmdSet},
		Command{Name: "show", MinAbbrev: 2, MinArgs: 1, MaxArgs: 1, Run: cmdShow},
	)
}

// setSubverbMinAbbrev/showSubverbMinAbbrev are the minimum abbreviation
// lengths for `set`'s and `show`'s own sub-verbs — these are matched with
// the same matchesAbbrev rule the top-level command table uses, just
// against a one-off list instead of Table.
const subverbMinAbbrev = 2

// cmdSet implements `set default DIR-SPEC`, the only `set` sub-command
// this project (like the reference implementation) supports.
func cmdSet(s *Session, args []string, quals Qualifiers) error {
	if !matchesAbbrev(args[0], "default", subverbMinAbbrev) {
		return fmt.Errorf("set: unrecognized attribute %q (only DEFAULT is supported)", args[0])
	}

	spec, err := filespec.Parse(args[1], s.Default)
	if err != nil {
		return fmt.Errorf("set default: %w", err)
	}
	s.Default = spec
	return nil
}

// cmdShow implements `show default` and `show time`.
func cmdShow(s *Session, args []string, quals Qualifiers) error {
	switch {
	case matchesAbbrev(args[0], "default", subverbMinAbbrev):
		fmt.Fprintln(s.Stdout, s.Default.String())
	case matchesAbbrev(args[0], "time", subverbMinAbbrev):
		fmt.Fprintln(s.Stdout, vmstime.FromTime(time.Now()).String())
	default:
		return fmt.Errorf("show: unrecognized attribute %q", args[0])
	}
	return nil
}
