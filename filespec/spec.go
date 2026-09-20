package filespec

import "strings"

// Spec is a parsed VMS file specification:
//
//	device:[dir.subdir]name.type;version
//
// Any component may be its zero value, meaning "not specified" — Parse
// fills in unspecified components from a default Spec (typically a
// session's current default directory), the same way VMS itself lets you
// type a partial file spec like "FOO.TXT" and have the device and
// directory filled in from wherever your current default happens to be.
type Spec struct {
	// Device is the volume/device name, e.g. "DUA0". VMS device names are
	// conventionally written with a trailing ':' ("DUA0:"), but that
	// colon is just punctuation separating the device from the rest of
	// the spec — it is not part of the value stored here.
	Device string

	// Dirs is the directory path as a sequence of component names, read
	// from the volume's master file directory (MFD) downward — e.g.
	// []string{"FOO", "BAR"} for what VMS writes as "[FOO.BAR]". An empty
	// (or nil) Dirs means the MFD itself, which VMS writes as
	// "[000000]" — this is a normal, fully-specified value, not a marker
	// for "not specified" (see Parse's documentation for how those two
	// cases are told apart while parsing).
	Dirs []string

	// Recursive is true when the directory spec ended in VMS's "..."
	// wildcard, e.g. "[FOO...]" or "[-.SYS*...]" — meaning "Dirs, and
	// every subdirectory beneath it, to any depth". It has no effect on
	// its own; it's Glob's wildcard expansion that acts on it.
	Recursive bool

	Name string
	Type string

	// Version is the raw text of the version field, e.g. "5", "*", or
	// "-1"; an empty string means no version was written at all.
	// Interpreting what a particular version string actually selects (an
	// exact version, every version, the Nth-from-latest version, ...) is
	// a wildcard-matching concern for a different part of this package,
	// not something Spec itself needs to know.
	Version string
}

// String renders spec back into VMS file-specification text, e.g.
// "DUA0:[FOO.BAR]NAME.TYP;5" — the inverse of Parse, for displaying a
// spec to a user (a "show default" command, say). Components left at
// their zero value are simply omitted, the same as when they're left
// unwritten in typed input; Dirs is the one exception, since an empty
// Dirs is itself a complete, valid value (the master file directory) —
// see Spec.Dirs's own documentation.
func (s Spec) String() string {
	var b strings.Builder

	if s.Device != "" {
		b.WriteString(s.Device)
		b.WriteByte(':')
	}

	b.WriteByte('[')
	b.WriteString(strings.Join(s.Dirs, "."))
	if len(s.Dirs) == 0 {
		b.WriteString("000000")
	}
	if s.Recursive {
		b.WriteString("...")
	}
	b.WriteByte(']')

	b.WriteString(s.Name)
	if s.Type != "" {
		b.WriteByte('.')
		b.WriteString(s.Type)
	}
	if s.Version != "" {
		b.WriteByte(';')
		b.WriteString(s.Version)
	}

	return b.String()
}
