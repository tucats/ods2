package session

import (
	"fmt"
	"strconv"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "create",
			MinAbbrev:  3,
			MinArgs:    2,
			MaxArgs:    2,
			Qualifiers: []string{"version", "owner", "protection", "allocation"},
			Run:        cmdCreate,
		},
	)
}

// cmdCreate implements `create directory dir-spec [qualifiers]` --
// currently the only `create` sub-command this project supports,
// dispatched the same way `set default` (and, per docs/PHASE-03.md
// subtask 7, a future `set file`) is: a sub-verb as the first positional
// argument, matched via matchesAbbrev/subverbMinAbbrev exactly like
// cmdSet's own dispatch.
func cmdCreate(s *Session, args []string, quals Qualifiers) error {
	if !matchesAbbrev(args[0], "directory", subverbMinAbbrev) {
		return fmt.Errorf("create: unrecognized object %q (only DIRECTORY is supported)", args[0])
	}

	return cmdCreateDirectory(s, args[1], quals)
}

// cmdCreateDirectory implements `create directory dir-spec [/VERSION=n]
// [/OWNER=[g,m]] [/PROTECTION=(...)] [/ALLOCATION=n]`, e.g.
// `CREATE DIRECTORY [FOO.BAR] /VERSION=5`, which creates [FOO.BAR] --
// and [FOO] first, if that doesn't exist yet either, as real VMS's own
// CREATE/DIRECTORY does (filespec.CreateDirectoryPath). dir-spec may use
// any of filespec.Parse's directory syntax, including a relative path from
// the session's current default ("[.BAR]").
//
// dir-spec must be a bare directory path: a trailing file name/type/
// version (e.g. "[FOO]BAR.TXT") or a recursive "..." suffix are both
// rejected, since neither means anything for a directory being created. A
// directory that already exists isn't an error: it's reported as existing,
// as VMS reports it.
//
// Each directory made gets VMS's defaults (volume.InheritedDirectoryOptions,
// worked out against its own parent) unless a qualifier says otherwise:
//
//   - /VERSION=n sets the new directory's own default version limit --
//     RecordAttributes.VersionLimit, which docs/PHASE-03.md's
//     "Version-limit design" explains is where a directory's default for
//     the names created directly inside it lives. Without it, the parent's
//     limit is copied (a one-time snapshot, not a live link back to the
//     parent).
//   - /OWNER=[g,m] sets the owner UIC (ondisk.ParseUic); without it, the
//     parent directory's owner.
//   - /PROTECTION=(S:RWED,...) sets the protection (ondisk.ParseProtection;
//     a category it leaves out keeps the default); without it, the
//     parent's protection less delete access.
//   - /ALLOCATION=n gives the directory n blocks at once; without it, 1.
func cmdCreateDirectory(s *Session, arg string, quals Qualifiers) error {
	spec, err := filespec.Parse(arg, s.Default)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if spec.Recursive || spec.Name != "" || spec.Type != "" || spec.Version != "" {
		return fmt.Errorf("create directory: %s: expected a directory path such as [FOO.BAR], not a file spec", arg)
	}

	options, err := createDirectoryOptions(quals)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	dev := vol.Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	levels, createErr := filespec.CreateDirectoryPath(vol, spec.Dirs, options, bm, ib)

	// Whatever was made before a failure stays made, so the bitmaps are
	// written either way.
	if err := bm.Flush(); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := ib.Flush(); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	for _, level := range levels {
		if level.Created {
			full := filespec.Spec{Device: spec.Device, Dirs: level.Path[:len(level.Path)-1], Name: level.Path[len(level.Path)-1], Type: "DIR", Version: "1"}
			fmt.Fprintf(s.Stdout, "%%CREATE-S-CREATED, %s created\n", full.String())
		}
	}

	if createErr != nil {
		return fmt.Errorf("create directory: %w", createErr)
	}

	if last := levels[len(levels)-1]; !last.Created {
		existing := filespec.Spec{Device: spec.Device, Dirs: last.Path}
		fmt.Fprintf(s.Stdout, "%%CREATE-I-EXISTS, %s already exists\n", existing.String())
	}

	return nil
}

// createDirectoryOptions turns CREATE DIRECTORY's qualifiers into the
// options callback filespec.CreateDirectoryPath calls for each directory it
// makes: VMS's defaults for that directory's parent, with whatever the
// qualifiers set laid over them.
func createDirectoryOptions(quals Qualifiers) (func(*volume.Directory) volume.DirectoryOptions, error) {
	var (
		versionLimit   *uint16
		owner          *ondisk.Uic
		protectionText string
		allocation     uint32
	)

	if quals.Has("version") {
		v, err := strconv.ParseUint(quals.Value("version"), 10, 16)
		if err != nil || v > 32767 {
			return nil, fmt.Errorf("invalid /VERSION value %q: want 0 to 32767", quals.Value("version"))
		}

		limit := uint16(v)
		versionLimit = &limit
	}

	if quals.Has("owner") {
		uic, err := ondisk.ParseUic(quals.Value("owner"))
		if err != nil {
			return nil, err
		}

		owner = &uic
	}

	if quals.Has("protection") {
		protectionText = quals.Value("protection")

		// Checked now, so a bad value fails before anything is made.
		if _, err := ondisk.ParseProtection(protectionText, 0); err != nil {
			return nil, err
		}
	}

	if quals.Has("allocation") {
		n, err := strconv.ParseUint(quals.Value("allocation"), 10, 32)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("invalid /ALLOCATION value %q: want a number of blocks", quals.Value("allocation"))
		}

		allocation = uint32(n)
	}

	return func(parent *volume.Directory) volume.DirectoryOptions {
		o := volume.InheritedDirectoryOptions(parent)

		if versionLimit != nil {
			o.VersionLimit = *versionLimit
		}

		if owner != nil {
			o.Owner = owner
		}

		if protectionText != "" {
			// Can't fail: the same text parsed above.
			p, _ := ondisk.ParseProtection(protectionText, *o.Protection)
			o.Protection = &p
		}

		o.Allocation = allocation

		return o
	}, nil
}
