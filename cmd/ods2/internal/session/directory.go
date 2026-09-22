package session

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "directory",
			MinAbbrev:  3,
			MinArgs:    0,
			MaxArgs:    1,
			Qualifiers: []string{"full", "file", "size", "date"},
			Run:        cmdDirectory,
		},
	)
}

// cmdDirectory implements `directory [/full] [/file] [/size] [/date] [file-spec]`.
// With no file spec, it lists everything in the current default directory
// (equivalent to "*.*"). /full implies all three of the other qualifiers.
func cmdDirectory(s *Session, args []string, quals Qualifiers) error {
	specText := "*.*;*"
	if len(args) > 0 {
		specText = args[0]
	}

	spec, err := filespec.Parse(specText, s.Default)
	if err != nil {
		return fmt.Errorf("directory: %w", err)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("directory: %w", err)
	}

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("directory: %w", err)
	}

	full := quals.Has("full")
	showFile := full || quals.Has("file")
	showSize := full || quals.Has("size")
	showDate := full || quals.Has("date")

	totalFiles := 0

	var totalBlocks uint32

	for _, group := range groupMatchesByDir(matches) {
		fmt.Fprintf(s.Stdout, "\nDirectory %s:[%s]\n\n", spec.Device, strings.Join(group.dirs, "."))

		for _, m := range group.matches {
			line, blocks, err := formatDirectoryEntry(vol, m, showFile, showSize, showDate, full, s.Delim)
			if err != nil {
				return fmt.Errorf("directory: %w", err)
			}

			fmt.Fprintln(s.Stdout, line)

			totalFiles++
			totalBlocks += blocks
		}
	}

	fmt.Fprintf(s.Stdout, "\nTotal of %d file(s)", totalFiles)

	if showSize {
		fmt.Fprintf(s.Stdout, ", %d block(s)", totalBlocks)
	}

	fmt.Fprintln(s.Stdout, ".")

	return nil
}

// dirGroup is every Match found in one directory, keyed by that
// directory's path.
type dirGroup struct {
	dirs    []string
	matches []filespec.Match
}

// groupMatchesByDir groups Glob's results by which directory they were
// found in (relevant when a wildcarded directory component, or /full's
// implicit "..." recursion, matched more than one directory), and returns
// the groups sorted by directory path for stable, readable output.
func groupMatchesByDir(matches []filespec.Match) []dirGroup {
	var order []string

	index := make(map[string]*dirGroup)

	for _, m := range matches {
		key := strings.Join(m.Dirs, ".")

		g, ok := index[key]
		if !ok {
			g = &dirGroup{dirs: m.Dirs}

			index[key] = g

			order = append(order, key)
		}

		g.matches = append(g.matches, m)
	}

	sort.Strings(order)
	groups := make([]dirGroup, len(order))

	for i, key := range order {
		groups[i] = *index[key]
	}

	return groups
}

// formatDirectoryEntry renders one line of DIRECTORY output for m, and
// also returns its size in blocks (0 if size information wasn't
// requested), which the caller accumulates into a grand total.
//
// This doesn't attempt to reproduce VMS's exact column alignment for a
// bare (no-qualifier) multi-column listing — it always prints one file
// per line, with any requested extra detail appended after it.
func formatDirectoryEntry(vol *volume.Volume, m filespec.Match, showFile, showSize, showDate, full bool, delim byte) (string, uint32, error) {
	var line strings.Builder

	name := fmt.Sprintf("%s.%s%c%d", m.Name, m.Type, delim, m.Version)
	fmt.Fprintf(&line, "%-30s", name)

	if !showFile && !showSize && !showDate && !full {
		return line.String(), 0, nil
	}

	f, err := vol.OpenFID(m.Fid)
	if err != nil {
		return "", 0, fmt.Errorf("opening %s.%s: %w", m.Name, m.Type, err)
	}

	blocks := f.Header.RecordAttributes.HighestBlock

	if showFile {
		fmt.Fprintf(&line, "  %-16s", m.Fid.String())
	}

	if showSize {
		fmt.Fprintf(&line, "  %5d", blocks)
	}

	if showDate {
		if ident, err := f.Header.Ident(); err == nil {
			fmt.Fprintf(&line, "  %s", ident.RevisionDate.String())
		}
	}

	if full {
		attr := f.Header.RecordAttributes
		fmt.Fprintf(&line, "  %s", attr.Format)
	}

	return line.String(), blocks, nil
}
