package session

import (
	"fmt"
	"strings"
)

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
// command name itself) into positional arguments and qualifiers.
//
// This replaces the original implementation's hand-rolled cmdsplit
// function with the same basic shape: whitespace-separated fields, where
// any field starting with '/' is a qualifier (optionally carrying a value
// after a ':' or '=') rather than a positional argument. Unlike the
// original, this project doesn't impose a fixed maximum count on either —
// a Go slice/map has no such limit to begin with.
func tokenize(rest string) ([]string, Qualifiers, error) {
	fields := strings.Fields(rest)

	var args []string
	quals := make(Qualifiers)

	for _, f := range fields {
		if !strings.HasPrefix(f, "/") {
			args = append(args, f)
			continue
		}

		name, value := f[1:], ""
		if idx := strings.IndexAny(name, ":="); idx != -1 {
			name, value = name[:idx], name[idx+1:]
		}
		if name == "" {
			return nil, nil, fmt.Errorf("session: empty qualifier in %q", f)
		}
		quals[strings.ToLower(name)] = value
	}

	return args, quals, nil
}
