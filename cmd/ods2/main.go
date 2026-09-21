// Command ods2 is an interactive (and scriptable) tool for reading VAX/VMS
// ODS-2 disk volumes and images.
//
// Run with no arguments, it starts an interactive REPL (mount, directory,
// copy, ...). Each of those same commands is also available as a
// one-shot subcommand for scripting (e.g. "ods2 dir image.iso *.TXT"),
// sharing the exact same command implementations in package session, so
// behavior never diverges between the two entry points.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/tucats/ods2/cmd/ods2/internal/repl"
	"github.com/tucats/ods2/cmd/ods2/internal/session"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ods2:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "ods2",
		Short:         "Read and extract files from VAX/VMS ODS-2 disk volumes and images.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractive()
		},
	}

	for _, spec := range oneShotCommands {
		root.AddCommand(newOneShotCommand(spec))
	}

	return root
}

func runInteractive() error {
	s := session.New()
	fmt.Fprintln(s.Stdout, "ODS2 (Go port) -- type HELP for a command summary, EXIT to quit.")

	if term.IsTerminal(int(os.Stdin.Fd())) {
		return repl.RunInteractive(s, "ODS2> ", historyFilePath())
	}

	// Standard input isn't a real terminal — it's been piped or
	// redirected, e.g. "ods2 < script.txt" or a test/CI harness feeding
	// commands via a pipe. github.com/chzyer/readline's raw-terminal-mode
	// handling doesn't behave correctly against a pipe on every platform
	// (notably Windows, where it emits terminal control sequences instead
	// of ever reading the piped input), so fall back to plain line
	// reading instead, which works identically everywhere.
	return repl.Run(os.Stdin, s)
}

// historyFilePath returns where command history should be persisted
// between runs (mirroring the reference implementation's own optional
// .ods2_history file), or "" if the user's home directory can't be
// determined, which simply disables history persistence for that run.
func historyFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ods2_history")
}

// oneShotSpec describes one non-interactive subcommand: "ods2 <name>
// <image> [args...]" mounts image, then runs "<verb> args..." as a single
// ods2 command line — exactly what typing that same line at the
// interactive prompt (after "mount image") would do.
type oneShotSpec struct {
	use   string // cobra's Use string; its first word is the subcommand's name
	short string
	verb  string // the session command name to run
}

var oneShotCommands = []oneShotSpec{
	{"dir <image> [file-spec] [qualifiers...]", "List directory contents", "directory"},
	{"copy <image> <source> <destination> [qualifiers...]", "Copy a file off the volume", "copy"},
	{"type <image> <file-spec>", "Display a file's contents", "type"},
	{"search <image> <file-spec> <string>", "Search files for a string", "search"},
	{"difference <image> <file-spec> <local-file>", "Compare a file against a local file", "difference"},
}

func newOneShotCommand(spec oneShotSpec) *cobra.Command {
	return &cobra.Command{
		Use:   spec.use,
		Short: spec.short,
		Args:  cobra.MinimumNArgs(1),
		// ods2's own qualifiers use '/', not '-'/'--', so nothing here
		// should be parsed as a cobra/pflag flag — this also means a
		// destination path that happens to start with '-' passes through
		// untouched, rather than confusing cobra's own flag parser.
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOneShot(spec.verb, args[0], args[1:])
		},
	}
}

// runOneShot mounts image as a fresh session's only volume, then executes
// "verb rest..." as a single command line against it.
func runOneShot(verb, image string, rest []string) error {
	s := session.New()

	if _, err := s.Execute("mount " + image); err != nil {
		return err
	}

	line := verb
	if len(rest) > 0 {
		line += " " + strings.Join(rest, " ")
	}

	_, err := s.Execute(line)
	return err
}
