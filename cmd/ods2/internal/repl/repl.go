// Package repl implements the ods2 interactive command loop.
package repl

import (
	"bufio"
	"fmt"
	"io"

	"github.com/tucats/ods2/cmd/ods2/internal/session"
)

// Run reads command lines from r, one at a time, executing each against s
// and writing any command error to s.Stdout, until r is exhausted or a
// command ends the session (exit/quit). It returns the first unexpected
// I/O error encountered while reading from r, if any — a command's own
// execution error is reported and does NOT stop the loop, the same way a
// DCL error doesn't end an interactive VMS session.
//
// This is the REPL's non-interactive core: no prompting, line editing, or
// history of its own, which is exactly what makes it trivially testable
// with a plain strings.Reader. See RunInteractive for the real terminal
// experience (prompting, line editing, persistent history) built on top
// of the same Session.Execute this function drives.
func Run(r io.Reader, s *session.Session) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		keepGoing, err := s.Execute(scanner.Text())
		if err != nil {
			fmt.Fprintf(s.Stdout, "%%ODS2-E-ERROR, %v\n", err)
		}
		if !keepGoing {
			return nil
		}
	}
	return scanner.Err()
}
