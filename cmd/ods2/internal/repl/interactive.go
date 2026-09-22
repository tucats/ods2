package repl

import (
	"fmt"

	"github.com/chzyer/readline"

	"github.com/tucats/ods2/cmd/ods2/internal/session"
)

// RunInteractive drives an interactive ods2 session using a real
// line-editing terminal, via github.com/chzyer/readline: a prompt,
// ordinary line-editing keys (arrows, Ctrl-A/E, etc.), and command
// history persisted to historyFile (if non-empty — mirroring the
// reference implementation's own optional .ods2_history file).
//
// Unlike Run, this isn't practically unit-testable — it drives a real
// terminal — so it's kept as thin as possible: all it does beyond reading
// a line is exactly what Run does with that line (execute it against s,
// report any error, stop on exit/quit), so the behavior that actually
// matters is covered by Run's own tests.
func RunInteractive(s *session.Session, prompt, historyFile string) error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:      prompt,
		HistoryFile: historyFile,
	})
	if err != nil {
		return fmt.Errorf("repl: initializing line editor: %w", err)
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		switch err {
		case nil:
			// fall through to execute the line

		case readline.ErrInterrupt:
			// Ctrl-C on an empty or in-progress line: re-prompt, matching
			// an ordinary shell's behavior, rather than ending the
			// session.
			continue

		default:
			// io.EOF (Ctrl-D) or a genuine terminal error: either way,
			// there's nothing more to read, so end the session.
			return nil
		}

		keepGoing, cmdErr := s.Execute(line)
		if cmdErr != nil {
			fmt.Fprintf(s.Stdout, "%%ODS2-E-ERROR, %v\n", cmdErr)
		}
		
		if !keepGoing {
			return nil
		}
	}
}
