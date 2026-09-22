package filespec

import "strings"

// matchWildcard reports whether text matches pattern, using VMS's two
// wildcard characters: '*' matches any run of characters (including
// none), and '%' matches exactly one character. Matching is
// case-insensitive, matching VMS's own convention (and the fact that
// directory entries are conventionally stored upper-case).
//
// A pattern with no wildcard characters at all just becomes an exact
// (case-insensitive) match, so this same function serves both wildcard
// and non-wildcard lookups uniformly.
func matchWildcard(pattern, text string) bool {
	return matchHere(strings.ToUpper(pattern), strings.ToUpper(text))
}

// matchHere is matchWildcard's recursive worker, matching an
// already-uppercased pattern/text pair. This is the classic simple
// recursive glob-matching algorithm: straightforward to read and verify
// correct, at the cost of exponential worst-case time for pathological
// patterns (long runs of '*'). That tradeoff is fine here — VMS file names
// are capped at a few dozen characters, so the worst case is nowhere near
// large enough to matter in practice.
func matchHere(pattern, text string) bool {
	if pattern == "" {
		return text == ""
	}

	switch pattern[0] {
	case '*':
		// A '*' can consume zero or more characters of text; try every
		// possible split point.
		for i := 0; i <= len(text); i++ {
			if matchHere(pattern[1:], text[i:]) {
				return true
			}
		}

		return false

	case '%':
		if text == "" {
			return false
		}

		return matchHere(pattern[1:], text[1:])

	default:
		if text == "" || pattern[0] != text[0] {
			return false
		}

		return matchHere(pattern[1:], text[1:])
	}
}
