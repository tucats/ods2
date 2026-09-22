package session

import (
	"fmt"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:      "delete",
			MinAbbrev: 3,
			MinArgs:   1,
			MaxArgs:   1,
			Run:       cmdDelete,
		},
	)
}

// cmdDelete implements `delete file-spec`, reclaiming a file's storage
// and removing its directory entry via volume.DeleteFile (docs/PHASE-03.md
// subtask 3): `DELETE FOO.TXT;3` deletes one specific version; `DELETE
// FOO.TXT;*` deletes every version of that name (filespec.Glob's own
// versionSelector already understands ";*", the same machinery DIRECTORY
// and COPY rely on); the name portion may itself be wildcarded (`DELETE
// *.TXT;3`) to hit several files in one command.
//
// A version is always required — unlike every other command in this
// package that resolves a file spec against the session's current
// default, a bare `DELETE FOO.TXT` (or a trailing-semicolon `DELETE
// FOO.TXT;`, which filespec.Parse can't tell apart from "no version
// typed at all" — see Spec.Version's own doc comment) is rejected before
// anything is touched. This is deliberate, not an oversight: DIRECTORY's
// own "no version means the highest one" convenience default would be
// exactly the wrong behavior here, since it would let a mistyped `DELETE
// *.TXT` silently delete the newest version of every matching name.
func cmdDelete(s *Session, args []string, quals Qualifiers) (err error) {
	spec, err := filespec.Parse(args[0], s.Default)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if spec.Version == "" {
		return fmt.Errorf("delete: %s: a specific version is required, e.g. %s;3 or %s;* (bare DELETE never defaults to a version)", args[0], args[0], args[0])
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if len(vol.Devices) != 1 {
		return fmt.Errorf("delete: %s is a %d-device volume set; DELETE only supports a single-device volume", spec.Device, len(vol.Devices))
	}
	dev := vol.Devices[0]

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("delete: %s.%s;%s not found", spec.Name, spec.Type, spec.Version)
	}

	bm, err := dev.Bitmap()
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	ib, err := dev.IndexBitmap()
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	// Flushed once, however this function returns (success, or an error
	// partway through several matches) — see docs/PHASE-03.md's "Flush
	// strategy". Using a deferred flush, rather than one only on the
	// success path, means a DELETE that fails partway through a
	// multi-match glob still keeps whatever it already freed rather than
	// discarding it: bm/ib's mutations are in-memory only until Flush
	// runs (volume.Bitmap/IndexBitmap's own deferred-cache design), and
	// every match already processed by the time a later one fails has
	// already made its bm.MarkFree/ib.MarkFree calls. A flush failure only
	// replaces err if the loop above didn't already fail for its own
	// reason — matching Volume.Dismount's own "report the first error"
	// convention (volume/dismount.go).
	defer func() {
		if flushErr := bm.Flush(); flushErr != nil && err == nil {
			err = fmt.Errorf("delete: %w", flushErr)
		}
		if flushErr := ib.Flush(); flushErr != nil && err == nil {
			err = fmt.Errorf("delete: %w", flushErr)
		}
	}()

	for _, group := range groupMatchesByDir(matches) {
		dir, err := filespec.ResolveDirectory(vol, group.dirs)
		if err != nil {
			return fmt.Errorf("delete: %w", err)
		}

		for _, m := range group.matches {
			fullName := m.Name + "." + m.Type
			if err := volume.DeleteFile(dir, fullName, m.Version, bm, ib); err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			fmt.Fprintf(s.Stdout, "%%DELETE-S-DELETED, %s;%d deleted\n", fullName, m.Version)
		}
	}

	return nil
}
