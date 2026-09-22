package session

import (
	"fmt"
	"strconv"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "purge",
			MinAbbrev:  3,
			MinArgs:    0,
			MaxArgs:    1,
			Qualifiers: []string{"limit"},
			Run:        cmdPurge,
		},
	)
}

// cmdPurge implements `purge [file-spec] [/limit=n]`, trimming every
// distinct name file-spec matches down to its n most recent surviving
// versions (default 1, meaning "keep only the single most recent
// version") via volume.PurgeVersions (docs/PHASE-03.md subtask 8).
//
// With no file-spec at all, PURGE defaults to "*.*" -- everything in the
// current default directory -- the same convenience default DIRECTORY
// itself uses.
//
// Unlike DELETE, a version in file-spec is never required, and isn't even
// meaningful: PURGE always considers every surviving version of each
// matched name, regardless of what (if anything) was typed after a ";",
// so any version selector the parsed spec happens to carry is overridden
// outright, rather than rejected as an error the way DELETE rejects a
// missing one.
func cmdPurge(s *Session, args []string, quals Qualifiers) (err error) {
	specText := "*.*"
	if len(args) > 0 {
		specText = args[0]
	}

	spec, err := filespec.Parse(specText, s.Default)
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	spec.Version = "*"

	keep := uint16(1)

	if quals.Has("limit") {
		v, err := strconv.ParseUint(quals.Value("limit"), 10, 16)
		if err != nil {
			return fmt.Errorf("purge: invalid /LIMIT value %q: %w", quals.Value("limit"), err)
		}

		keep = uint16(v)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	if len(vol.Devices) != 1 {
		return fmt.Errorf("purge: %s is a %d-device volume set; PURGE only supports a single-device volume", spec.Device, len(vol.Devices))
	}

	dev := vol.Devices[0]

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	bm, err := dev.Bitmap()
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	// Flushed once, however this function returns -- see docs/PHASE-03.md's
	// "Flush strategy" and cmdDelete's own identical pattern (delete.go),
	// which this mirrors exactly: a PURGE that trims some names before
	// failing on a later one still keeps whatever it already freed.
	defer func() {
		if flushErr := bm.Flush(); flushErr != nil && err == nil {
			err = fmt.Errorf("purge: %w", flushErr)
		}

		if flushErr := ib.Flush(); flushErr != nil && err == nil {
			err = fmt.Errorf("purge: %w", flushErr)
		}
	}()

	for _, group := range groupMatchesByDir(matches) {
		dir, err := filespec.ResolveDirectory(vol, group.dirs)
		if err != nil {
			return fmt.Errorf("purge: %w", err)
		}

		for _, name := range distinctNames(group.matches) {
			if err := volume.PurgeVersions(dir, name, keep, bm, ib); err != nil {
				return fmt.Errorf("purge: %w", err)
			}

			fmt.Fprintf(s.Stdout, "%%PURGE-S-PURGED, %s purged (keeping %d version(s))\n", name, keep)
		}
	}

	return nil
}

// distinctNames returns matches' combined "NAME.TYPE" names with
// duplicates removed, in first-seen order -- Glob's flat match list has
// one entry per surviving VERSION, so a name with several surviving
// versions appears several times; PurgeVersions only needs to be called
// once per distinct name (it resolves every version itself via
// Directory.List), not once per match.
func distinctNames(matches []filespec.Match) []string {
	seen := make(map[string]bool, len(matches))
	names := make([]string, 0, len(matches))

	for _, m := range matches {
		full := m.Name + "." + m.Type
		if !seen[full] {
			seen[full] = true

			names = append(names, full)
		}
	}

	return names
}
