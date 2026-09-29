package volume

import (
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// UpdateHeader rewrites f's primary file header after change has modified
// a copy of it: the general-purpose way to change what a header records
// about a file, beyond its data — its record attributes (including the end
// of file), characteristics, owner, protection, and the dates and name in
// its identification (IDENT) area.
//
// This is what VMS's disk ACP does when a program *writes attributes*: a
// $QIO to the disk (IO$_MODIFY, IO$_DEACCESS, IO$_CREATE) can carry an
// "attribute list" naming header fields to set, and RMS uses exactly this
// to record a file's end of file every time it closes one. SetVersionLimit
// (delete.go) is a narrower, older example of the same kind of rewrite.
//
// change receives pointers to a copy of the header's fixed fields and to a
// copy of its IDENT area, and edits them in place. A header with no IDENT
// area at all (legitimate: an extension segment written by the reference
// implementation has none) gets a zero Ident that is thrown away afterward,
// so the header's shape never changes.
//
// Some fields aren't the caller's to change, because other structures on
// the disk depend on them agreeing with the header's own retrieval-pointer
// map or its position in the index file: the header's Fid, its
// ExtensionFid, SegmentNumber, and StructureLevel, and
// RecordAttributes.HighestBlock (how many blocks the map allocates). Any
// change change makes to those is quietly put back before writing — so a
// caller can, for instance, write back a whole 32-byte record attribute
// area a program supplied without first having to patch the allocation
// into it.
//
// Like SetVersionLimit and Extend, this is an immediate header rewrite: no
// bitmap changes, nothing to Flush afterward. f.Header is updated to the
// header as written, so later operations on f (Close, WriteBlock's
// auto-extend) start from the new content rather than overwriting it with
// a stale copy.
func UpdateHeader(f *File, change func(h *ondisk.FileHeader, id *ondisk.Ident)) error {
	container, ok := f.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: updating header of file %v: device is not open for write", f.Header.Fid)
	}

	old := f.Header

	areas, err := existingAreas(old)
	if err != nil {
		return fmt.Errorf("volume: updating header of file %v: %w", old.Fid, err)
	}

	// The copies change edits. ident is a copy too, so a header without an
	// IDENT area can be handed a throwaway one.
	h := old
	ident := ondisk.Ident{}
	if areas.Ident != nil {
		ident = *areas.Ident
	}

	change(&h, &ident)

	// Put back what isn't the caller's to change (see above).
	h.Fid = old.Fid
	h.ExtensionFid = old.ExtensionFid
	h.SegmentNumber = old.SegmentNumber
	h.StructureLevel = old.StructureLevel
	h.RecordAttributes.HighestBlock = old.RecordAttributes.HighestBlock

	if areas.Ident != nil {
		areas.Ident = &ident
	}

	decoded, err := writeHeader(f.Device, container, old.Fid.Number(), h, areas)
	if err != nil {
		return fmt.Errorf("volume: updating header of file %v: %w", old.Fid, err)
	}

	f.Header = decoded

	return nil
}
