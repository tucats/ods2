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
// caller (DeleteFile, a later subtask) is responsible for that ordering;
// this function only knows how to reclaim what a header chain describes.
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
