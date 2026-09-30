package volume

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
)

// This file implements renaming a file: giving an existing file a new
// name, a new version, a new directory, or any mix of the three, without
// copying its data.
//
// # What a rename actually changes
//
// A file on an ODS-2 volume is its header (in INDEXF.SYS) plus its data
// blocks. Its *name* lives in a directory entry that points at the header
// by file ID (FID). Renaming therefore never touches the data at all: it
// removes the old directory entry, and adds a new entry, in the same or
// another directory, pointing at the same FID. Because both directories
// have to point into the same index file, a file can only be renamed
// within its own volume; moving it to another volume is a copy followed
// by a delete, which VMS's RENAME refuses to do (see ErrCrossVolume).
//
// The header also keeps two copies of naming information for the file
// system's own bookkeeping, and both are brought up to date:
//
//   - its *back link*, the FID of the directory the file's primary entry
//     is in (what ANALYZE/DISK uses to find a file's name from its header,
//     and what VMS uses to find a directory's parent);
//   - its *identification area*'s file name, "NAME.TYP;VERSION".
//
// This is how the VMS file system (the F11BXQP, whose ENTER, REMOVE and
// CREATE routines RMS's $RENAME drives -- see the VMS V7.3 sources,
// RMS0RENAM.MAR, DELETE.B32 and CREATE.B32) does it: removing a file's
// *primary* entry clears the back link; entering a name for a file whose
// back link is clear sets it to the new directory and writes the new name
// into the header, and counts as a revision (the revision count goes up,
// the revision date becomes now, and the backup date is cleared, since the
// file has changed since it was backed up). A secondary ("alias") entry --
// a second name for a file whose back link points elsewhere -- can be
// renamed too, and then the header is left alone.
//
// # Versions
//
// A version of 0 for the old name means the highest existing version, as
// in Directory.Lookup. A version of 0 for the new name means "the next
// one": one more than the highest version of the new name already in the
// new directory, or 1 if there is none -- computed after the old entry is
// removed, as VMS does, so renaming A.TXT;3 to plain A.TXT in the same
// directory, when ;3 is the only version, gives A.TXT;1. An explicit new
// version that already exists is refused (ErrExists, VMS's
// SS$_DUPFILENAME); renaming never replaces another file.
//
// Adding a version to a name that is already at its version limit
// deletes the name's oldest version to make room, exactly as creating a
// new version does (VMS's SS$_FILEPURGED).
//
// # Directories
//
// A directory file (NAME.DIR;1) can be renamed or moved to another
// directory like any other file; everything under it moves with it, since
// its entries still point at the same subdirectories and files. What it
// can't do is move into itself or into one of its own subdirectories,
// which would cut the whole subtree off from the master file directory:
// that is refused (ErrDirectoryLoop, RMS's RMS$_IDR, "invalid directory
// rename operation").
//
// # If entering the new name fails
//
// VMS removes the old entry before entering the new one, and if the enter
// fails (the new directory can't grow, say), puts the old entry back.
// Rename does the same. If even putting it back fails, the file is left
// with no directory entry at all -- still intact, reachable by its FID and
// recoverable with ANALYZE/DISK/REPAIR, but with no name: Rename then
// returns an error wrapping ErrRenameLost (RMS's RMS$_REENT, "file could
// not be renamed and recovery failed; file has been lost"). The ordinary
// failures -- the old file missing, the new version taken, a loop -- are
// all detected before anything on the disk changes.

// Rename's errors, besides ErrNotFound (the old name isn't there) and
// ErrExists (the new name and version already are).
var (
	// ErrCrossVolume: the old and new directories aren't on the same
	// volume. A rename only moves a directory entry, and an entry can
	// only point at a header in its own volume's index file.
	ErrCrossVolume = errors.New("the old and new names must be on the same volume")

	// ErrDirectoryLoop: the file is a directory, and the new directory is
	// that directory itself or one of its subdirectories.
	ErrDirectoryLoop = errors.New("a directory can't be moved into itself or one of its subdirectories")

	// ErrBadVersion: the new version is above 32767, the highest a
	// directory entry can hold (VMS's SS$_BADFILEVER).
	ErrBadVersion = errors.New("version numbers must be between 1 and 32767")

	// ErrRenameLost: entering the new name failed, and so did putting the
	// old one back, so the file has no directory entry (see this file's
	// opening comment).
	ErrRenameLost = errors.New("the file could not be renamed and its old name could not be restored")
)

// maxVersion is the highest version number a directory entry can record:
// VMS keeps versions as positive signed 16-bit numbers.
const maxVersion = 32767

// Renamed is what Rename did.
type Renamed struct {
	// Fid is the renamed file's ID -- unchanged by the rename.
	Fid ondisk.Fid

	// Name and Version are the file's new directory entry. Name is spelled
	// as the caller gave it.
	Name    string
	Version uint16

	// OldName and OldVersion are the entry that was removed, spelled as
	// the old directory had it (so OldVersion is the real version even
	// when the caller asked for version 0, the highest).
	OldName    string
	OldVersion uint16

	// Purged lists the versions of Name deleted to keep it within its
	// version limit, oldest first; usually empty.
	Purged []uint16
}

// Rename gives the file that oldName;oldVersion names in oldDir the name
// newName;newVersion in newDir (see this file's opening comment for the
// rules). Names are NAME.TYP, without a version; versions are as
// described there (0 for the old version means the highest, 0 for the new
// one means the next). oldDir and newDir may be the same directory.
//
// bm and ib are the volume's storage- and index-file bitmap caches (see
// OpenBitmap/OpenIndexBitmap) for newDir's device: entering the new name
// may extend newDir, and keeping the new name within its version limit
// may delete an old version. Like DeleteFile, Rename leaves flushing them
// to the caller.
func (vol *Volume) Rename(oldDir *Directory, oldName string, oldVersion uint16, newDir *Directory, newName string, newVersion uint16, bm *Bitmap, ib *IndexBitmap) (Renamed, error) {
	fail := func(err error) (Renamed, error) {
		return Renamed{}, fmt.Errorf("volume: renaming %s to %s: %w", oldName, newName, err)
	}

	if !vol.holds(oldDir) || !vol.holds(newDir) {
		return fail(ErrCrossVolume)
	}

	if newVersion > maxVersion {
		return fail(ErrBadVersion)
	}

	// The same directory opened twice is still one directory: removing an
	// entry through one copy rewrites the directory (and possibly moves it,
	// see relocate), leaving the other copy's header stale. Use one.
	if sameFid(oldDir.Header.Fid, newDir.Header.Fid) && oldDir.Device == newDir.Device {
		newDir = oldDir
	}

	entry, err := oldDir.Lookup(oldName, oldVersion)
	if err != nil {
		return fail(err)
	}

	file, err := vol.OpenFID(entry.Fid)
	if err != nil {
		return fail(fmt.Errorf("reading the file's header: %w", err))
	}

	if file.Header.IsDirectory() {
		if err := vol.checkNotAncestor(entry.Fid, newDir); err != nil {
			return fail(err)
		}
	}

	// An explicit new version that's taken is refused before anything
	// changes -- unless the entry taking it is the one being renamed
	// (renaming a file to its own name), which the removal frees.
	if newVersion != 0 && !(newDir == oldDir && strings.EqualFold(newName, entry.Name) && newVersion == entry.Version) {
		switch _, err := newDir.Lookup(newName, newVersion); {
		case err == nil:
			return fail(fmt.Errorf("%s;%d: %w", newName, newVersion, ErrExists))
		case !errors.Is(err, ErrNotFound):
			return fail(err)
		}
	}

	// Whether this is the file's primary entry -- the one its header's back
	// link and name describe -- decides whether the header changes.
	primary := isPrimaryEntry(file.Header, oldDir, entry)

	if err := oldDir.Remove(entry.Name, entry.Version, bm, ib); err != nil {
		return fail(err)
	}

	result, limit, err := enterRenamed(newDir, newName, newVersion, entry.Fid, bm, ib)
	if err != nil {
		// Put the old entry back, as VMS does, so a failed rename changes
		// nothing.
		if restoreErr := oldDir.Insert(entry.Name, entry.Version, entry.Fid, bm, ib); restoreErr != nil {
			return fail(errors.Join(err, fmt.Errorf("%w: %v", ErrRenameLost, restoreErr)))
		}

		return fail(err)
	}

	result.Fid = entry.Fid
	result.OldName = entry.Name
	result.OldVersion = entry.Version

	// From here on the file has its new name; a failure leaves it so.
	if primary {
		if err := renameHeader(file, newDir, result, limit); err != nil {
			return result, fmt.Errorf("volume: renaming %s to %s: %w", oldName, newName, err)
		}
	}

	result.Purged, err = purgeForRename(newDir, result.Name, result.Version, limit, bm, ib)
	if err != nil {
		return result, fmt.Errorf("volume: renaming %s to %s: %w", oldName, newName, err)
	}

	return result, nil
}

// enterRenamed is Rename's enter step: it picks the new version (the
// next one, when version is 0) and enters name;version for fid in dir. It
// also returns the version limit the name now has (see
// resolveVersionLimit): the one its existing versions carry, or dir's
// default for a new name. On error, dir is unchanged.
func enterRenamed(dir *Directory, name string, version uint16, fid ondisk.Fid, bm *Bitmap, ib *IndexBitmap) (Renamed, uint16, error) {
	if version == 0 {
		next, err := dir.NextVersion(name)
		if err != nil {
			return Renamed{}, 0, err
		}

		if next > maxVersion {
			return Renamed{}, 0, fmt.Errorf("%s: %w", name, ErrBadVersion)
		}

		version = next
	}

	limit, err := resolveVersionLimit(dir, name, version)
	if err != nil {
		return Renamed{}, 0, err
	}

	if err := dir.Insert(name, version, fid, bm, ib); err != nil {
		return Renamed{}, 0, err
	}

	return Renamed{Name: name, Version: version}, limit, nil
}

// purgeForRename keeps name within its version limit after version was
// entered for the renamed file, deleting the oldest *other* versions. It
// differs from enforceVersionLimit in exactly one way: the renamed file is
// never the one deleted, even when it was given a version lower than the
// ones already there -- VMS's ENTER makes room by removing the lowest
// existing version, never the one being entered.
func purgeForRename(dir *Directory, name string, version, limit uint16, bm *Bitmap, ib *IndexBitmap) ([]uint16, error) {
	if limit == 0 {
		return nil, nil
	}

	entries, err := dir.List()
	if err != nil {
		return nil, err
	}

	var others []uint16

	for _, e := range entries {
		if strings.EqualFold(e.Name, name) && e.Version != version {
			others = append(others, e.Version)
		}
	}

	// The renamed file counts toward the limit too.
	if len(others)+1 <= int(limit) {
		return nil, nil
	}

	sort.Slice(others, func(i, j int) bool { return others[i] < others[j] })
	excess := others[:len(others)+1-int(limit)]

	var purged []uint16

	for _, v := range excess {
		if err := DeleteFile(dir, name, v, bm, ib); err != nil {
			return purged, fmt.Errorf("deleting %s;%d to stay within the version limit: %w", name, v, err)
		}

		purged = append(purged, v)
	}

	return purged, nil
}

// renameHeader records the rename in the file's header (see this file's
// opening comment): its back link now names dir, its identification area
// the new name, and the rename counts as a revision. An ordinary file's
// version limit becomes limit, the new name's (see enterRenamed); a
// directory's is its own default for the files in it, so it stays.
func renameHeader(f *File, dir *Directory, r Renamed, limit uint16) error {
	now := vmstime.FromTime(time.Now())
	filename, extension := ondisk.IdentName(r.Name, r.Version)

	return UpdateHeader(f, func(h *ondisk.FileHeader, id *ondisk.Ident) {
		h.Backlink = dir.Header.Fid

		if !h.IsDirectory() {
			h.RecordAttributes.VersionLimit = limit
		}

		id.Filename = filename
		id.FilenameExtension = extension
		id.Revision++
		id.RevisionDate = now
		id.BackupDate = 0
	})
}

// isPrimaryEntry reports whether entry, in dir, is the primary directory
// entry for the file whose header is h -- the F11BXQP's ALIAS_ENTRY test
// turned around. It is when the header has no back link at all, or when
// the back link is dir and the header's recorded name is entry's.
//
// Headers written by older tools may record the name without its
// ";version", so either spelling matches.
func isPrimaryEntry(h ondisk.FileHeader, dir *Directory, entry ondisk.DirEntry) bool {
	if h.Backlink.Number() == 0 {
		return true
	}

	if !sameFid(h.Backlink, dir.Header.Fid) {
		return false
	}

	id, err := h.Ident()
	if err != nil {
		// No readable name to compare: the back link alone says primary.
		return true
	}

	recorded := id.Filename + id.FilenameExtension
	withVersion := fmt.Sprintf("%s;%d", entry.Name, entry.Version)

	return strings.EqualFold(recorded, withVersion) || strings.EqualFold(recorded, entry.Name)
}

// checkNotAncestor refuses to move the directory fid into dir when dir is
// that directory itself or lies beneath it, by following dir's chain of
// back links up to the master file directory. (RMS makes the same check
// while it looks the new directory up, directory by directory -- see
// RM$SETDID's FWA$T_RNM_FID test.) The walk is bounded, so a volume whose
// back links form a cycle can't hang it.
func (vol *Volume) checkNotAncestor(fid ondisk.Fid, dir *Directory) error {
	h := dir.Header

	for range 256 {
		if sameFid(h.Fid, fid) {
			return ErrDirectoryLoop
		}

		parent := h.Backlink
		if parent.Number() == 0 || sameFid(parent, h.Fid) || sameFid(h.Fid, ondisk.MasterFileDirectoryFid) {
			return nil
		}

		f, err := vol.OpenFID(parent)
		if err != nil {
			return fmt.Errorf("following directory back links: %w", err)
		}

		h = f.Header
	}

	return nil
}

// holds reports whether dir is on one of vol's devices.
func (vol *Volume) holds(dir *Directory) bool {
	for _, dev := range vol.Devices {
		if dir.Device == dev {
			return true
		}
	}

	return false
}

// sameFid reports whether a and b name the same file: the same file
// number and sequence number. Relative volume numbers are compared only
// when both are set, since 0 means "this volume" (see ondisk.Fid).
func sameFid(a, b ondisk.Fid) bool {
	if a.Number() != b.Number() || a.Seq != b.Seq {
		return false
	}

	return a.Rvn == 0 || b.Rvn == 0 || a.Rvn == b.Rvn
}
