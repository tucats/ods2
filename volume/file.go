package volume

import (
	"fmt"

	"github.com/tucats/ods2/ondisk"
)

// ExtentLocation is one ondisk.Extent together with the relative volume
// number (Rvn) of the device it lives on. In an ordinary, single-disk
// volume every extent naturally lives on that one disk; in a volume set,
// different files (or, in principle, different parts of the same file)
// can live on different member disks, which is why this project tracks
// the owning device per extent rather than once per file.
type ExtentLocation struct {
	ondisk.Extent
	Rvn uint8
}

// File is an open ODS-2 file: its primary (segment 0) header, plus the
// fully-resolved, in-order list of Extents describing where its data
// actually lives. Extents is assembled by buildFile from the primary
// header's own retrieval pointers and, if the header indicates there is
// more data than fits in one header's retrieval-pointer map (via
// Header.ExtensionFid), by following the chain of additional "extension
// header" segments and appending each one's extents in turn.
type File struct {
	Device  *Device
	Header  ondisk.FileHeader
	Extents []ExtentLocation
}

// Blocks reports the file's length in virtual blocks (VBNs), as recorded
// in its header — this is the authoritative size, and does not need to
// equal the sum of Extents' Counts (a file's map only ever describes
// blocks it has actually been allocated; nothing about that value is
// double-checked against the header's own record of its size).
func (f *File) Blocks() uint32 {
	return f.Header.RecordAttributes.HighestBlock
}

// isUnwritten reports whether virtual block vbn is allocated to f but has
// never actually been written — see Header.HighWaterMark's own doc
// comment for the guarantee this relies on. Not every file header records
// a high-water mark (older/smaller headers omit it, indicated by
// Header.IdentOffset <= 39); when it's absent, every allocated block is
// assumed to hold real data, so isUnwritten always reports false.
//
// This is shared by ReadBlock (which must return zeroed data for such a
// block rather than exposing whatever leftover bytes physically occupy
// it) and Directory.List (which must not attempt to decode such a block
// as directory records at all — its guaranteed-zero content isn't a
// legitimately empty directory block, just unused trailing allocation VMS
// left in place for future growth).
func (f *File) isUnwritten(vbn uint32) bool {
	hasHighWaterMark := f.Header.IdentOffset > 39
	return hasHighWaterMark && vbn >= f.Header.HighWaterMark
}

// ReadBlock reads virtual block vbn of the file's data into buf, which
// must be at least ondisk.BlockSize bytes long.
//
// Like VMS's own VBN numbering, vbn is 1-based: VBN 1 is a file's first
// block. This matches the numbering built into the on-disk format itself
// (retrieval pointers describe contiguous ranges of VBNs starting from 1,
// and fields like Header.HighWaterMark are themselves VBNs in this same
// scheme), so this package uses 1-based VBNs throughout rather than
// silently converting to a 0-based convention and risking an off-by-one
// mismatch against the format's own arithmetic.
//
// A block at or beyond the file's high-water mark has been allocated but
// never actually written — it may contain leftover data from a
// previously-deleted file that once occupied the same physical space.
// ReadBlock returns an all-zero block for such a VBN instead of exposing
// that leftover data, matching the guarantee VMS itself makes to readers.
func (f *File) ReadBlock(vbn uint32, buf []byte) error {
	if len(buf) < ondisk.BlockSize {
		return fmt.Errorf("volume: ReadBlock buffer too small: need %d bytes, got %d", ondisk.BlockSize, len(buf))
	}

	if f.isUnwritten(vbn) {
		clear(buf[:ondisk.BlockSize])
		return nil
	}

	data, err := readExtents(f.Device, f.Extents, vbn)
	if err != nil {
		return fmt.Errorf("volume: reading virtual block %d of file %v: %w", vbn, f.Header.Fid, err)
	}
	copy(buf, data)
	return nil
}

// readExtents locates and reads virtual block vbn (1-based — see
// File.ReadBlock) from the given ordered list of Extents, each of which
// covers a contiguous run of virtual blocks immediately following the one
// before it.
func readExtents(dev *Device, extents []ExtentLocation, vbn uint32) ([]byte, error) {
	if vbn == 0 {
		return nil, fmt.Errorf("virtual block numbers are 1-based; 0 is not a valid VBN")
	}

	remaining := vbn
	for _, e := range extents {
		if remaining <= e.Count {
			lbn := e.StartLBN + (remaining - 1)
			buf := make([]byte, ondisk.BlockSize)
			if err := dev.Container.ReadBlock(lbn, buf); err != nil {
				return nil, fmt.Errorf("reading LBN %d: %w", lbn, err)
			}
			return buf, nil
		}
		remaining -= e.Count
	}

	return nil, fmt.Errorf("virtual block %d is beyond the end of the file", vbn)
}

// buildFile resolves a file's complete Extents list starting from its
// already-decoded primary header, following Header.ExtensionFid across as
// many additional header segments as needed.
//
// This same function bootstraps INDEXF.SYS's own File (see
// Volume.mountDevice): each additional extension segment's header is just
// another block within INDEXF.SYS, located and read through
// readFileHeaderViaIndex using whatever extents have been resolved SO
// FAR — which is exactly enough information to find the next segment, by
// construction of the on-disk format. This mirrors how the reference
// implementation resolves a file's map incrementally rather than all at
// once, which is what makes bootstrapping INDEXF.SYS's own extent list
// possible in the first place: there is no other file whose retrieval
// pointers could be consulted to find the rest of INDEXF.SYS, so it has
// to be able to find itself.
func buildFile(dev *Device, primary ondisk.FileHeader) (*File, error) {
	f := &File{Device: dev, Header: primary}

	header := primary
	for {
		extents, err := header.RetrievalPointers()
		if err != nil {
			return nil, fmt.Errorf("decoding retrieval pointers for file %v: %w", header.Fid, err)
		}
		for _, e := range extents {
			// Every extent of a file is treated as living on the same
			// device the file was opened against (dev.Rvn), rather than
			// trusting a per-segment device value: the reference
			// implementation's own header-extension-following code
			// (fid_copy) always carries forward the ORIGINAL relative
			// volume number rather than reading a new one from each
			// extension segment's on-disk Fid, so a file's data is
			// effectively pinned to the device it started on.
			f.Extents = append(f.Extents, ExtentLocation{Extent: e, Rvn: dev.Rvn})
		}

		if header.ExtensionFid.IsZero() {
			break
		}

		// Which INDEXF.SYS extents do we consult to locate the NEXT
		// header segment? For an ordinary file, opened after Mount has
		// already fully bootstrapped this device's index file, the
		// answer is simply dev.IndexFile.Extents. But while
		// bootstrapping INDEXF.SYS's own File (see bootstrapIndexFile),
		// dev.IndexFile is still nil — this very call is what's building
		// it — so the only extents available yet are whatever this loop
		// has resolved of THIS file (which, for INDEXF.SYS itself, is
		// exactly the right thing to consult: it needs to find itself).
		indexExtents := f.Extents
		if dev.IndexFile != nil {
			indexExtents = dev.IndexFile.Extents
		}

		nextHeader, err := readFileHeaderViaIndex(dev, indexExtents, header.ExtensionFid)
		if err != nil {
			return nil, fmt.Errorf("following header extension chain for file %v: %w", header.Fid, err)
		}
		header = nextHeader
	}

	return f, nil
}

// readFileHeaderViaIndex locates and decodes the on-disk FileHeader for
// fid, given the Extents of INDEXF.SYS (on the same device as fid) needed
// to find it.
//
// A file's header lives at a specific virtual block within INDEXF.SYS,
// computed from its file number and the volume's index-bitmap location —
// the header area immediately follows the index bitmap (see
// HomeBlock.IndexBitmapVBN/IndexBitmapSize), so file number N's header is
// N-1 blocks past the start of that area. That virtual block number is
// then resolved to a physical block through INDEXF.SYS's own extents, the
// same way any other file's data would be.
func readFileHeaderViaIndex(dev *Device, indexExtents []ExtentLocation, fid ondisk.Fid) (ondisk.FileHeader, error) {
	vbn := fid.Number() - 1 + uint32(dev.Home.IndexBitmapVBN) + uint32(dev.Home.IndexBitmapSize)

	buf, err := readExtents(dev, indexExtents, vbn)
	if err != nil {
		return ondisk.FileHeader{}, fmt.Errorf("locating header for file %v (index file VBN %d): %w", fid, vbn, err)
	}

	header, err := ondisk.DecodeFileHeader(buf)
	if err != nil {
		return ondisk.FileHeader{}, fmt.Errorf("decoding header for file %v: %w", fid, err)
	}

	// Confirm the header we found really is the file we were looking
	// for, and not, say, a different file that has since reused the same
	// header slot (detected via a mismatched sequence number) — the same
	// safeguard a stale directory entry's Fid needs, since nothing
	// prevents a file lookup from racing a deletion+reuse of its slot
	// (not a concern for single-threaded use of this package today, but
	// a cheap, worthwhile check regardless).
	if header.Fid.Number() != fid.Number() || header.Fid.Seq != fid.Seq {
		return ondisk.FileHeader{}, fmt.Errorf("file %v: header slot now holds a different file (%v) -- Fid is stale", fid, header.Fid)
	}

	return header, nil
}
