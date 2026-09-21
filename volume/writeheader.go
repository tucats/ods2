package volume

import (
	"fmt"
	"time"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/vmstime"
)

// NewFileHeader bundles what CreateHeader needs to build a brand-new file
// header: everything about the file that isn't derived from the volume
// itself (Owner/FileProtection come from the home block -- see CreateHeader)
// or chosen by the allocator (the header slot and Fid).
type NewFileHeader struct {
	// Name is stored in the new header's IDENT area (see ondisk.Ident) --
	// this is the file's own record of its name, independent of whatever
	// directory entry (a separate on-disk structure, package volume's
	// Directory) points at it. It does NOT itself make the file visible in
	// any directory; that's a later subtask's job (docs/PHASE-02.md
	// subtask 9), which is expected to insert a directory entry pointing at
	// the Fid CreateHeader returns.
	Name string

	// Directory is the Fid of the file's parent directory, stored as the
	// new header's Backlink -- the reverse of the (directory -> file) link
	// a directory entry records, letting a file be traced back to where it
	// lives without a directory scan.
	Directory ondisk.Fid

	// Characteristics is a bitmask of the ondisk.Fch* constants, most
	// importantly ondisk.FchDirectory for a new directory rather than an
	// ordinary file.
	Characteristics uint32

	// RecordAttributes carries the new file's record-format fields (Format,
	// MaxRecordSize, VfcSize, and so on -- see ondisk.RecAttr). Its
	// whole-file bookkeeping fields (HighestBlock, EndOfFileBlock,
	// FirstFreeByte) are always reset to zero by CreateHeader regardless of
	// what's passed here: a header CreateHeader has just allocated has no
	// data and no space yet, by construction -- Extend (below) is what
	// grows HighestBlock as space is added, and a later subtask's file
	// write path is what advances EndOfFileBlock/FirstFreeByte as data is
	// actually written.
	RecordAttributes ondisk.RecAttr
}

// CreateHeader allocates a free header slot from ib (see
// IndexBitmap.FindFreeSlot) and writes a brand-new, empty (zero blocks
// allocated) primary FileHeader into it, returning the resulting File --
// Extents is empty, since nothing has been allocated to the file yet (see
// Extend for growing it).
//
// The new header's owner and default protection come from the volume's own
// home block (HomeBlock.VolumeOwner/FileProtection), not a hardcoded UIC --
// unlike the reference implementation's update_addhead(), which hardcodes
// UIC [1,4] regardless of the volume it's writing to (see
// docs/PHASE-02.md's design overview for why this project doesn't reproduce
// that).
//
// The new Fid's sequence number (Seq) follows the same convention the
// reference implementation uses, reproduced here because it's genuine
// on-disk semantics other code depends on (readFileHeaderViaIndex's stale-
// Fid check), not merely a reference artifact: one more than whatever
// sequence number the slot's previous occupant (if any) last held --
// FindFreeSlot has already confirmed the slot "looks unused" (zero checksum,
// zero file number), but its Seq field can still be nonzero if the slot once
// held a since-deleted file, and VMS keeps incrementing from there so a Fid
// captured before that deletion is reliably detected as stale rather than
// coincidentally matching a new file that reused the same slot. A wrap to 0
// is bumped to 1, since this project (matching IndexBitmap.FindFreeSlot's
// own check) treats a stored Seq of 0 as meaning "this slot has never held a
// file."
func CreateHeader(dev *Device, ib *IndexBitmap, opts NewFileHeader) (*File, error) {
	container, ok := dev.Container.(diskimage.WritableContainer)
	if !ok {
		return nil, fmt.Errorf("volume: creating file header: device is not open for write")
	}

	fileNumber, err := ib.FindFreeSlot()
	if err != nil {
		return nil, fmt.Errorf("volume: creating file header: %w", err)
	}

	fid, err := nextFileFid(dev, fileNumber)
	if err != nil {
		return nil, fmt.Errorf("volume: creating file header: %w", err)
	}

	recAttr := opts.RecordAttributes
	recAttr.HighestBlock = 0
	recAttr.EndOfFileBlock = 0
	recAttr.FirstFreeByte = 0

	now := vmstime.FromTime(time.Now())
	h := ondisk.FileHeader{
		StructureLevel:      ondisk.FileHeaderStructureLevel,
		Fid:                 fid,
		RecordAttributes:    recAttr,
		FileCharacteristics: opts.Characteristics,
		Owner:               dev.Home.VolumeOwner,
		FileProtection:      dev.Home.FileProtection,
		Backlink:            opts.Directory,
	}
	areas := ondisk.FileHeaderAreas{
		Ident: &ondisk.Ident{
			Filename:     opts.Name,
			Revision:     1,
			CreationDate: now,
			RevisionDate: now,
		},
	}

	decoded, err := writeHeader(dev, container, fid.Number(), h, areas)
	if err != nil {
		return nil, fmt.Errorf("volume: creating file header: %w", err)
	}

	if err := ib.MarkAllocated(fileNumber); err != nil {
		return nil, fmt.Errorf("volume: creating file header: %w", err)
	}

	return &File{Device: dev, Header: decoded}, nil
}

// nextFileFid builds the Fid a newly-allocated header slot (file number
// fileNumber, already confirmed free by IndexBitmap.FindFreeSlot) should be
// given -- see CreateHeader's doc comment for the sequence-number
// convention this reproduces.
func nextFileFid(dev *Device, fileNumber uint32) (ondisk.Fid, error) {
	vbn := fileHeaderVBN(dev.Home, fileNumber)
	buf := make([]byte, ondisk.BlockSize)
	if err := dev.IndexFile.ReadBlock(vbn, buf); err != nil {
		return ondisk.Fid{}, fmt.Errorf("reading previous contents of header slot for file %d: %w", fileNumber, err)
	}
	// The slot's previous occupant (if any) is decoded purely to read its
	// now-superseded Seq -- FindFreeSlot already confirmed the slot "looks
	// unused" (checksum and file number both zero), so a checksum-
	// validation error from a genuinely blank (all-zero) slot is expected
	// here and deliberately ignored.
	previous, _ := ondisk.DecodeFileHeader(buf)

	seq := previous.Fid.Seq + 1
	if seq == 0 {
		seq = 1
	}

	return ondisk.Fid{
		Num: uint16(fileNumber),
		Nmx: uint8(fileNumber >> 16),
		Seq: seq,
		// Rvn 0: every file this project creates lives on the same device
		// its header does, matching the reference implementation's own
		// convention for a single-device volume (update_addhead() only
		// ever stores a nonzero Rvn for a device other than the volume
		// set's first member) -- Phase 2 doesn't support writing volume
		// sets at all (see docs/PHASE-02.md's non-goals), so this is never
		// anything but 0 today.
		Rvn: 0,
	}, nil
}

// writeHeader encodes h+areas and writes the result to the on-disk header
// slot for fileNumber on dev, returning the freshly-decoded header (not h
// itself) so the caller gets back exactly what a future lookup (OpenFID,
// readFileHeaderViaIndex) would see -- including the IdentOffset/MapOffset/
// EndOffset/MapWordsInUse fields EncodeFileHeader computed, which h never
// carried in the first place (see FileHeaderAreas' own doc comment).
func writeHeader(dev *Device, container diskimage.WritableContainer, fileNumber uint32, h ondisk.FileHeader, areas ondisk.FileHeaderAreas) (ondisk.FileHeader, error) {
	buf, err := ondisk.EncodeFileHeader(h, areas)
	if err != nil {
		return ondisk.FileHeader{}, fmt.Errorf("encoding header for file %d: %w", fileNumber, err)
	}
	return writeHeaderBytes(dev, container, fileNumber, buf)
}

// writeHeaderBytes writes an already-encoded 512-byte header block to
// fileNumber's on-disk slot and returns it decoded back, the low-level step
// writeHeader and appendExtent both build on.
func writeHeaderBytes(dev *Device, container diskimage.WritableContainer, fileNumber uint32, buf []byte) (ondisk.FileHeader, error) {
	vbn := fileHeaderVBN(dev.Home, fileNumber)
	lbn, err := resolveExtentLBN(dev.IndexFile.Extents, vbn)
	if err != nil {
		return ondisk.FileHeader{}, fmt.Errorf("locating header slot for file %d (VBN %d): %w", fileNumber, vbn, err)
	}
	if err := container.WriteBlock(lbn, buf); err != nil {
		return ondisk.FileHeader{}, fmt.Errorf("writing header slot for file %d (LBN %d): %w", fileNumber, lbn, err)
	}

	decoded, err := ondisk.DecodeFileHeader(buf)
	if err != nil {
		// Unreachable: EncodeFileHeader always produces a checksum that
		// validates against its own output.
		return ondisk.FileHeader{}, fmt.Errorf("decoding just-written header for file %d: %w", fileNumber, err)
	}
	return decoded, nil
}

// Extend grows f by allocating additionalBlocks more blocks of space
// (rounded up to whole clusters -- see Bitmap's own doc comment on why
// space is always allocated in clusters, not individual blocks) from bm,
// and links that new space into f's retrieval-pointer map.
//
// New space is appended to whichever header segment currently has room in
// its own retrieval-pointer map -- normally the primary header, or the last
// extension segment already chained onto it -- found by following
// ExtensionFid to the end of the chain, the same walk buildFile performs
// when opening a file. When that segment's map has run out of room, Extend
// allocates a brand new extension segment (a fresh header slot from ib,
// via the same path CreateHeader uses) and links it on by setting the old
// tail's ExtensionFid, mirroring how the reference implementation's
// update_extend() grows a file whose header has run out of map space --
// except that here it's always a normal, handled case, never the
// directory-splitting exit(0) the reference reserves for a DIFFERENT
// out-of-room case (see docs/PHASE-02.md's "what we're deliberately not
// porting" table).
//
// The additionalBlocks new blocks are allocated but never written: they
// read back as zero via File.ReadBlock, because Extend never moves
// HighWaterMark forward -- every VBN at or beyond it (which already covered
// every block being added here, since HighWaterMark can never exceed a
// file's previous HighestBlock) continues to read as unwritten until an
// actual write (a later subtask) advances it. RecordAttributes.HighestBlock
// on the primary header IS updated, to the new, larger total allocation.
//
// A failure partway through (a disk write error, or running out of free
// space after already placing some new extents) can leave some newly
// allocated clusters marked used in bm without any header actually
// referencing them yet -- a leak, not a correctness hazard (nothing else
// will hand out the same space while it's still marked allocated), and
// exactly the kind of inconsistency ANALYZE/DISK (a later subtask) is
// designed to detect and, with /REPAIR, reclaim.
func Extend(f *File, bm *Bitmap, ib *IndexBitmap, additionalBlocks uint32) error {
	dev := f.Device
	container, ok := dev.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: extending file %v: device is not open for write", f.Header.Fid)
	}
	if additionalBlocks == 0 {
		return fmt.Errorf("volume: extending file %v: additionalBlocks must be at least 1, got 0", f.Header.Fid)
	}

	newExtents, err := allocateExtents(bm, additionalBlocks)
	if err != nil {
		return fmt.Errorf("volume: extending file %v: %w", f.Header.Fid, err)
	}

	tail, err := tailHeader(dev, f.Header)
	if err != nil {
		return fmt.Errorf("volume: extending file %v: %w", f.Header.Fid, err)
	}

	var grown uint32
	for _, e := range newExtents {
		updated, fits, err := appendExtent(dev, container, tail, e)
		if err != nil {
			return fmt.Errorf("volume: extending file %v: %w", f.Header.Fid, err)
		}

		if !fits {
			var relinkedTail ondisk.FileHeader
			relinkedTail, updated, err = linkNewExtensionSegment(dev, container, ib, tail, e)
			if err != nil {
				return fmt.Errorf("volume: extending file %v: %w", f.Header.Fid, err)
			}
			// linkNewExtensionSegment just rewrote tail's own on-disk slot
			// (to point ExtensionFid at the new segment) independently of
			// the "tail == primary?" bookkeeping below -- if tail WAS the
			// primary header, f.Header's in-memory copy needs the same
			// update, or the final RecordAttributes rewrite below would
			// re-encode f.Header from its now-stale (pre-relink) copy and
			// silently erase the ExtensionFid this just wrote.
			if relinkedTail.Fid.Number() == f.Header.Fid.Number() {
				f.Header = relinkedTail
			}
		}

		tail = updated
		if tail.Fid.Number() == f.Header.Fid.Number() {
			f.Header = tail
		}

		grown += e.Count
		f.Extents = append(f.Extents, ExtentLocation{Extent: e, Rvn: dev.Rvn})
	}

	// HighestBlock lives on the primary header, and needs recording even
	// when every new extent above landed in an extension segment (in which
	// case the primary header's own map/ident content is unchanged, but
	// its RecordAttributes still needs rewriting).
	f.Header.RecordAttributes.HighestBlock += grown
	areas, err := existingAreas(f.Header)
	if err != nil {
		return fmt.Errorf("volume: extending file %v: %w", f.Header.Fid, err)
	}
	decoded, err := writeHeader(dev, container, f.Header.Fid.Number(), f.Header, areas)
	if err != nil {
		return fmt.Errorf("volume: extending file %v: recording new HighestBlock: %w", f.Header.Fid, err)
	}
	f.Header = decoded

	return nil
}

// tailHeader walks f's header-extension chain (following ExtensionFid, the
// same way buildFile does when opening a file) and returns its LAST
// segment -- the one new retrieval-pointer entries should be tried against
// first, since VMS always appends new space to the end of the chain rather
// than searching earlier segments for room.
func tailHeader(dev *Device, primary ondisk.FileHeader) (ondisk.FileHeader, error) {
	header := primary
	for !header.ExtensionFid.IsZero() {
		next, err := readFileHeaderViaIndex(dev, dev.IndexFile.Extents, header.ExtensionFid)
		if err != nil {
			return ondisk.FileHeader{}, fmt.Errorf("following header extension chain for file %v: %w", primary.Fid, err)
		}
		header = next
	}
	return header, nil
}

// headerIdent decodes header's IDENT area, or returns (nil, nil) if it has
// none -- the same "IdentOffset == MapOffset means a zero-length IDENT
// area" convention EncodeFileHeader itself uses (see FileHeaderAreas.Ident's
// doc comment), needed here to correctly round-trip a header (such as an
// extension segment, which the reference implementation always writes with
// an empty IDENT area) that was built that way.
func headerIdent(header ondisk.FileHeader) (*ondisk.Ident, error) {
	if header.IdentOffset == header.MapOffset {
		return nil, nil
	}
	id, err := header.Ident()
	if err != nil {
		return nil, fmt.Errorf("decoding existing IDENT area: %w", err)
	}
	return &id, nil
}

// existingAreas reconstructs the FileHeaderAreas a decoded header's current
// IDENT and retrieval-pointer content came from, suitable for re-encoding
// via EncodeFileHeader with some field of the header itself changed (a new
// ExtensionFid, a larger HighestBlock, ...) while leaving its variable
// content exactly as it already was. header must have come from
// ondisk.DecodeFileHeader (its raw bytes are what Ident()/
// RetrievalPointers() read from) -- every header this function is called
// with does, since it's only ever used on headers this package has just
// read or just written back.
func existingAreas(header ondisk.FileHeader) (ondisk.FileHeaderAreas, error) {
	ident, err := headerIdent(header)
	if err != nil {
		return ondisk.FileHeaderAreas{}, err
	}
	extents, err := header.RetrievalPointers()
	if err != nil {
		return ondisk.FileHeaderAreas{}, fmt.Errorf("decoding existing retrieval pointers: %w", err)
	}
	mapBytes, err := ondisk.EncodeRetrievalPointers(extents)
	if err != nil {
		return ondisk.FileHeaderAreas{}, fmt.Errorf("re-encoding existing retrieval pointers: %w", err)
	}
	return ondisk.FileHeaderAreas{Ident: ident, MapBytes: mapBytes}, nil
}

// appendExtent tries to add extent to header's own retrieval-pointer map
// and write the result back to header's on-disk slot. fits is false (with
// no error) when the map area doesn't have room for one more entry -- the
// same "is the map area nearly full" case the reference implementation's
// update_extend() checks (update.c:412-413) before falling back to a new
// extension segment; Extend treats that as an ordinary, expected outcome,
// not a failure.
func appendExtent(dev *Device, container diskimage.WritableContainer, header ondisk.FileHeader, extent ondisk.Extent) (result ondisk.FileHeader, fits bool, err error) {
	areas, err := existingAreas(header)
	if err != nil {
		return ondisk.FileHeader{}, false, err
	}
	extents, err := header.RetrievalPointers()
	if err != nil {
		return ondisk.FileHeader{}, false, fmt.Errorf("decoding existing retrieval pointers: %w", err)
	}
	mapBytes, err := ondisk.EncodeRetrievalPointers(append(extents, extent))
	if err != nil {
		return ondisk.FileHeader{}, false, fmt.Errorf("encoding retrieval pointers: %w", err)
	}
	areas.MapBytes = mapBytes

	buf, err := ondisk.EncodeFileHeader(header, areas)
	if err != nil {
		// The only way EncodeFileHeader fails against content this
		// function itself assembled is the combined IDENT+map+ACL area
		// overflowing the header's 402 available bytes -- i.e. no room
		// left in THIS segment, not a genuine error.
		return ondisk.FileHeader{}, false, nil
	}

	decoded, err := writeHeaderBytes(dev, container, header.Fid.Number(), buf)
	if err != nil {
		return ondisk.FileHeader{}, false, err
	}
	return decoded, true, nil
}

// linkNewExtensionSegment allocates a fresh header slot from ib (the same
// path CreateHeader uses), writes a new extension-header segment into it
// containing exactly extent, and links it onto the end of the chain by
// rewriting tail's on-disk ExtensionFid to point at it. It returns both the
// relinked tail (decoded back from what was just written, since the caller
// may need to keep its own copy of that same segment in sync -- see
// Extend) and the new segment's decoded header.
//
// The new segment's Backlink is set to tail's own Fid, not the file's
// parent directory -- reproducing the reference implementation's
// update_extend()/update_addhead() convention of chaining each extension
// segment's backlink to the segment it extends (primary or a prior
// extension), rather than to the directory a primary header's Backlink
// points at. This is real on-disk semantics worth keeping intact, not a
// reference artifact: it lets a segment be traced back to what it extends
// without needing to trust the FORWARD ExtensionFid chain (useful to a
// future ANALYZE/DISK-style consistency check), symmetric with how a
// primary header's own Backlink lets it be traced back to its directory.
func linkNewExtensionSegment(dev *Device, container diskimage.WritableContainer, ib *IndexBitmap, tail ondisk.FileHeader, extent ondisk.Extent) (relinkedTail, newSegment ondisk.FileHeader, err error) {
	segFileNumber, err := ib.FindFreeSlot()
	if err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, fmt.Errorf("allocating extension header segment: %w", err)
	}
	segFid, err := nextFileFid(dev, segFileNumber)
	if err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, err
	}

	// Relink the current tail onto the new segment BEFORE writing the new
	// segment itself, so a failure partway through never leaves a segment
	// on disk that nothing points at yet (the reverse order would risk
	// exactly that).
	tailAreas, err := existingAreas(tail)
	if err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, err
	}
	previousFid := tail.Fid
	tail.ExtensionFid = segFid
	relinkedTail, err = writeHeader(dev, container, tail.Fid.Number(), tail, tailAreas)
	if err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, fmt.Errorf("linking new extension segment onto file %v: %w", previousFid, err)
	}

	if err := ib.MarkAllocated(segFileNumber); err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, fmt.Errorf("marking new extension header slot allocated: %w", err)
	}

	mapBytes, err := ondisk.EncodeRetrievalPointers([]ondisk.Extent{extent})
	if err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, fmt.Errorf("encoding new extension segment's retrieval pointers: %w", err)
	}
	segHeader := ondisk.FileHeader{
		SegmentNumber:  tail.SegmentNumber + 1,
		StructureLevel: ondisk.FileHeaderStructureLevel,
		Fid:            segFid,
		Owner:          dev.Home.VolumeOwner,
		FileProtection: dev.Home.FileProtection,
		Backlink:       previousFid,
	}
	newSegment, err = writeHeader(dev, container, segFileNumber, segHeader, ondisk.FileHeaderAreas{MapBytes: mapBytes})
	if err != nil {
		return ondisk.FileHeader{}, ondisk.FileHeader{}, fmt.Errorf("writing new extension segment: %w", err)
	}
	return relinkedTail, newSegment, nil
}

// allocateExtents allocates enough space from bm to cover at least blocks
// virtual blocks, in whatever number of Extents that takes -- more than one
// when the volume is fragmented enough that no single free run is long
// enough by itself, per docs/PHASE-02.md subtask 6's own note that a caller
// needing more space than the single largest free run offers is expected to
// call FindFree again for a smaller amount and accept fragmentation, rather
// than requiring Bitmap to solve multi-extent allocation itself. Each
// returned Extent has already been marked allocated in bm (in memory only
// -- see Bitmap.Flush for when that reaches disk).
//
// Finding the largest run that still fits the remaining request is a
// simple linear search (try the full remaining amount, then one less, and
// so on, until FindFree succeeds) rather than anything cleverer -- matching
// this project's general preference (see docs/PHASE-02.md's "what we're
// deliberately not porting" table) for the simplest design that gets the
// job done correctly, over a more elaborate search this project's actual
// (small, synthetic, or modestly-sized real) volumes have no real need for.
func allocateExtents(bm *Bitmap, blocks uint32) (extents []ondisk.Extent, err error) {
	if blocks == 0 {
		return nil, fmt.Errorf("volume: allocateExtents requires at least one block, got 0")
	}
	if bm.clusterSize == 0 {
		return nil, fmt.Errorf("volume: bitmap has a zero cluster size")
	}

	defer func() {
		if err != nil {
			// Roll back whatever this call itself allocated, so a failed
			// Extend doesn't leave bm's in-memory state dirty with space
			// the caller never ended up using.
			for _, e := range extents {
				_ = bm.MarkFree(e)
			}
		}
	}()

	clusters := (blocks + bm.clusterSize - 1) / bm.clusterSize
	remaining := clusters
	for remaining > 0 {
		request := remaining
		var extent ondisk.Extent
		var findErr error
		for request > 0 {
			extent, findErr = bm.FindFree(request)
			if findErr == nil {
				break
			}
			request--
		}
		if request == 0 {
			return extents, fmt.Errorf("not enough free space: %d cluster(s) still needed, none available", remaining)
		}

		if err := bm.MarkAllocated(extent); err != nil {
			return extents, fmt.Errorf("marking newly-found extent allocated: %w", err)
		}
		extents = append(extents, extent)
		remaining -= extent.Count / bm.clusterSize
	}

	return extents, nil
}
