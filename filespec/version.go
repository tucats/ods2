package filespec

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/tucats/ods2/ondisk"
)

// versionSelectorKind classifies how a Spec.Version string selects among
// a name's surviving versions.
type versionSelectorKind int

const (
	// versionHighest selects only the single highest-numbered version of
	// a name. This is what an empty Version string (or the literal "0",
	// which is not itself a legal VMS version number) means — the same
	// convention volume.Directory.Lookup uses.
	versionHighest versionSelectorKind = iota

	// versionAll selects every surviving version of a name. Written as
	// ";*".
	versionAll

	// versionExact selects one specific version number. Written as e.g.
	// ";5".
	versionExact

	// versionRelative selects the Nth version back from the highest,
	// counting the highest itself as 1st. Written as e.g. ";-1" (the
	// highest version — equivalent to versionHighest) or ";-2" (the
	// second-highest).
	versionRelative
)

// versionSelector is a parsed Spec.Version string.
type versionSelector struct {
	kind  versionSelectorKind
	value int // meaningful for versionExact (the version number) and versionRelative (how many back, 1 = highest)
}

// parseVersionSelector interprets a Spec.Version string.
func parseVersionSelector(v string) (versionSelector, error) {
	switch v {
	case "", "0":
		return versionSelector{kind: versionHighest}, nil
	case "*":
		return versionSelector{kind: versionAll}, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return versionSelector{}, fmt.Errorf("filespec: invalid version %q", v)
	}

	switch {
	case n > 0:
		return versionSelector{kind: versionExact, value: n}, nil
	case n < 0:
		return versionSelector{kind: versionRelative, value: -n}, nil
	default: // n == 0, already handled above via the string switch, but kept for completeness
		return versionSelector{kind: versionHighest}, nil
	}
}

// selectVersions filters entries — which may name any number of distinct
// files, each potentially with multiple surviving versions — down to
// those matching sel, considered independently within each distinct Name.
//
// The result preserves entries' relative order within each name group
// (highest version first), but groups are emitted in the order their name
// first appears in entries.
func selectVersions(entries []ondisk.DirEntry, sel versionSelector) []ondisk.DirEntry {
	type group struct {
		name    string
		entries []ondisk.DirEntry
	}

	var groups []*group

	byName := make(map[string]*group)
	for _, e := range entries {
		g, ok := byName[e.Name]
		if !ok {
			g = &group{name: e.Name}
			byName[e.Name] = g
			groups = append(groups, g)
		}

		g.entries = append(g.entries, e)
	}

	var result []ondisk.DirEntry

	for _, g := range groups {
		sort.Slice(g.entries, func(i, j int) bool {
			return g.entries[i].Version > g.entries[j].Version
		})

		switch sel.kind {
		case versionAll:
			result = append(result, g.entries...)

		case versionExact:
			for _, e := range g.entries {
				if int(e.Version) == sel.value {
					result = append(result, e)

					break
				}
			}

		default: // versionHighest or versionRelative
			index := 0
			if sel.kind == versionRelative {
				index = sel.value - 1
			}

			if index >= 0 && index < len(g.entries) {
				result = append(result, g.entries[index])
			}
		}
	}

	return result
}
