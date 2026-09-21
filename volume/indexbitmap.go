package volume

import (
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// IndexBitmap is an in-memory cache of one device's index-file header-slot
// bitmap — the free/allocated map of file-header slots within INDEXF.SYS
// itself, as opposed to Bitmap (BITMAP.SYS), which tracks free *data*
// space. See docs/PHASE-02.md's "Two bitmaps, not one" for why a volume
// has exactly two of these, at different locations, in different units.
//
// Background for readers new to this on-disk format: every file on an
// ODS-2 volume needs a "header slot" — a 512-byte record in INDEXF.SYS
// describing it (see ondisk.FileHeader) — before it can have any data at
// all. A volume only has room for HomeBlock.MaxFiles such slots, so
// creating a file means first finding an unused slot, the same way
// writing file data means first finding free space in BITMAP.SYS.
//
// Unlike BITMAP.SYS, this bitmap isn't a separate file: it's a region
// *within* INDEXF.SYS itself, immediately before the header area
// (HomeBlock.IndexBitmapVBN/IndexBitmapSize give its location and size, as
// virtual blocks of INDEXF.SYS — the same fields readFileHeaderViaIndex
// already uses to skip past it when locating an ordinary file's header).
// One bit per potential file number, up to HomeBlock.MaxFiles: bit N
// (0-based) records whether file number N+1's header slot is in use.
//
// Critically, this bitmap's free/allocated polarity is the OPPOSITE of
// BITMAP.SYS's: a SET bit here means the slot is IN USE; a clear bit
// means free. This was confirmed empirically against
// testdata/rq0-ra92.dsk (a real, actively-used OpenVMS volume, see
// docs/PHASE-02.md's "Testing strategy"): comparing every one of its
// 38900 header slots' actual on-disk contents (a slot "looks unused" when
// its stored checksum and file number are both zero) against this
// region's bits found zero mismatches under this polarity, and thousands
// under the reverse. This matches the reference implementation's own
// headmap_clear() (update.c:230, `bitmap[...] &= ~(1<<bit)` to mark a
// slot free again) and update_findhead()'s search (update.c:264, a bit is
// a free candidate when `(work_val & (1<<bit_no)) == 0`) — unlike most of
// that function (see docs/PHASE-02.md's "what we're deliberately not
// porting" table), this particular bit's meaning is real on-disk data,
// not an implementation artifact, so it has to be reproduced exactly as
// VMS itself writes it, not "corrected" to match BITMAP.SYS's polarity
// for consistency's own sake.
//
// ondisk.BitmapTest/BitmapSet/BitmapClear are still reused here — they're
// pure bit-position mechanics (test/set/clear bit N of a buffer),
// agnostic to which polarity means what — only the interpretation at each
// call site below is reversed from Bitmap's: BitmapSet marks a slot IN
// USE (not free), and BitmapClear marks it free (not allocated).
//
// Like Bitmap, IndexBitmap is loaded once and mutated purely in memory —
// nothing is written back to the device until Flush is called (see
// docs/PHASE-02.md's "Caching strategy").
type IndexBitmap struct {
	dev       *Device
	container diskimage.WritableContainer

	// totalSlots is HomeBlock.MaxFiles: FindFreeSlot/MarkAllocated/MarkFree
	// never consider a slot at or beyond this, even though bits (below) is
	// sized to a whole number of 512-byte blocks and so may have a few
	// extra, meaningless trailing bits as padding.
	totalSlots uint32

	// reservedSlots is HomeBlock.ReservedFiles: FindFreeSlot always skips
	// this many slots at the start (file numbers 1..reservedSlots),
	// reserved for the volume's own bookkeeping files. Always read from
	// the home block, never hardcoded — see the type doc comment on why
	// this project avoids the reference implementation's hardcoded
	// headmap_clear() equivalent (`if (head_no < 10) return 0`).
	reservedSlots uint32

	// bits holds every index-bitmap block read from INDEXF.SYS,
	// concatenated in VBN order starting at HomeBlock.IndexBitmapVBN.
	// Mutated in place by MarkAllocated/MarkFree.
	bits []byte

	// dirty is set by MarkAllocated/MarkFree and cleared by Flush — same
	// whole-bitmap dirty tracking as Bitmap, for the same reason (see its
	// doc comment).
	dirty bool
}

// OpenIndexBitmap reads dev's index-file header-slot bitmap into memory,
// ready for allocation. dev must already have been mounted (so
// dev.IndexFile is populated — see Mount/bootstrapIndexFile) and opened
// for write: dev's underlying container must implement
// diskimage.WritableContainer, or OpenIndexBitmap fails immediately
// rather than letting a later Flush fail confusingly.
func OpenIndexBitmap(dev *Device) (*IndexBitmap, error) {
	container, ok := dev.Container.(diskimage.WritableContainer)
	if !ok {
		return nil, fmt.Errorf("volume: opening index-file bitmap: device is not open for write")
	}

	if dev.Home.MaxFiles == 0 {
		return nil, fmt.Errorf("volume: opening index-file bitmap: home block MaxFiles is zero")
	}

	size := uint32(dev.Home.IndexBitmapSize)
	bitCapacity := size * ondisk.BlockSize * 8
	if dev.Home.MaxFiles > bitCapacity {
		return nil, fmt.Errorf(
			"volume: opening index-file bitmap: MaxFiles (%d) exceeds the index bitmap region's capacity (%d bits)",
			dev.Home.MaxFiles, bitCapacity)
	}

	bits := make([]byte, size*ondisk.BlockSize)
	buf := make([]byte, ondisk.BlockSize)
	for i := uint32(0); i < size; i++ {
		vbn := uint32(dev.Home.IndexBitmapVBN) + i
		if err := dev.IndexFile.ReadBlock(vbn, buf); err != nil {
			return nil, fmt.Errorf("volume: reading index-file bitmap block (VBN %d): %w", vbn, err)
		}
		copy(bits[i*ondisk.BlockSize:], buf)
	}

	return &IndexBitmap{
		dev:           dev,
		container:     container,
		totalSlots:    dev.Home.MaxFiles,
		reservedSlots: uint32(dev.Home.ReservedFiles),
		bits:          bits,
	}, nil
}

// headerVBN returns the virtual block number, within INDEXF.SYS, of the
// on-disk header slot for the given (1-based) file number — the same
// arithmetic readFileHeaderViaIndex uses to locate any file's header.
func (ib *IndexBitmap) headerVBN(fileNumber uint32) uint32 {
	return fileNumber - 1 + uint32(ib.dev.Home.IndexBitmapVBN) + uint32(ib.dev.Home.IndexBitmapSize)
}

// FindFreeSlot returns the file number of one free header slot, skipping
// the volume's reserved slots (HomeBlock.ReservedFiles — always via that
// field, never a hardcoded constant), or an error if none are free.
//
// Matching the reference implementation's own extra safety check in
// update_findhead() (a correct, worth-keeping check, unlike most of that
// function — see the type doc comment and docs/PHASE-02.md's "what we're
// deliberately not porting" table): before trusting a clear bit, this
// reads that slot's actual header block and confirms it really looks
// unused (zero checksum, zero file number). A mismatch means the bitmap
// and the volume's actual header slots have diverged — exactly the kind
// of corruption ANALYZE/DISK (a later subtask) is designed to catch at a
// whole-volume level — so FindFreeSlot fails loudly here rather than
// silently allocating over what might be live data.
func (ib *IndexBitmap) FindFreeSlot() (uint32, error) {
	buf := make([]byte, ondisk.BlockSize)
	for slot := ib.reservedSlots; slot < ib.totalSlots; slot++ {
		if ondisk.BitmapTest(ib.bits, slot) {
			continue // set bit: slot is in use -- see the type doc comment on polarity
		}

		fileNumber := slot + 1
		vbn := ib.headerVBN(fileNumber)
		if err := ib.dev.IndexFile.ReadBlock(vbn, buf); err != nil {
			return 0, fmt.Errorf("volume: FindFreeSlot: reading header slot for file %d (VBN %d): %w", fileNumber, vbn, err)
		}

		header, _ := ondisk.DecodeFileHeader(buf)
		if header.Checksum != 0 || header.Fid.Number() != 0 {
			return 0, fmt.Errorf(
				"volume: FindFreeSlot: index-file bitmap inconsistency: slot for file %d is marked free but its header does not look unused (checksum %#04x, file number %d)",
				fileNumber, header.Checksum, header.Fid.Number())
		}

		return fileNumber, nil
	}

	return 0, fmt.Errorf(
		"volume: FindFreeSlot: no free header slot found (%d total, %d reserved)",
		ib.totalSlots, ib.reservedSlots)
}

// slotRange validates fileNumber (1-based) and returns its 0-based bit
// index within bits.
func (ib *IndexBitmap) slotRange(fileNumber uint32) (uint32, error) {
	if fileNumber == 0 {
		return 0, fmt.Errorf("file numbers are 1-based; 0 is not a valid file number")
	}
	slot := fileNumber - 1
	if slot >= ib.totalSlots {
		return 0, fmt.Errorf("file number %d is beyond the volume's %d header slot(s)", fileNumber, ib.totalSlots)
	}
	return slot, nil
}

// MarkAllocated marks fileNumber's header slot as in use (in memory only
// — see Flush) and marks the bitmap dirty.
func (ib *IndexBitmap) MarkAllocated(fileNumber uint32) error {
	slot, err := ib.slotRange(fileNumber)
	if err != nil {
		return fmt.Errorf("volume: MarkAllocated: %w", err)
	}

	ondisk.BitmapSet(ib.bits, slot) // set bit: in use -- see the type doc comment on polarity
	ib.dirty = true
	return nil
}

// MarkFree marks fileNumber's header slot as free (in memory only — see
// Flush) and marks the bitmap dirty.
func (ib *IndexBitmap) MarkFree(fileNumber uint32) error {
	slot, err := ib.slotRange(fileNumber)
	if err != nil {
		return fmt.Errorf("volume: MarkFree: %w", err)
	}

	ondisk.BitmapClear(ib.bits, slot) // clear bit: free -- see the type doc comment on polarity
	ib.dirty = true
	return nil
}

// Flush writes every in-memory index-bitmap block back to INDEXF.SYS if
// the bitmap has been mutated since the last Flush (or since
// OpenIndexBitmap, if Flush has never been called), and clears the dirty
// flag. Calling Flush on a clean bitmap is a cheap no-op.
func (ib *IndexBitmap) Flush() error {
	if !ib.dirty {
		return nil
	}

	blocks := uint32(len(ib.bits)) / ondisk.BlockSize
	buf := make([]byte, ondisk.BlockSize)
	for i := uint32(0); i < blocks; i++ {
		vbn := uint32(ib.dev.Home.IndexBitmapVBN) + i
		lbn, err := resolveExtentLBN(ib.dev.IndexFile.Extents, vbn)
		if err != nil {
			return fmt.Errorf("volume: flushing index-file bitmap: locating VBN %d: %w", vbn, err)
		}

		copy(buf, ib.bits[i*ondisk.BlockSize:(i+1)*ondisk.BlockSize])
		if err := ib.container.WriteBlock(lbn, buf); err != nil {
			return fmt.Errorf("volume: flushing index-file bitmap: writing LBN %d: %w", lbn, err)
		}
	}

	ib.dirty = false
	return nil
}
