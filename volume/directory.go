package volume

import (
	"fmt"
	"strings"

	"github.com/tucats/ods2/ondisk"
)

// Directory is a File whose data holds directory records rather than
// arbitrary file content — every ODS-2 directory is simply a file with
// the "directory" characteristic bit set in its header (see
// ondisk.FileHeader.IsDirectory), and its data, read block by block, is a
// sequence of records decoded by ondisk.DecodeDirectoryBlock.
type Directory struct {
	*File
}

// Directory reinterprets an already-open File as a Directory, failing if
// the file's header doesn't actually have the directory characteristic
// set.
func (f *File) Directory() (*Directory, error) {
	if !f.Header.IsDirectory() {
		return nil, fmt.Errorf("volume: file %v is not a directory", f.Header.Fid)
	}
	return &Directory{File: f}, nil
}

// OpenDirectory opens the directory identified by fid — a convenience for
// vol.OpenFID(fid) followed by (*File).Directory().
func (vol *Volume) OpenDirectory(fid ondisk.Fid) (*Directory, error) {
	f, err := vol.OpenFID(fid)
	if err != nil {
		return nil, err
	}
	return f.Directory()
}

// List returns every entry recorded in the directory, across all of its
// data blocks, in on-disk order: entries are grouped by name (ascending),
// with every existing version of a given name appearing together under
// it.
func (d *Directory) List() ([]ondisk.DirEntry, error) {
	var all []ondisk.DirEntry
	buf := make([]byte, ondisk.BlockSize)

	for vbn := uint32(1); vbn <= d.Blocks(); vbn++ {
		if d.isUnwritten(vbn) {
			// VMS routinely pre-extends a directory file by several
			// blocks at a time (see HomeBlock.DefaultExtendSize) and
			// leaves the slack unwritten until it's actually needed.
			// Blocks() reports the directory's full *allocation*, which
			// can run ahead of how much of it has ever actually been
			// written; a block at or beyond the high-water mark reads
			// back as all-zero (see File.ReadBlock) rather than holding
			// a legitimately empty directory block, so it can't be
			// decoded as one. Every later VBN is unwritten too (the
			// high-water mark only ever grows as a file is extended, so
			// nothing beyond it is written while an earlier block isn't),
			// so there's nothing more to find past this point.
			break
		}
		if err := d.ReadBlock(vbn, buf); err != nil {
			return nil, fmt.Errorf("volume: reading directory block %d: %w", vbn, err)
		}
		entries, err := ondisk.DecodeDirectoryBlock(buf)
		if err != nil {
			return nil, fmt.Errorf("volume: decoding directory block %d: %w", vbn, err)
		}
		all = append(all, entries...)
	}

	return all, nil
}

// Lookup finds one specific (name, version) entry in the directory. Name
// matching is case-insensitive, matching VMS's own convention. Passing
// version 0 — not itself a legal VMS version number, so this is
// unambiguous — selects the highest existing version of that name instead
// of an exact version.
//
// This is a simple, non-wildcard lookup: matching wildcards like "*" and
// "%" against directory contents, and resolving relative version
// references like ";-1", are handled by package filespec, built on top of
// List rather than by this method.
func (d *Directory) Lookup(name string, version uint16) (ondisk.DirEntry, error) {
	entries, err := d.List()
	if err != nil {
		return ondisk.DirEntry{}, err
	}

	var best *ondisk.DirEntry
	for i := range entries {
		e := &entries[i]
		if !strings.EqualFold(e.Name, name) {
			continue
		}
		if version != 0 {
			if e.Version == version {
				return *e, nil
			}
			continue
		}
		if best == nil || e.Version > best.Version {
			best = e
		}
	}

	if best != nil {
		return *best, nil
	}
	if version != 0 {
		return ondisk.DirEntry{}, fmt.Errorf("volume: %s;%d not found", name, version)
	}
	return ondisk.DirEntry{}, fmt.Errorf("volume: %s not found", name)
}
