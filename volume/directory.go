package volume

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tucats/ods2/diskimage"
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

	// Bound the walk by UsedBlocks, not Blocks(): a directory routinely
	// has more blocks allocated than it currently uses (VMS pre-extends
	// by HomeBlock.DefaultExtendSize at a time), and that trailing slack
	// — whether it's real, physically-zeroed disk space below the
	// high-water mark, or still-unwritten space at/beyond it — isn't
	// directory content and can't be decoded as any. See UsedBlocks'
	// own doc comment for why Blocks() alone isn't the right bound here.
	limit := d.Blocks()
	if used := d.UsedBlocks(); used < limit {
		limit = used
	}

	for vbn := uint32(1); vbn <= limit; vbn++ {
		if d.isUnwritten(vbn) {
			// Should be unreachable given the UsedBlocks-derived limit
			// above (a well-formed header never records data as "used"
			// past its own high-water mark) — kept as a defensive
			// fallback so a header with an internally inconsistent
			// EndOfFileBlock/HighWaterMark still stops here rather than
			// trying to decode simulated-zero content as real records.
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

// NextVersion reports the version number a new entry named name should be
// given: one more than the highest existing version of that name already in
// the directory, or 1 if the name isn't present at all -- the same "highest
// existing version + 1" rule the reference implementation's search_ent()
// applies when creating a new file (direct.c:630-636).
func (d *Directory) NextVersion(name string) (uint16, error) {
	entries, err := d.List()
	if err != nil {
		return 0, err
	}

	var highest uint16
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) && e.Version > highest {
			highest = e.Version
		}
	}
	return highest + 1, nil
}

// Insert adds one new (name, version, fid) entry to the directory. bm and ib
// are the volume's storage- and index-file bitmap caches (see OpenBitmap/
// OpenIndexBitmap) -- Insert consults them only to grow the directory's own
// data (via Extend) when its current allocation has no room left for the
// larger entry set; it never allocates a file header itself (that's
// CreateHeader's job, on the caller's side of a full "create a file"
// operation -- see docs/PHASE-02.md subtask 10).
//
// Unlike the reference implementation's insert_ent(), which splices one
// record into an existing block in place and simply gives up
// (`exit(0)`) if that block has no room, Insert re-reads every existing
// entry (List) and re-lays out the directory's COMPLETE entry set --
// existing plus the one being added -- across as many blocks as that takes
// (packDirectoryBlocks), extending the directory first if the new layout
// needs more blocks than it currently has. This is more I/O than a true
// in-place splice, but it turns "the directory needs another block" from a
// special case its own encoder would need in-place-mutation logic for (see
// ondisk.EncodeDirectoryBlock's own doc comment on why it deliberately
// isn't that kind of API) into the ordinary, already-tested "allocate more
// space" path every other file uses.
func (d *Directory) Insert(name string, version uint16, fid ondisk.Fid, bm *Bitmap, ib *IndexBitmap) error {
	container, ok := d.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: inserting %s;%d: device is not open for write", name, version)
	}

	entries, err := d.List()
	if err != nil {
		return fmt.Errorf("volume: inserting %s;%d: %w", name, version, err)
	}
	entries = append(entries, ondisk.DirEntry{Name: name, Version: version, Fid: fid})

	blocks, err := packDirectoryBlocks(entries)
	if err != nil {
		return fmt.Errorf("volume: inserting %s;%d: %w", name, version, err)
	}

	if needed := uint32(len(blocks)); needed > d.Blocks() {
		if err := Extend(d.File, bm, ib, needed-d.Blocks()); err != nil {
			return fmt.Errorf("volume: inserting %s;%d: extending directory: %w", name, version, err)
		}
	}

	for i, block := range blocks {
		vbn := uint32(i + 1)
		lbn, err := resolveExtentLBN(d.Extents, vbn)
		if err != nil {
			return fmt.Errorf("volume: inserting %s;%d: locating directory block %d: %w", name, version, vbn, err)
		}
		if err := container.WriteBlock(lbn, block); err != nil {
			return fmt.Errorf("volume: inserting %s;%d: writing directory block %d: %w", name, version, vbn, err)
		}
	}

	if err := d.recordUsedBlocks(container, uint32(len(blocks))); err != nil {
		return fmt.Errorf("volume: inserting %s;%d: %w", name, version, err)
	}

	return nil
}

// Remove deletes one specific (name, version) entry from the directory --
// the mirror image of Insert. Like Insert, it doesn't splice a single
// record out in place; instead it re-reads the full entry set (List),
// drops the one matching entry, and re-lays out everything that's left
// (packDirectoryBlocks) across however many blocks that now takes --
// generally fewer than before, since there's strictly less content to
// pack, though never more blocks than the directory already has allocated
// (see the file-level doc comment above, and
// docs/PHASE-03.md's non-goals, on why this method still never calls
// Extend: removing entries can only shrink a greedy pack, never grow it).
//
// Name matching is case-insensitive, matching every other lookup in this
// package (List/Lookup/NextVersion). Unlike Lookup, version 0 is never
// accepted here as a "highest version" convenience -- an exact version is
// always required, so a caller can never remove the wrong version by
// relying on an implicit default. (The DELETE command, docs/PHASE-03.md
// subtask 4, enforces the same rule one layer up: a bare "DELETE name"
// with no version at all is rejected before it ever reaches here.)
// Removing a (name, version) that isn't present is reported as an error,
// leaving the directory's on-disk content untouched.
//
// bm and ib are the volume's storage- and index-file bitmap caches (see
// OpenBitmap/OpenIndexBitmap) -- they're threaded through purely to keep
// this method's signature symmetric with Insert's (which does consult
// them, to Extend the directory when it needs to grow); Remove itself
// never needs to allocate anything, since removing entries never needs
// more space than the directory already has.
func (d *Directory) Remove(name string, version uint16, bm *Bitmap, ib *IndexBitmap) error {
	if version == 0 {
		return fmt.Errorf("volume: removing %s: a specific version is required (0 is not a valid version)", name)
	}

	container, ok := d.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: removing %s;%d: device is not open for write", name, version)
	}

	entries, err := d.List()
	if err != nil {
		return fmt.Errorf("volume: removing %s;%d: %w", name, version, err)
	}

	remaining := make([]ondisk.DirEntry, 0, len(entries))
	found := false
	for _, e := range entries {
		if !found && strings.EqualFold(e.Name, name) && e.Version == version {
			found = true
			continue
		}
		remaining = append(remaining, e)
	}
	if !found {
		return fmt.Errorf("volume: removing %s;%d: not found", name, version)
	}

	blocks, err := packDirectoryBlocks(remaining)
	if err != nil {
		return fmt.Errorf("volume: removing %s;%d: %w", name, version, err)
	}

	for i, block := range blocks {
		vbn := uint32(i + 1)
		lbn, err := resolveExtentLBN(d.Extents, vbn)
		if err != nil {
			return fmt.Errorf("volume: removing %s;%d: locating directory block %d: %w", name, version, vbn, err)
		}
		if err := container.WriteBlock(lbn, block); err != nil {
			return fmt.Errorf("volume: removing %s;%d: writing directory block %d: %w", name, version, vbn, err)
		}
	}

	// recordUsedBlocks may move HighWaterMark BACKWARD here, unlike every
	// other caller of it (Insert only ever grows) -- see its own doc
	// comment. Any now-stale bytes physically sitting in blocks beyond the
	// new count are harmless: List's UsedBlocks-bounded walk never reads
	// past the new, smaller HighWaterMark, so that leftover content is
	// simply never looked at again unless a future Insert overwrites it.
	if err := d.recordUsedBlocks(container, uint32(len(blocks))); err != nil {
		return fmt.Errorf("volume: removing %s;%d: %w", name, version, err)
	}

	return nil
}

// recordUsedBlocks rewrites the directory's own header so that
// File.UsedBlocks/File.isUnwritten correctly reflect that its first
// usedBlocks virtual blocks now hold real, freshly-written content: both
// RecordAttributes.EndOfFileBlock/FirstFreeByte (the logical-size fields
// UsedBlocks reads -- see its own doc comment) and HighWaterMark (the
// isUnwritten guard).
//
// This is the mirror image of Extend's own final header rewrite, which
// updates only HighestBlock and deliberately leaves HighWaterMark alone --
// Extend allocates space without writing anything into it, so leaving
// HighWaterMark behind is what keeps that new space reading back as zero.
// Insert is the opposite case: every block up to usedBlocks was just
// overwritten with real directory content, so HighWaterMark must advance to
// match, or a later Directory.List/File.ReadBlock would wrongly treat that
// freshly-written data as still-unwritten simulated-zero space -- List, in
// particular, would stop reading (or even fail to decode) real entries this
// call just wrote.
//
// Directory blocks are always written out at exactly ondisk.BlockSize bytes
// (see packDirectoryBlocks/ondisk.EncodeDirectoryBlock), so the directory's
// real content always ends precisely on a block boundary -- the
// FirstFreeByte-0, EndOfFileBlock-one-past-the-last-real-block convention
// File.UsedBlocks' own doc comment describes.
func (d *Directory) recordUsedBlocks(container diskimage.WritableContainer, usedBlocks uint32) error {
	h := d.Header
	h.RecordAttributes.EndOfFileBlock = usedBlocks + 1
	h.RecordAttributes.FirstFreeByte = 0
	h.HighWaterMark = usedBlocks + 1

	areas, err := existingAreas(h)
	if err != nil {
		return fmt.Errorf("reconstructing existing header content: %w", err)
	}
	decoded, err := writeHeader(d.Device, container, h.Fid.Number(), h, areas)
	if err != nil {
		return fmt.Errorf("recording directory's new logical size: %w", err)
	}
	d.Header = decoded
	return nil
}

// packDirectoryBlocks lays entries out across as many 512-byte directory
// blocks as needed, using ondisk.EncodeDirectoryBlock (subtask 5) as the
// sole authority on what fits in one block -- this function's only job is
// deciding WHICH entries go in which block, exactly the split
// EncodeDirectoryBlock's own doc comment describes ("volume [decides] which
// entries go in which block, and what to do when they don't fit").
//
// All versions of a single name are always kept together in one block,
// matching how a real VMS directory groups them and how
// DecodeDirectoryBlock (and this package's own List) read them back; a
// single name's version list too large to fit in an empty block by itself
// is reported as an error, since neither this function nor
// EncodeDirectoryBlock has any way to split one name record's version
// entries across two blocks.
func packDirectoryBlocks(entries []ondisk.DirEntry) ([][]byte, error) {
	byName := make(map[string][]ondisk.DirEntry, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if _, seen := byName[e.Name]; !seen {
			names = append(names, e.Name)
		}
		byName[e.Name] = append(byName[e.Name], e)
	}
	sort.Strings(names)

	var blocks [][]byte
	var pending []ondisk.DirEntry

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		block, err := ondisk.EncodeDirectoryBlock(pending)
		if err != nil {
			// Unreachable: every entry set assigned to pending below was
			// already confirmed, at the point it was assigned, to fit.
			return fmt.Errorf("internal error: previously-fitting directory block no longer encodes: %w", err)
		}
		blocks = append(blocks, block)
		pending = nil
		return nil
	}

	for _, name := range names {
		group := byName[name]

		candidate := make([]ondisk.DirEntry, 0, len(pending)+len(group))
		candidate = append(candidate, pending...)
		candidate = append(candidate, group...)
		if _, err := ondisk.EncodeDirectoryBlock(candidate); err == nil {
			pending = candidate
			continue
		}

		if len(pending) == 0 {
			return nil, fmt.Errorf("directory entries for %q don't fit in a single %d-byte block", name, ondisk.BlockSize)
		}
		if err := flush(); err != nil {
			return nil, err
		}
		if _, err := ondisk.EncodeDirectoryBlock(group); err != nil {
			return nil, fmt.Errorf("directory entries for %q don't fit in a single %d-byte block: %w", name, ondisk.BlockSize, err)
		}
		pending = group
	}
	if err := flush(); err != nil {
		return nil, err
	}

	if len(blocks) == 0 {
		// Only reachable if entries itself was empty -- Insert never calls
		// this with an empty set (it always adds the new entry first), but
		// an empty directory is still a well-formed one block of "no
		// entries" rather than zero blocks.
		block, err := ondisk.EncodeDirectoryBlock(nil)
		if err != nil {
			return nil, err
		}
		blocks = [][]byte{block}
	}

	return blocks, nil
}
