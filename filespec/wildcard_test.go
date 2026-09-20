package filespec

import "testing"

func TestMatchWildcard(t *testing.T) {
	cases := []struct {
		pattern, text string
		want          bool
	}{
		{"README.TXT", "README.TXT", true},
		{"README.TXT", "OTHER.TXT", false},
		{"readme.txt", "README.TXT", true}, // case-insensitive
		{"*", "ANYTHING", true},
		{"*", "", true},
		{"*.TXT", "README.TXT", true},
		{"*.TXT", "README.DAT", false},
		{"README.*", "README.TXT", true},
		{"README.*", "OTHER.TXT", false},
		{"*README*", "MYREADMEFILE", true},
		{"A%C", "ABC", true},
		{"A%C", "AC", false},   // % requires exactly one character
		{"A%C", "ABBC", false}, // and no more than one
		{"A%%D", "ABCD", true},
		{"%*", "X", true},
		{"%*", "", false}, // % requires at least one character even with * following
		{"*%", "", false},
		{"*%", "X", true},
	}

	for _, c := range cases {
		if got := matchWildcard(c.pattern, c.text); got != c.want {
			t.Errorf("matchWildcard(%q, %q) = %v, want %v", c.pattern, c.text, got, c.want)
		}
	}
}
