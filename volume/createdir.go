package volume

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// MaxDirectoryNameLength is the longest name a directory can have, not
// counting its ".DIR" type: ODS-2 allows a file name of at most 39
// characters, and a directory's name is the name part of its NAME.DIR file.
const MaxDirectoryNameLength = 39

// DirectoryOptions holds what CreateDirectory can be told about a new
// directory, beyond its name and parent. Each field's zero value gives what
// VMS's CREATE/DIRECTORY gives without the matching qualifier, except where
// noted; the defaults VMS works out from the PARENT directory (its version
// limit, its owner for /OWNER_UIC=PARENT, its protection) are the caller's
// to resolve, since only the caller knows what was asked for.
type DirectoryOptions struct {
	// VersionLimit becomes the new directory file's own
	// RecordAttributes.VersionLimit: the default version limit for names
	// created directly inside it (docs/PHASE-03.md's "Version-limit
	// design"). 0 means no limit. It's stored as given -- inheriting the
	// parent's limit, as VMS does without /VERSION_LIMIT, is the caller's
	// job.
	VersionLimit uint16

	// Owner is the new directory's owner UIC; nil for the volume's default
	// (see NewFileHeader.Owner).
	Owner *ondisk.Uic

	// Protection is the new directory's protection mask; nil for the
	// volume's default (see NewFileHeader.Protection).
	Protection *uint16

	// Allocation is how many blocks to give the directory at once, rounded
	// up to the volume's cluster size; 0 means 1, VMS's /ALLOCATION default.
	Allocation uint32
}

// CreateDirectory creates a brand-new subdirectory named name inside
// parent, laid out the way VMS 7.3's CREATE/DIRECTORY lays one out:
//
//   - The file's characteristics are "directory" and "contiguous"
//     (ondisk.FchDirectory | ondisk.FchContig). VMS keeps every directory
//     in one contiguous run of blocks (see Directory.relocate), and a
//     directory is born that way.
//   - Its records are variable-length, at most 512 bytes, and may not span
//     blocks (ondisk.AttrNoSpan): a directory record never crosses from one
//     block into the next.
//   - It's given opts.Allocation blocks (1 by default) at once, the first
//     of which is written as an empty directory block (one that reads as
//     "no entries", see ondisk.EncodeDirectoryBlock), so its end of file
//     is just past block 1. The rest, if any, are allocated but unused.
//   - It's always version 1. VMS doesn't keep versions of a directory: if
//     parent already has any version of name, nothing is created and the
//     error wraps ErrExists.
//
// name must end in ".DIR" -- not just a cosmetic convention, but the exact
// suffix filespec.Glob's own directory-walking logic
// (matchingSubdirectories) checks for when it decides whether a directory
// entry names a subdirectory worth descending into -- and the part before
// it can be at most MaxDirectoryNameLength characters.
//
// The new directory's entry in parent is made last, after the directory
// itself is complete, so a failure part way (a full disk, say) never leaves
// parent naming a half-made directory; whatever had been allocated is given
// back. bm and ib are the volume's storage- and index-file bitmap caches
// (see OpenBitmap/OpenIndexBitmap); as with CreateFile, their changes are
// in memory until the caller Flushes them.
func (vol *Volume) CreateDirectory(parent *Directory, name string, opts DirectoryOptions, bm *Bitmap, ib *IndexBitmap) (*Directory, error) {
	upper := strings.ToUpper(name)
	if !strings.HasSuffix(upper, ".DIR") {
		return nil, fmt.Errorf("volume: creating directory %s: name must end in \".DIR\"", name)
	}

	if base := len(name) - len(".DIR"); base == 0 || base > MaxDirectoryNameLength {
		return nil, fmt.Errorf("volume: creating directory %s: name must be 1 to %d characters before \".DIR\"", name, MaxDirectoryNameLength)
	}

	container, ok := parent.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return nil, fmt.Errorf("volume: creating directory %s: device is not open for write", name)
	}

	// Lookup with version 0 finds the highest version, so any version at
	// all means the directory exists.
	if _, err := parent.Lookup(name, 0); err == nil {
		return nil, fmt.Errorf("volume: creating directory %s: %w", name, ErrExists)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("volume: creating directory %s: %w", name, err)
	}

	const version = 1

	f, err := CreateHeader(parent.Device, ib, NewFileHeader{
		Name:            name,
		Version:         version,
		Directory:       parent.Header.Fid,
		Characteristics: ondisk.FchDirectory | ondisk.FchContig,
		RecordAttributes: ondisk.RecAttr{
			Format:        ondisk.RecordFormatVariable,
			Attributes:    ondisk.AttrNoSpan,
			RecordSize:    ondisk.BlockSize,
			MaxRecordSize: ondisk.BlockSize,
			VersionLimit:  opts.VersionLimit,
		},
		Owner:      opts.Owner,
		Protection: opts.Protection,
	})
	if err != nil {
		return nil, fmt.Errorf("volume: creating directory %s;%d: %w", name, version, err)
	}

	dir, err := f.Directory()
	if err != nil {
		// Unreachable: Characteristics: ondisk.FchDirectory above is
		// exactly what File.Directory() itself checks before it will
		// reinterpret a File as a Directory.
		return nil, fmt.Errorf("volume: creating directory %s;%d: %w", name, version, err)
	}

	if err := dir.initialize(container, bm, opts.Allocation); err != nil {
		// Give back the header and whatever space it got.
		_ = DeleteHeader(dir.Device, dir.Header.Fid, bm, ib)

		return nil, fmt.Errorf("volume: creating directory %s;%d: %w", name, version, err)
	}

	if err := parent.Insert(name, version, dir.Header.Fid, bm, ib); err != nil {
		_ = DeleteHeader(dir.Device, dir.Header.Fid, bm, ib)

		return nil, fmt.Errorf("volume: creating directory %s;%d: %w", name, version, err)
	}

	return dir, nil
}

// initialize gives a brand-new (zero-block) directory its first allocation,
// blocks blocks in one contiguous run (1 if blocks is 0), and writes its
// first block as an empty directory block -- exactly what VMS 7.3 leaves in
// a directory nothing has been entered in yet: the 0xFFFF end-of-data
// marker, then zeros. The header then records one block used (end of file
// at block 2, first free byte 0) and the high-water mark past it, as VMS's
// do.
func (d *Directory) initialize(container diskimage.WritableContainer, bm *Bitmap, blocks uint32) error {
	if blocks == 0 {
		blocks = 1
	}

	// relocate allocates a contiguous run and frees the old one, which for
	// a new directory is nothing at all.
	if err := d.relocate(container, bm, blocks); err != nil {
		return err
	}

	empty, err := ondisk.EncodeDirectoryBlock(nil)
	if err != nil {
		return err
	}

	lbn, err := resolveExtentLBN(d.Extents, 1)
	if err != nil {
		return err
	}

	if err := container.WriteBlock(lbn, empty); err != nil {
		return fmt.Errorf("writing the directory's first block: %w", err)
	}

	return d.recordUsedBlocks(container, 1)
}
