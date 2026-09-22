package volume

import (
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// freeFileStorage reclaims every header slot and every data extent
// belonging to a file's header chain (its primary segment, plus however
// many extension segments follow it via ExtensionFid — see
// fileHeaderChain), given the file's already-loaded primary header.
//
// This function does NOT touch any directory entry. See
// docs/PHASE-03.md's "Ordering for safety without journaling" for why
// removing a file's directory entry (Directory.Remove) is always done
// separately, and strictly BEFORE this function is ever called — the
// caller (DeleteFile, below) is responsible for that ordering; this
// function only knows how to reclaim what a header chain describes.
//
// For each segment in the chain, in order: every extent in its
// retrieval-pointer map is marked free in bm (Bitmap.MarkFree), the
// segment's own header slot is marked free in ib (IndexBitmap.MarkFree),
// and the slot's on-disk bytes are overwritten with an all-zero block.
//
// That last step matters for more than tidiness. IndexBitmap.FindFreeSlot
// doesn't just trust a clear bitmap bit before handing a slot back out —
// it re-reads the slot's actual header and requires BOTH a zero checksum
// and a zero file number (see IndexBitmap's own doc comment and its
// FindFreeSlot). Marking the bitmap bit free without also erasing the
// slot's stale header bytes would leave that check failing forever, since
// a since-deleted file's old header content (nonzero checksum, nonzero
// Fid) would still be sitting there — FindFreeSlot would treat the slot as
// permanently unusable even though its bitmap bit says it's free. Writing
// an all-zero 512-byte block avoids this: it decodes with Checksum == 0
// (the checksum of an all-zero block is itself zero — see
// ondisk.Checksum) and Fid.Number() == 0, exactly satisfying the check.
//
// bm and ib mutations are in-memory only until their own Flush is called,
// matching every other write-path operation's deferred-flush design (see
// docs/PHASE-02.md's "Caching strategy") — this function never flushes
// them itself, leaving that to the caller (typically once, after deleting
// however many files one command needs to). Zeroing a header slot's
// on-disk bytes, by contrast, is an immediate write, matching how this
// package treats every other header write (CreateHeader, Extend).
//
// A failure partway through this walk (for instance, a disk I/O error
// freeing the second of three segments) is reported but not rolled back.
// By the time this function is ever called, the file's directory entry has
// already been removed (per the ordering note above), so there is no
// consistent state to roll back TO — whatever this call already freed
// stays freed, and whatever it didn't reach becomes an orphan: still
// marked allocated in one or both bitmaps, holding real but now
// unreachable header content. That's a real but bounded cost, accepted
// explicitly in docs/PHASE-03.md's non-goals — a future ANALYZE/DISK
// enhancement scanning for header slots with no owning directory entry
// would be able to find and reclaim it.
func freeFileStorage(dev *Device, primary ondisk.FileHeader, bm *Bitmap, ib *IndexBitmap) error {
	container, ok := dev.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: freeing file %v: device is not open for write", primary.Fid)
	}

	chain, err := fileHeaderChain(dev, primary)
	if err != nil {
		return fmt.Errorf("volume: freeing file %v: %w", primary.Fid, err)
	}

	zero := make([]byte, ondisk.BlockSize)

	for _, segment := range chain {
		extents, err := segment.RetrievalPointers()
		if err != nil {
			return fmt.Errorf("volume: freeing file %v: decoding retrieval pointers for segment %v: %w", primary.Fid, segment.Fid, err)
		}
		for _, e := range extents {
			if err := bm.MarkFree(e); err != nil {
				return fmt.Errorf("volume: freeing file %v: freeing extent of segment %v: %w", primary.Fid, segment.Fid, err)
			}
		}

		if err := ib.MarkFree(segment.Fid.Number()); err != nil {
			return fmt.Errorf("volume: freeing file %v: freeing header slot for segment %v: %w", primary.Fid, segment.Fid, err)
		}

		if _, err := writeHeaderBytes(dev, container, segment.Fid.Number(), zero); err != nil {
			return fmt.Errorf("volume: freeing file %v: zeroing header slot for segment %v: %w", primary.Fid, segment.Fid, err)
		}
	}

	return nil
}

// DeleteFile removes one specific (name, version) file from dir and fully
// reclaims everything it owned — the API docs/PHASE-03.md's Goals section
// describes as "precisely reversing what CreateHeader/Extend/
// Directory.Insert did to create the file in the first place." It ties
// together Directory.Remove (directory.go) and freeFileStorage (above) in
// the exact order the design doc's "Ordering for safety without
// journaling" section works through in detail; see that section for why
// the order matters and what happens if this is interrupted partway
// through.
//
// name is the file's combined "NAME.TYPE" text, matching every other
// Directory method that takes a name (Insert, Lookup, NextVersion,
// Remove) and the same combined-name convention CreateFile's own callers
// already use — it is NOT split into separate name/type parts anywhere in
// this package.
//
// Unlike Directory.Lookup, version 0 is never accepted here as a "highest
// version" convenience — exactly like Directory.Remove itself, an exact
// version is always required, so a caller can never delete the wrong
// version of a name by relying on an implicit default. (The DELETE
// command, docs/PHASE-03.md subtask 4, enforces the same rule one layer
// up: a bare "DELETE name" with no version at all is rejected before it
// ever reaches here.) This is checked before anything else, so a caller's
// mistake is reported without even reading the file's header.
//
// The volume's master file directory (MFD — the fixed, always-present
// top-level directory every other directory and file is ultimately
// reached from, identified by the fixed file number ondisk.
// MasterFileDirectoryFid.Number()) can never be deleted, full stop, even
// if it happens to be empty. There's no design-doc precedent to fall back
// on for what "an unmounted volume with no MFD at all" would even mean —
// every other piece of this package (Volume.OpenDirectory,
// filespec.ResolveDirectory's root, ANALYZE/DISK's own walk) assumes the
// MFD is always there to start from — so this is refused unconditionally
// rather than treated as an ordinary (if unusual) empty-directory delete.
// INDEXF.SYS and BITMAP.SYS (ondisk.IndexFileFid, ondisk.BitmapFileFid) get
// the identical unconditional refusal, and for the identical reason: the
// volume as a whole depends on both of them existing, so there is no
// meaningful state for the volume to be in without them.
//
// If the file being deleted is itself a directory (its header has the
// FchDirectory characteristic set — see ondisk.FileHeader.IsDirectory), it
// is only ever deleted while EMPTY: still containing even one entry (a
// file or a subdirectory) is rejected outright, before anything is
// removed. VMS itself enforces the same rule, for the same reason —
// deleting a non-empty directory would orphan every file/subdirectory it
// still names: each one's own header would keep pointing back at this
// directory's Fid (via Backlink) as its parent, but nothing would ever
// find them again through an ordinary directory walk starting from the
// volume's master file directory, since the one directory entry that led
// here would be gone. Reclaiming an empty directory's own storage once
// it's confirmed empty works exactly like reclaiming any other file's —
// there's nothing directory-specific about steps 4-5 below.
//
// The steps, strictly in this order:
//
//  1. Look up the (name, version) entry to learn its Fid, and refuse
//     outright if that Fid's file number is the MFD's, INDEXF.SYS's, or
//     BITMAP.SYS's own fixed file number.
//  2. Read the primary FileHeader through that Fid (readFileHeaderViaIndex)
//     — this has to happen BEFORE the directory entry naming the file is
//     removed, since nothing else will be able to find the file's header
//     once step 4 below succeeds.
//  3. If that header is itself a directory, resolve its full data
//     (buildFile) and List its entries; refuse with an error, before
//     touching anything, if even one entry is found.
//  4. Directory.Remove the (name, version) entry. Once this returns
//     successfully, the file is unambiguously gone from every reader's
//     point of view, regardless of whether step 5 below ever completes —
//     see the design-doc section named above for why this ordering, not
//     the reverse, keeps a crash's worst case bounded to a recoverable
//     orphan rather than a directory entry pointing at corrupt-looking,
//     already-zeroed header content.
//  5. freeFileStorage walks the file's complete header-extension chain
//     (primary segment plus every extension segment, if any) and returns
//     every header slot and every data extent it owns to ib/bm.
//
// A failure in step 5, after step 4 has already succeeded, is returned as
// an error but is NOT rolled back — there is no way to "undo" a
// directory-entry removal that has already been written to disk, so
// rolling back only the reclamation half wouldn't restore a consistent
// state anyway (again, see the design-doc section above). The caller's
// command layer (DELETE/PURGE, subtasks 4/9) is expected to report such an
// error and move on to (or stop before) its next target, rather than treat
// it as fatal to the whole operation.
//
// bm and ib mutations this makes (via freeFileStorage) are in-memory only
// until their own Flush is called — Directory.Remove's own directory-block
// rewrites, by contrast, are immediate, unbuffered writes, matching every
// other directory mutation in this package.
func DeleteFile(dir *Directory, name string, version uint16, bm *Bitmap, ib *IndexBitmap) error {
	if version == 0 {
		return fmt.Errorf("volume: deleting %s: a specific version is required (0 is not a valid version)", name)
	}

	entry, err := dir.Lookup(name, version)
	if err != nil {
		return fmt.Errorf("volume: deleting %s;%d: %w", name, version, err)
	}

	if entry.Fid.Number() == ondisk.MasterFileDirectoryFid.Number() {
		return fmt.Errorf("volume: deleting %s;%d: the volume's master file directory cannot be deleted", name, version)
	}

	// INDEXF.SYS (file number 1) and BITMAP.SYS (file number 2) are the
	// volume's own index file and storage-allocation bitmap — see
	// ondisk.IndexFileFid and ondisk.BitmapFileFid. Every other structure on
	// the volume, including every other file's own header, is only
	// reachable through these two: INDEXF.SYS is where every file header
	// (including the volume's own) physically lives, and BITMAP.SYS is the
	// sole record of which blocks are free versus allocated. Deleting
	// either would not just lose one file's data, it would make the entire
	// volume unreadable to any ODS-2 implementation, VMS or otherwise — so
	// this refuses unconditionally, the same way the MFD check above does.
	//
	// This check is deliberately keyed on file number (entry.Fid.Number()),
	// not on the directory entry's name, per the guard's own requirement:
	// a caller cannot bypass it by renaming these files (ODS-2 has no
	// concept of a file being unrenameable, but the file number identifying
	// INDEXF.SYS/BITMAP.SYS never changes regardless of what name any
	// directory happens to list them under).
	if n := entry.Fid.Number(); n == ondisk.IndexFileFid.Number() || n == ondisk.BitmapFileFid.Number() {
		return fmt.Errorf("volume: deleting %s;%d: the volume's index file and storage bitmap cannot be deleted", name, version)
	}

	dev := dir.Device
	primary, err := readFileHeaderViaIndex(dev, dev.IndexFile.Extents, entry.Fid)
	if err != nil {
		return fmt.Errorf("volume: deleting %s;%d: %w", name, version, err)
	}

	if primary.IsDirectory() {
		empty, err := isEmptyDirectory(dev, primary)
		if err != nil {
			return fmt.Errorf("volume: deleting %s;%d: %w", name, version, err)
		}
		if !empty {
			return fmt.Errorf("volume: deleting %s;%d: directory is not empty", name, version)
		}
	}

	if err := dir.Remove(name, version, bm, ib); err != nil {
		return fmt.Errorf("volume: deleting %s;%d: %w", name, version, err)
	}

	if err := freeFileStorage(dev, primary, bm, ib); err != nil {
		return fmt.Errorf("volume: deleting %s;%d: directory entry removed, but reclaiming its storage failed: %w", name, version, err)
	}

	return nil
}

// SetVersionLimit rewrites f's own RecordAttributes.VersionLimit — the
// only way, per docs/PHASE-03.md, to actually change a file's (or
// directory's) version limit after it's been created; see
// docs/PHASE-03.md's "Version-limit design" for why that field lives on
// the file's own header rather than anywhere else. Works identically
// whether f is a plain file or a directory: a directory is a File whose
// header happens to also have FchDirectory set, and its own
// RecordAttributes.VersionLimit is read exactly the same way (as the
// default new names created directly inside it inherit — see
// resolveVersionLimit, writefile.go) regardless of that bit.
//
// This is an immediate header rewrite, like CreateHeader/Extend and
// unlike WriteBlock/Close's deferred bookkeeping — there is no bitmap
// mutation involved at all (limit is just one field of an already-
// allocated header being changed in place), so there's nothing here for a
// caller to Flush afterward.
func SetVersionLimit(f *File, limit uint16) error {
	container, ok := f.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: setting version limit for file %v: device is not open for write", f.Header.Fid)
	}

	h := f.Header
	h.RecordAttributes.VersionLimit = limit

	areas, err := existingAreas(h)
	if err != nil {
		return fmt.Errorf("volume: setting version limit for file %v: %w", f.Header.Fid, err)
	}
	decoded, err := writeHeader(f.Device, container, h.Fid.Number(), h, areas)
	if err != nil {
		return fmt.Errorf("volume: setting version limit for file %v: %w", f.Header.Fid, err)
	}

	f.Header = decoded
	return nil
}

// PurgeVersions trims name's surviving versions in dir down to exactly
// keep, deleting whatever oldest excess versions are needed via DeleteFile
// — the API behind the PURGE command (docs/PHASE-03.md subtask 9).
//
// keep == 0 is rejected outright: VMS's own PURGE has no "/LIMIT=0 means
// delete every version" meaning, and this project doesn't invent one
// either — deleting every version of a name is exactly what `DELETE
// name;*` (DeleteFile called once per surviving version) already provides,
// cleanly, with no need for PurgeVersions to also support the degenerate
// case.
//
// A name with keep or fewer surviving versions is left completely
// untouched — no DeleteFile call, and therefore no directory rewrite or
// bitmap mutation at all — rather than this function doing unnecessary
// work just to arrive back at the same state.
func PurgeVersions(dir *Directory, name string, keep uint16, bm *Bitmap, ib *IndexBitmap) error {
	if keep == 0 {
		return fmt.Errorf("volume: purging %s: /LIMIT must be at least 1 (DELETE %s;* removes every version)", name, name)
	}

	entries, err := dir.List()
	if err != nil {
		return fmt.Errorf("volume: purging %s: %w", name, err)
	}

	for _, v := range excessVersions(entries, name, keep) {
		if err := DeleteFile(dir, name, v, bm, ib); err != nil {
			return fmt.Errorf("volume: purging %s: %w", name, err)
		}
	}
	return nil
}

// isEmptyDirectory reports whether the directory file described by
// primary — already confirmed by the caller (DeleteFile) to have the
// FchDirectory characteristic set — currently has zero directory entries.
// It resolves the directory's complete data (buildFile, the same
// retrieval-pointer walk every other file's data is read through) rather
// than trusting anything in the header alone, since a directory's entry
// count isn't itself a header field — the only way to know is to actually
// read and decode its data blocks (Directory.List).
func isEmptyDirectory(dev *Device, primary ondisk.FileHeader) (bool, error) {
	f, err := buildFile(dev, primary)
	if err != nil {
		return false, fmt.Errorf("resolving directory %v's data extents: %w", primary.Fid, err)
	}

	d, err := f.Directory()
	if err != nil {
		// Unreachable: the caller has already confirmed primary.IsDirectory()
		// is true, which is exactly what File.Directory() itself checks.
		return false, err
	}

	entries, err := d.List()
	if err != nil {
		return false, fmt.Errorf("listing directory %v's entries: %w", primary.Fid, err)
	}
	return len(entries) == 0, nil
}
