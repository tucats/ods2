package volume

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// OpenForWrite arms an already-open File (typically from Volume.OpenFID) so
// WriteBlock can auto-extend it using bm/ib and Close can finalize its EOF
// bookkeeping — the write-path counterpart to opening a file read-only,
// used to modify an EXISTING file's data (e.g. overwriting a block in
// place) rather than create a brand-new one. CreateFile arms a File this
// same way internally, so callers creating a new file from scratch never
// need to call this themselves.
//
// f.maxWrittenVBN is seeded from f.UsedBlocks() — the file's own existing
// notion of how much of it is real content — rather than 0, so that
// reopening a file that already has data and closing it again without
// writing anything new (or after only overwriting existing blocks in
// place) can never shrink its recorded size. See Close's own doc comment
// for how maxWrittenVBN is used.
func (f *File) OpenForWrite(bm *Bitmap, ib *IndexBitmap) error {
	if _, ok := f.Device.Container.(diskimage.WritableContainer); !ok {
		return fmt.Errorf("volume: opening file %v for write: device is not open for write", f.Header.Fid)
	}
	f.bm = bm
	f.ib = ib
	f.maxWrittenVBN = f.UsedBlocks()
	return nil
}

// WriteBlock writes data (exactly ondisk.BlockSize bytes) to virtual block
// vbn of f, which must have been armed for writing first (via CreateFile or
// OpenForWrite) — matching File.ReadBlock's own 1-based VBN numbering.
//
// If vbn is beyond f's current allocation (Blocks()), f is extended first
// (via Extend, using the Bitmap/IndexBitmap it was armed with) so the write
// always lands on real, allocated space; a write within the existing
// allocation, including one that overwrites a block that already holds
// data, requires no extension at all.
//
// Unlike CreateHeader/Extend/Insert, which each rewrite their own header
// changes through to disk immediately (see docs/PHASE-02.md's "Caching
// strategy"), WriteBlock itself never touches f's header — it only records,
// in memory, the highest vbn written so far (see Close), leaving the header
// rewrite as a single deferred step so writing many blocks in a row doesn't
// cost one header rewrite per block.
//
// WriteBlock does not enforce that blocks be written in order. Writing vbn
// 5 without ever writing vbns 2-4 leaves those skipped blocks reading back
// as whatever is physically on the underlying container at their resolved
// LBN — usually genuine zero bytes, since Phase 2 has no file-deletion
// support yet (see docs/PHASE-02.md's non-goals) and therefore never hands
// out a cluster that once belonged to a since-deleted file, but callers
// that need File.ReadBlock's stronger "reads as zero until written"
// guarantee for every block should write in order. This is a block-level
// API; a caller that needs record-level (partial-final-block) accuracy is
// what a future record-aware writer (docs/PHASE-02.md subtask 13) is for.
func (f *File) WriteBlock(vbn uint32, data []byte) error {
	if f.bm == nil || f.ib == nil {
		return fmt.Errorf("volume: WriteBlock: file %v is not open for write", f.Header.Fid)
	}
	if len(data) != ondisk.BlockSize {
		return fmt.Errorf("volume: WriteBlock: data must be exactly %d bytes, got %d", ondisk.BlockSize, len(data))
	}
	if vbn == 0 {
		return fmt.Errorf("volume: WriteBlock: virtual block numbers are 1-based; 0 is not a valid VBN")
	}

	if vbn > f.Blocks() {
		if err := Extend(f, f.bm, f.ib, vbn-f.Blocks()); err != nil {
			return fmt.Errorf("volume: WriteBlock: extending file %v: %w", f.Header.Fid, err)
		}
	}

	container, ok := f.Device.Container.(diskimage.WritableContainer)
	if !ok {
		// Unreachable in practice: OpenForWrite/CreateFile already confirmed
		// this before f.bm/f.ib were ever set, but checked again directly
		// here since this is the actual point of I/O.
		return fmt.Errorf("volume: WriteBlock: device is not open for write")
	}

	lbn, err := resolveExtentLBN(f.Extents, vbn)
	if err != nil {
		return fmt.Errorf("volume: WriteBlock: locating virtual block %d of file %v: %w", vbn, f.Header.Fid, err)
	}
	if err := container.WriteBlock(lbn, data); err != nil {
		return fmt.Errorf("volume: WriteBlock: writing virtual block %d of file %v: %w", vbn, f.Header.Fid, err)
	}

	if vbn > f.maxWrittenVBN {
		f.maxWrittenVBN = vbn
	}

	// The in-memory high-water mark moves past the block too, so reading
	// it back through f (ReadBlock) returns what was just written rather
	// than the zeros ReadBlock gives a never-written block. Close records
	// the same value on disk; any earlier header rewrite through f
	// (Extend, UpdateHeader) records it, correctly, sooner.
	if f.Header.HighWaterMark < vbn+1 {
		f.Header.HighWaterMark = vbn + 1
	}
	return nil
}

// Close finalizes bookkeeping for a File armed for writing (via CreateFile
// or OpenForWrite): a single rewrite of its RecordAttributes.EndOfFileBlock/
// FirstFreeByte/HighWaterMark reflecting every block WriteBlock has written
// since it was opened (see WriteBlock's own doc comment for why this is
// deferred to Close rather than done on every WriteBlock call).
//
// The EndOfFileBlock/FirstFreeByte pair is always written using this
// package's whole-block convention (data ends exactly on a block boundary —
// the same one Directory.recordUsedBlocks uses): FirstFreeByte 0 and
// EndOfFileBlock one past the last block actually written, or both 0 if
// WriteBlock was never called at all. HighWaterMark only ever moves
// forward, never back, so Close can never make a block that already read
// as real data start reading as simulated-zero again.
//
// Close is a harmless no-op on a File that was never armed for writing (an
// ordinary File from Volume.OpenFID, never passed to OpenForWrite) and is
// idempotent — calling it again after it has already run is also a no-op —
// so callers can defer Close unconditionally without needing to know in
// advance whether a given File is writable.
//
// Close is exactly CloseWithFinalByte(0) — see that method's doc comment
// for the one case where a caller needs something other than this
// whole-block convention.
func (f *File) Close() error {
	return f.CloseWithFinalByte(0)
}

// CloseWithFinalByte is Close's counterpart for a caller that needs
// FirstFreeByte to land at a precise offset within the last block written,
// rather than Close's own whole-block convention of always recording data
// as ending exactly on a block boundary.
//
// WriteBlock only ever writes whole ondisk.BlockSize blocks — there's no
// way to give it a final block that's only partly real data. A caller that
// tracks its own exact byte length as it writes (package rms's Writer,
// docs/PHASE-02.md subtask 13, is the motivating case: individual records
// routinely end partway through a block) can still write that final block
// through WriteBlock, zero-padding it out to full size itself, and then
// call CloseWithFinalByte instead of Close to record the block's true,
// partial extent: finalByte is the offset, within the highest virtual
// block WriteBlock has been asked to write (see maxWrittenVBN), of the
// first byte past the file's real content — exactly what ends up in
// RecordAttributes.FirstFreeByte itself. finalByte 0 means "the file's
// data ends exactly on a block boundary", i.e. Close's own convention —
// which is why Close is defined as CloseWithFinalByte(0) rather than
// duplicating this method's body.
func (f *File) CloseWithFinalByte(finalByte uint16) error {
	if f.bm == nil || f.ib == nil {
		return nil
	}
	if finalByte >= ondisk.BlockSize {
		return fmt.Errorf("volume: closing file %v: finalByte %d is not a valid offset within a %d-byte block", f.Header.Fid, finalByte, ondisk.BlockSize)
	}

	container, ok := f.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: closing file %v: device is not open for write", f.Header.Fid)
	}

	h := f.Header
	h.RecordAttributes.EndOfFileBlock = 0
	h.RecordAttributes.FirstFreeByte = 0
	if f.maxWrittenVBN > 0 {
		h.RecordAttributes.EndOfFileBlock = f.maxWrittenVBN + 1
		if finalByte != 0 {
			// The highest block actually written isn't fully real data --
			// record it, and only it, as the last block of the file,
			// instead of the one past it that the whole-block convention
			// above assumed.
			h.RecordAttributes.EndOfFileBlock = f.maxWrittenVBN
			h.RecordAttributes.FirstFreeByte = finalByte
		}
		if h.HighWaterMark < f.maxWrittenVBN+1 {
			h.HighWaterMark = f.maxWrittenVBN + 1
		}
	}

	areas, err := existingAreas(h)
	if err != nil {
		return fmt.Errorf("volume: closing file %v: %w", f.Header.Fid, err)
	}
	decoded, err := writeHeader(f.Device, container, h.Fid.Number(), h, areas)
	if err != nil {
		return fmt.Errorf("volume: closing file %v: recording final size: %w", f.Header.Fid, err)
	}

	f.Header = decoded

	// A File other accessors are writing (access.go) stays armed for
	// them.
	if f.share.writers == 0 {
		f.bm, f.ib = nil, nil
	}

	return nil
}

// CreateFile creates a brand-new file named name in dir: resolves the next
// version number (Directory.NextVersion), allocates and writes a header for
// it (CreateHeader), inserts the corresponding directory entry
// (Directory.Insert), and returns the result already armed for writing
// (OpenForWrite) so a caller can go straight into WriteBlock calls followed
// by Close.
//
// bm and ib are the volume's storage- and index-file bitmap caches (see
// OpenBitmap/OpenIndexBitmap) — not part of this function's signature in
// docs/PHASE-02.md's original one-line sketch, but unavoidable in practice
// for the same reason subtasks 8 and 9 each already needed them: allocating
// a header slot, inserting a directory entry, and (if a later WriteBlock
// call extends the file) allocating data space all go through these same
// caches, and nothing else on Volume/Directory holds a reference to them.
//
// recAttr.VersionLimit is IGNORED — CreateFile computes the new version's
// own VersionLimit itself (resolveVersionLimit, below) per docs/PHASE-03.md's
// "Version-limit design": a brand-new name inherits dir's current default,
// while a later version of an already-existing name carries forward
// whatever limit its own immediately-previous version already had, rather
// than re-reading dir's default on every single create. Once the new
// version exists, enforceVersionLimit deletes however many of that name's
// oldest surviving versions are needed to bring the count back within the
// resolved limit (a no-op when the limit is 0, meaning "unlimited").
func (vol *Volume) CreateFile(dir *Directory, name string, recAttr ondisk.RecAttr, bm *Bitmap, ib *IndexBitmap) (*File, error) {
	return vol.CreateFileVersion(dir, name, 0, recAttr, bm, ib)
}

// CreateFileVersion is CreateFile with an explicit version number: the new
// file is entered in dir as name;version rather than as the next version.
// version 0 means the next version (exactly CreateFile). This is what a
// VMS program gets by creating "NAME.TYP;5": the version it asked for, as
// long as the directory doesn't already have it — if it does, nothing is
// created and the error wraps ErrExists (VMS's SS$_DUPFILENAME), checked
// before anything is allocated. Replacing the existing version instead
// ("superseding" it) is the caller's to do, by DeleteFile first.
//
// The new version's VersionLimit is resolved and enforced exactly as
// CreateFile's: see resolveVersionLimit and enforceVersionLimit.
func (vol *Volume) CreateFileVersion(dir *Directory, name string, version uint16, recAttr ondisk.RecAttr, bm *Bitmap, ib *IndexBitmap) (*File, error) {
	if version == 0 {
		next, err := dir.NextVersion(name)
		if err != nil {
			return nil, fmt.Errorf("volume: creating %s: %w", name, err)
		}
		version = next
	} else if _, err := dir.Lookup(name, version); err == nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, ErrExists)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	versionLimit, err := resolveVersionLimit(dir, name, version)
	if err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}
	recAttr.VersionLimit = versionLimit

	f, err := CreateHeader(dir.Device, ib, NewFileHeader{
		Name:             name,
		Version:          version,
		Directory:        dir.Header.Fid,
		RecordAttributes: recAttr,
	})
	if err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	if err := dir.Insert(name, version, f.Header.Fid, bm, ib); err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	if err := enforceVersionLimit(dir, name, versionLimit, bm, ib); err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	if err := f.OpenForWrite(bm, ib); err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	return f, nil
}

// resolveVersionLimit computes the VersionLimit the version-th version of
// name (about to be created in dir) should itself carry, per
// docs/PHASE-03.md's "Version-limit design": the first version of a name
// (no version of it exists yet) inherits dir's own current default
// (RecordAttributes.VersionLimit on the directory file's own header); any
// later version instead carries forward whatever VersionLimit the name's
// highest existing version already had, captured once at that earlier
// version's own creation rather than re-derived from dir on every call.
//
// For the usual case, a new file given the next version (dir.NextVersion:
// one more than the highest existing version, whatever gaps there are
// below it), the highest existing version is exactly version-1. For an
// explicit version (CreateFileVersion) it's whichever version is highest,
// above or below the new one; version itself only matters for the error
// text.
func resolveVersionLimit(dir *Directory, name string, version uint16) (uint16, error) {
	entry, err := dir.Lookup(name, 0)
	if errors.Is(err, ErrNotFound) {
		return dir.Header.RecordAttributes.VersionLimit, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolving version limit for %s;%d: %w", name, version, err)
	}
	previous, err := readFileHeaderViaIndex(dir.Device, dir.Device.IndexFile.Extents, entry.Fid)
	if err != nil {
		return 0, fmt.Errorf("resolving version limit from previous version %s;%d: %w", name, entry.Version, err)
	}
	return previous.RecordAttributes.VersionLimit, nil
}

// enforceVersionLimit deletes however many of name's oldest surviving
// versions in dir are needed to bring its count back within limit, after a
// new version has just been inserted — the create-time enforcement half of
// docs/PHASE-03.md's version-limit design (the other half, resolving what
// the new version's own limit should be, is resolveVersionLimit above).
//
// limit == 0 means "unlimited" (docs/PHASE-03.md's "Version-limit design")
// and is always a no-op, matching Phase 2's own behavior (before this
// field was ever wired up) with zero observable change for every existing
// caller that never sets a limit.
func enforceVersionLimit(dir *Directory, name string, limit uint16, bm *Bitmap, ib *IndexBitmap) error {
	if limit == 0 {
		return nil
	}

	entries, err := dir.List()
	if err != nil {
		return fmt.Errorf("enforcing version limit for %s: %w", name, err)
	}

	for _, v := range excessVersions(entries, name, limit) {
		if err := DeleteFile(dir, name, v, bm, ib); err != nil {
			return fmt.Errorf("enforcing version limit for %s: deleting excess version %d: %w", name, v, err)
		}
	}
	return nil
}

// excessVersions returns, from entries, the versions of name (case-
// insensitive match, matching every other name comparison in this package)
// that must be removed to bring its version count down to exactly keep —
// the oldest ones first, since "oldest" is always what both CreateFile's
// create-time enforcement (enforceVersionLimit, above) and PurgeVersions
// (delete.go) remove. Returns nil if name already has keep or fewer
// versions.
func excessVersions(entries []ondisk.DirEntry, name string, keep uint16) []uint16 {
	var versions []uint16
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			versions = append(versions, e.Version)
		}
	}
	if uint16(len(versions)) <= keep {
		return nil
	}

	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions[:len(versions)-int(keep)]
}
