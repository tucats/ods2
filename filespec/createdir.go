package filespec

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// MaxDirectoryDepth is how many levels below the MFD an ODS-2 directory
// path can have: VMS 7.3 makes [L1.L2.L3.L4.L5.L6.L7.L8] but refuses a
// ninth level with RMS$_DIR.
const MaxDirectoryDepth = 8

// CreatedDirectory reports one level of a CreateDirectoryPath walk.
type CreatedDirectory struct {
	// Path is the directory's path from the MFD, e.g. ["A", "B"] for
	// [A.B]; empty for the MFD itself.
	Path []string

	// Directory is the directory, open.
	Directory *volume.Directory

	// Created is true if this call made the directory, false if it
	// already existed.
	Created bool
}

// String renders the directory's path the way VMS writes it, "[A.B]".
func (c CreatedDirectory) String() string {
	return formatDirPath(c.Path)
}

// CreateDirectoryPath makes sure every level of the directory path dirs
// exists on vol, creating each one that doesn't, top down -- as VMS's
// CREATE/DIRECTORY [A.B.C] makes [A], then [A.B], then [A.B.C], whichever
// are missing. It returns one CreatedDirectory per level, in that order,
// saying whether this call made it. An empty dirs means the MFD, which
// always exists.
//
// options is called for each level about to be created, with the
// directory it will be created in, and returns that level's options. This
// lets defaults that depend on the parent -- the version limit and the
// protection a new directory inherits (see
// volume.InheritedDirectoryOptions), or an owner of "the parent's" -- be
// worked out against each level's own parent. A nil options means
// volume.InheritedDirectoryOptions.
//
// Each component must be a plain name: wildcards ("*", "%") are refused,
// since a directory being made has to be one directory. A path more than
// MaxDirectoryDepth levels deep, or with a name too long, wraps
// volume.ErrDirectoryName, and nothing is made. If a level can't
// be made, the levels already made stay made (as they do on VMS), and the
// returned slice reports them, along with the error. bm and ib are the
// volume's bitmap caches, whose changes are in memory until the caller
// Flushes them.
func CreateDirectoryPath(vol *volume.Volume, dirs []string, options func(parent *volume.Directory) volume.DirectoryOptions, bm *volume.Bitmap, ib *volume.IndexBitmap) ([]CreatedDirectory, error) {
	if options == nil {
		options = volume.InheritedDirectoryOptions
	}

	for _, d := range dirs {
		if d == "" || strings.ContainsAny(d, "*%") {
			return nil, fmt.Errorf("filespec: creating %s: %q is not a directory name", formatDirPath(dirs), d)
		}
	}

	// Checked before anything is made: VMS makes none of a path that's too
	// deep, or has a name too long, rather than the levels above it.
	if len(dirs) > MaxDirectoryDepth {
		return nil, fmt.Errorf("filespec: creating %s: more than %d levels: %w", formatDirPath(dirs), MaxDirectoryDepth, volume.ErrDirectoryName)
	}

	for _, d := range dirs {
		if len(d) > volume.MaxDirectoryNameLength {
			return nil, fmt.Errorf("filespec: creating %s: %q is longer than %d characters: %w", formatDirPath(dirs), d, volume.MaxDirectoryNameLength, volume.ErrDirectoryName)
		}
	}

	current, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		return nil, fmt.Errorf("filespec: opening the master file directory: %w", err)
	}

	if len(dirs) == 0 {
		return []CreatedDirectory{{Directory: current}}, nil
	}

	var levels []CreatedDirectory

	for i := range dirs {
		path := make([]string, i+1)
		for j := range path {
			path[j] = strings.ToUpper(dirs[j])
		}

		name := path[i] + ".DIR"

		entry, err := current.Lookup(name, 0)

		switch {
		case err == nil:
			next, err := vol.OpenDirectory(entry.Fid)
			if err != nil {
				return levels, fmt.Errorf("filespec: opening %s: %w", formatDirPath(path), err)
			}

			current = next
			levels = append(levels, CreatedDirectory{Path: path, Directory: current})

		case errors.Is(err, volume.ErrNotFound):
			next, err := vol.CreateDirectory(current, name, options(current), bm, ib)
			if err != nil {
				return levels, fmt.Errorf("filespec: creating %s: %w", formatDirPath(path), err)
			}

			current = next
			levels = append(levels, CreatedDirectory{Path: path, Directory: current, Created: true})

		default:
			return levels, fmt.Errorf("filespec: looking up %s: %w", formatDirPath(path), err)
		}
	}

	return levels, nil
}
