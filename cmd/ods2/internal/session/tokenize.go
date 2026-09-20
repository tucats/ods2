package session

import "strings"

// Qualifiers holds the "/name" or "/name:value" (or "/name=value")
// switches parsed from a command line, keyed by qualifier name in lower
// case. A qualifier with no value (a plain flag like "/full") maps to the
// empty string; use Has to tell that apart from the qualifier not having
// been given at all.
type Qualifiers map[string]string

// Has reports whether qualifier name was given on the command line at
// all, regardless of whether it carries a value.
func (q Qualifiers) Has(name string) bool {
	_, ok := q[strings.ToLower(name)]
	return ok
}

// Value returns qualifier name's value (the empty string if it was given
// with no value, or if it wasn't given at all — use Has to tell those
// apart).
func (q Qualifiers) Value(name string) string {
	return q[strings.ToLower(name)]
}

// tokenize splits a command line's arguments (everything after the
// command name itself) into positional arguments and qualifiers, given
// the command's own list of recognized qualifier names (see Command.
// Qualifiers).
//
// This replaces the original implementation's hand-rolled cmdsplit
// function with a similar shape: whitespace-separated fields, where a
// field starting with '/' is a qualifier (optionally carrying a value
// after a ':' or '=') rather than a positional argument. It deliberately
// diverges from the original in one way that matters a great deal for
// this port: a VMS file spec can never contain '/', so the original never
// had to worry about a positional argument starting with one — but this
// project's `copy` and `difference` commands take a plain HOST file path
// as one argument, and an absolute Unix-style path commonly starts with
// '/' too. So a "/..." token is only treated as a qualifier if its name
// (before any ':'/'=') actually matches one of validQualifiers; anything
// else starting with '/' is passed through as an ordinary positional
// argument instead. The tradeoff is a less specific error for a genuinely
// mistyped qualifier name (it becomes an "unexpected extra argument"
// rather than an "unrecognized qualifier" — Execute still catches it,
// just less precisely) in exchange for absolute paths simply working,
// which matters far more often in practice.
func tokenize(rest string, validQualifiers []string) ([]string, Qualifiers, error) {
	fields := strings.Fields(rest)

	var args []string
	quals := make(Qualifiers)

	for _, f := range fields {
		if strings.HasPrefix(f, "/") {
			name, value := f[1:], ""
			if idx := strings.IndexAny(name, ":="); idx != -1 {
				name, value = name[:idx], name[idx+1:]
			}
			if name != "" && containsFold(validQualifiers, name) {
				quals[strings.ToLower(name)] = value
				continue
			}
		}
		args = append(args, f)
	}

	return args, quals, nil
}
