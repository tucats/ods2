package session

import (
	"fmt"
	"strconv"
	"time"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/vmstime"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{Name: "set", MinAbbrev: 3, MinArgs: 2, MaxArgs: 2, Qualifiers: []string{"version_limit"}, Run: cmdSet},
		Command{Name: "show", MinAbbrev: 2, MinArgs: 1, MaxArgs: 1, Run: cmdShow},
	)
}

// setSubverbMinAbbrev/showSubverbMinAbbrev are the minimum abbreviation
// lengths for `set`'s and `show`'s own sub-verbs — these are matched with
// the same matchesAbbrev rule the top-level command table uses, just
// against a one-off list instead of Table.
const subverbMinAbbrev = 2

// cmdSet dispatches `set`'s own sub-verbs: `set default DIR-SPEC` (the
// only one this project's reference implementation supports) and `set
// file /version_limit=n file-spec` (docs/PHASE-03.md subtask 7).
func cmdSet(s *Session, args []string, quals Qualifiers) error {
	switch {
	case matchesAbbrev(args[0], "default", subverbMinAbbrev):
		spec, err := filespec.Parse(args[1], s.Default)
		if err != nil {
			return fmt.Errorf("set default: %w", err)
		}
		s.Default = spec
		return nil
	case matchesAbbrev(args[0], "file", subverbMinAbbrev):
		return cmdSetFile(s, args[1], quals)
	default:
		return fmt.Errorf("set: unrecognized attribute %q (only DEFAULT and FILE are supported)", args[0])
	}
}

// cmdSetFile implements `set file /version_limit=n file-spec`, e.g. `SET
// FILE /VERSION_LIMIT=3 FOO.TXT` — the space between the "file" sub-verb
// and its qualifier is required by this package's own tokenizer (see
// tokenize.go's doc comment), even though real VMS DCL would accept
// "FILE/VERSION_LIMIT" run together — the only way to change an
// already-existing file's (or directory's) own RecordAttributes.
// VersionLimit, via volume.SetVersionLimit (docs/PHASE-03.md subtask 6).
// /VERSION_LIMIT is required; there's no other file attribute this
// sub-command currently sets.
//
// file-spec is resolved via filespec.Glob exactly like DIRECTORY's own
// default (no version given means the highest existing version, and
// wildcards expand to every match), so one command can set a limit across
// several names or target a single directory's own file spec (e.g.
// [FOO]SUBDIR.DIR — the same "a directory entry is just an ordinary file
// named NAME.DIR" convention COPY's own /DIRS handling already relies on)
// -- unlike DELETE, there's no reason to require an exact version here,
// since setting a limit on an old version is just as meaningful as on the
// newest.
//
// Unlike DELETE/PURGE, this command never touches bm/ib at all --
// SetVersionLimit is a single, immediate header rewrite with no bitmap
// mutation whatsoever (see its own doc comment) -- so, unlike those two
// commands, there's nothing here to Flush at the end (docs/PHASE-03.md's
// "Flush strategy" describes the SAME once-at-the-end flush for this
// command, but it would be a pure no-op given nothing ever gets marked
// dirty in the first place).
func cmdSetFile(s *Session, arg string, quals Qualifiers) error {
	if !quals.Has("version_limit") {
		return fmt.Errorf("set file: /VERSION_LIMIT=n is required")
	}
	limit, err := strconv.ParseUint(quals.Value("version_limit"), 10, 16)
	if err != nil {
		return fmt.Errorf("set file: invalid /VERSION_LIMIT value %q: %w", quals.Value("version_limit"), err)
	}

	spec, err := filespec.Parse(arg, s.Default)
	if err != nil {
		return fmt.Errorf("set file: %w", err)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("set file: %w", err)
	}

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("set file: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("set file: %s not found", arg)
	}

	for _, m := range matches {
		f, err := vol.OpenFID(m.Fid)
		if err != nil {
			return fmt.Errorf("set file: %w", err)
		}
		if err := volume.SetVersionLimit(f, uint16(limit)); err != nil {
			return fmt.Errorf("set file: %w", err)
		}
		fmt.Fprintf(s.Stdout, "%%SET-S-SET, %s.%s;%d version limit set to %d\n", m.Name, m.Type, m.Version, limit)
	}

	return nil
}

// cmdShow implements `show default` and `show time`.
func cmdShow(s *Session, args []string, quals Qualifiers) error {
	switch {
	case matchesAbbrev(args[0], "default", subverbMinAbbrev):
		fmt.Fprintln(s.Stdout, s.Default.String())
	case matchesAbbrev(args[0], "time", subverbMinAbbrev):
		fmt.Fprintln(s.Stdout, vmstime.FromTime(time.Now()).String())
	default:
		return fmt.Errorf("show: unrecognized attribute %q", args[0])
	}
	return nil
}
