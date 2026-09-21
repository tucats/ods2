package volume

import (
	"fmt"

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
	f.bm, f.ib = nil, nil
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
func (vol *Volume) CreateFile(dir *Directory, name string, recAttr ondisk.RecAttr, bm *Bitmap, ib *IndexBitmap) (*File, error) {
	version, err := dir.NextVersion(name)
	if err != nil {
		return nil, fmt.Errorf("volume: creating %s: %w", name, err)
	}

	f, err := CreateHeader(dir.Device, ib, NewFileHeader{
		Name:             name,
		Directory:        dir.Header.Fid,
		RecordAttributes: recAttr,
	})
	if err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	if err := dir.Insert(name, version, f.Header.Fid, bm, ib); err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	if err := f.OpenForWrite(bm, ib); err != nil {
		return nil, fmt.Errorf("volume: creating %s;%d: %w", name, version, err)
	}

	return f, nil
}
