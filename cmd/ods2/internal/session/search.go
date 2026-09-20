package session

import (
	"fmt"
	"io"
	"strings"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/rms"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{Name: "search", MinAbbrev: 3, MinArgs: 2, MaxArgs: 2, Run: cmdSearch},
	)
}

// cmdSearch implements `search file-spec search-string`: a case-
// insensitive substring search (like a simple grep) across every record
// of every file file-spec matches, printing each matching file's name
// once followed by its matching lines.
func cmdSearch(s *Session, args []string, quals Qualifiers) error {
	spec, err := filespec.Parse(args[0], s.Default)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	needle := strings.ToLower(args[1])

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	for _, m := range matches {
		if err := searchFile(s, vol, m, needle); err != nil {
			return fmt.Errorf("search: %s.%s: %w", m.Name, m.Type, err)
		}
	}
	return nil
}

// searchFile searches one matched file's records for needle (already
// lower-cased), printing a header line the first time it finds a match in
// this file.
func searchFile(s *Session, vol *volume.Volume, m filespec.Match, needle string) error {
	f, err := vol.OpenFID(m.Fid)
	if err != nil {
		return err
	}

	r, err := rms.NewReader(f)
	if err != nil {
		return err
	}

	attr := f.Header.RecordAttributes
	isVFC := attr.Format == ondisk.RecordFormatVFC
	vfcSize := int(attr.VfcSize)

	printedHeader := false
	for {
		rec, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		text := rec
		if isVFC && len(rec) >= vfcSize {
			text = rec[vfcSize:]
		}

		if strings.Contains(strings.ToLower(string(text)), needle) {
			if !printedHeader {
				fmt.Fprintf(s.Stdout, "%s.%s%c%d\n", m.Name, m.Type, s.Delim, m.Version)
				printedHeader = true
			}
			fmt.Fprintf(s.Stdout, "%s\n", text)
		}
	}
}
