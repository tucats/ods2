package session

import (
	"fmt"
	"strings"
)

// Command describes one entry in the command table: a name, how much of
// it must be typed to select it unambiguously (VMS-style command
// abbreviation — e.g. typing just "dir" for "directory"), how many
// positional arguments it accepts, which qualifiers it recognizes, and
// the handler itself.
//
// Unlike the original C implementation's argument-count convention
// (where "minargs"/"maxargs" counted the command name itself as the
// first argument), MinArgs and MaxArgs here count only the arguments
// AFTER the command name — the more natural convention for a Go
// call signature that already receives args separately from the command
// name that selected it.
type Command struct {
	Name string

	// MinAbbrev is the fewest leading characters of Name that must be
	// typed to select this command; typing more (up to the full Name) is
	// always fine as long as it remains an exact prefix. A MinAbbrev
	// equal to len(Name) means the full name is required.
	MinAbbrev int

	MinArgs, MaxArgs int // number of positional arguments accepted, not including the command name; MaxArgs of -1 means unlimited

	// Qualifiers lists the "/name" switches this command recognizes
	// (without their leading '/', lower case). Using any other qualifier
	// is a command-line error. Leave nil for a command that accepts no
	// qualifiers at all.
	Qualifiers []string

	// Run executes the command. A nil Run marks a command (exit/quit) that
	// Execute handles specially by ending the session instead of calling
	// anything.
	Run func(s *Session, args []string, quals Qualifiers) error
}

// Table is the ordered list of commands a Session recognizes. It's a
// slice rather than a map so that abbreviation matching can scan it in a
// stable, deliberate order, and so a future `help` command can list
// commands in that same order rather than Go's randomized map iteration
// order.
var Table = []Command{
	{Name: "exit", MinAbbrev: 2, MinArgs: 0, MaxArgs: 0},
	{Name: "quit", MinAbbrev: 2, MinArgs: 0, MaxArgs: 0},
}

// matchesAbbrev reports whether input is a valid VMS-style abbreviation
// of full: at least minLen characters long, no longer than full itself,
// and an exact (case-insensitive) prefix of it. Both Table's own command
// lookup and commands with their own sub-verbs (like `set default` and
// `show default`/`show time`) use this same rule.
func matchesAbbrev(input, full string, minLen int) bool {
	input = strings.ToLower(input)
	full = strings.ToLower(full)
	return len(input) >= minLen && len(input) <= len(full) && full[:len(input)] == input
}

// lookup finds the single command in Table that name (case-insensitively)
// abbreviates.
func lookup(name string) (*Command, error) {
	var match *Command
	for i := range Table {
		cmd := &Table[i]
		if !matchesAbbrev(name, cmd.Name, cmd.MinAbbrev) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("session: %q is ambiguous between %q and %q", name, match.Name, cmd.Name)
		}
		match = cmd
	}
	if match == nil {
		return nil, fmt.Errorf("session: unrecognized command %q", name)
	}
	return match, nil
}

// Execute parses and runs one command line.
//
// It returns keepGoing = false only for `exit`/`quit` (a command with a
// nil Run) — the caller, typically a REPL loop, should stop reading
// further input in that case. Every other outcome, success or failure,
// returns keepGoing = true: a bad command line or a failed command
// doesn't end the session, the same way a DCL error doesn't log you out.
func (s *Session) Execute(line string) (keepGoing bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "!") {
		// A blank line or a comment (VMS's '!' convention): nothing to
		// do, matching the original cmdsplit's own handling of these.
		return true, nil
	}

	name, rest, _ := strings.Cut(line, " ")

	cmd, err := lookup(name)
	if err != nil {
		return true, err
	}

	if cmd.Run == nil {
		return false, nil
	}

	args, quals, err := tokenize(rest, cmd.Qualifiers)
	if err != nil {
		return true, err
	}

	if len(args) < cmd.MinArgs || (cmd.MaxArgs >= 0 && len(args) > cmd.MaxArgs) {
		return true, fmt.Errorf("session: %s requires between %d and %d argument(s), got %d", cmd.Name, cmd.MinArgs, cmd.MaxArgs, len(args))
	}

	return true, cmd.Run(s, args, quals)
}

// containsFold reports whether name (already lower case, from tokenize)
// appears in list, matched case-insensitively for robustness even though
// list's own entries are conventionally already lower case.
func containsFold(list []string, name string) bool {
	for _, item := range list {
		if strings.EqualFold(item, name) {
			return true
		}
	}
	return false
}
