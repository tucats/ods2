package session

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/volume"
)

// Session holds the state that persists across commands within one ods2
// run: which volumes are mounted, the current default device/directory,
// and a couple of small display preferences.
//
// The original C implementation kept this same information in
// package-level global variables (default_name, test_vcb, cmdVerbose,
// cDelim) — a Session replaces all of that with a single, explicit,
// testable value instead of implicit global state, which also means
// nothing prevents a program built on this package from running more
// than one independent session at once.
type Session struct {
	// Default is the current default device and directory: the "current
	// working directory" that a partial file spec typed at the command
	// line (like "COPY FOO.TXT *.*") is resolved against — see
	// filespec.Parse.
	Default filespec.Spec

	// Volumes maps a device name (as given to the `mount` command,
	// upper-cased) to the volume mounted there. VMS allows several
	// devices to be mounted at once; a file spec's Device field selects
	// which one a given command operates against.
	Volumes map[string]*volume.Volume

	// Verbose is a bitmask of optional diagnostic output categories (see
	// the Verbose* constants).
	Verbose VerboseFlags

	// Delim is the character used to separate a file's name/type from
	// its version number when building a host filename during `copy`.
	// VMS itself always uses ';'; this project allows overriding it
	// since ';' has special meaning to some host shells.
	Delim byte

	// Stdout is where command output is written. New sets this to
	// os.Stdout; tests and other embedders can point it elsewhere.
	Stdout io.Writer
}

// VerboseFlags is a bitmask of optional diagnostic output categories a
// command may consult before printing extra detail.
type VerboseFlags int

const (
	VerboseDirectory VerboseFlags = 1 << iota
	VerboseRead
	VerboseWrite
	VerboseFilename
)

// New creates an empty Session: no volumes mounted, the default version
// delimiter (';'), and output going to os.Stdout.
func New() *Session {
	return &Session{
		Volumes: make(map[string]*volume.Volume),
		Delim:   ';',
		Stdout:  os.Stdout,
	}
}

// volumeFor returns the mounted volume that spec.Device names, used by
// every command that operates on file specs (directory, copy, search,
// type, ...).
func (s *Session) volumeFor(spec filespec.Spec) (*volume.Volume, error) {
	key := strings.ToUpper(spec.Device)

	vol, ok := s.Volumes[key]
	if !ok {
		if key == "" {
			return nil, fmt.Errorf("no volume is mounted, and no default device is set")
		}

		return nil, fmt.Errorf("device %s is not mounted", key)
	}
	
	return vol, nil
}
