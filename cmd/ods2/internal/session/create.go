package session

import (
	"fmt"
	"strconv"

	"github.com/tucats/ods2/filespec"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "create",
			MinAbbrev:  3,
			MinArgs:    2,
			MaxArgs:    2,
			Qualifiers: []string{"version"},
			Run:        cmdCreate,
		},
	)
}

// cmdCreate implements `create directory dir-spec [/VERSION=n]` --
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

// cmdCreateDirectory implements `create directory dir-spec [/VERSION=n]`,
// e.g. `CREATE DIRECTORY [FOO.BAR] /VERSION=5`, which creates BAR.DIR
// inside the already-existing [FOO] -- matching real VMS's own
// CREATE/DIRECTORY, where a bracketed directory path's last component
// names the subdirectory being created and everything before it names its
// (required to already exist) parent. dir-spec may use any of
// filespec.Parse's directory syntax, including a relative path from the
// session's current default ("[.BAR]") -- whatever it resolves to, its
// last component is peeled off as the new directory's own name.
//
// dir-spec must be a bare directory path: a trailing file name/type/
// version (e.g. "[FOO]BAR.TXT") or a recursive "..." suffix are both
// rejected, since neither means anything for a directory being created.
//
// /VERSION sets the new directory's own default version limit --
// RecordAttributes.VersionLimit, which docs/PHASE-03.md's "Version-limit
// design" explains is where a directory's default for the names created
// directly inside it lives. Without /VERSION, the new directory inherits
// its own parent's current version limit at the moment of creation (a
// one-time snapshot, not a live link back to the parent -- the same
// "captured once" rule that section lays out for an ordinary file's first
// version).
func cmdCreateDirectory(s *Session, arg string, quals Qualifiers) error {
	spec, err := filespec.Parse(arg, s.Default)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if spec.Recursive || spec.Name != "" || spec.Type != "" || spec.Version != "" {
		return fmt.Errorf("create directory: %s: expected a directory path such as [FOO.BAR], not a file spec", arg)
	}
	if len(spec.Dirs) == 0 {
		return fmt.Errorf("create directory: %s: no directory name given", arg)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	parentDirs := spec.Dirs[:len(spec.Dirs)-1]
	name := spec.Dirs[len(spec.Dirs)-1]

	parent, err := filespec.ResolveDirectory(vol, parentDirs)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	versionLimit := parent.Header.RecordAttributes.VersionLimit
	if quals.Has("version") {
		v, err := strconv.ParseUint(quals.Value("version"), 10, 16)
		if err != nil {
			return fmt.Errorf("create directory: invalid /VERSION value %q: %w", quals.Value("version"), err)
		}
		versionLimit = uint16(v)
	}

	dev := parent.Device
	bm, err := dev.Bitmap()
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	ib, err := dev.IndexBitmap()
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	fullName := name + ".DIR"
	if _, err := vol.CreateDirectory(parent, fullName, versionLimit, bm, ib); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := bm.Flush(); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if err := ib.Flush(); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Looking the version actually assigned back up via Lookup, rather
	// than threading it back out of CreateDirectory itself, keeps that
	// bookkeeping entirely inside Directory, where it already lives --
	// the same convention copy.go's copyOneFileToVolume already follows
	// for CreateFile.
	entry, err := parent.Lookup(fullName, 0)
	if err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	full := filespec.Spec{Device: spec.Device, Dirs: parentDirs, Name: name, Type: "DIR", Version: fmt.Sprint(entry.Version)}
	fmt.Fprintf(s.Stdout, "%%CREATE-S-CREATED, %s created\n", full.String())
	return nil
}
