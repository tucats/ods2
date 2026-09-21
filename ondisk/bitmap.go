package ondisk

// This file implements the actual free/allocated bit packing used by a
// volume's storage bitmap, BITMAP.SYS (see StorageControlBlock's doc
// comment for that file's overall layout: an SCB block first, then the
// bitmap bits themselves in the blocks that follow). Package volume reads
// those blocks into one contiguous in-memory buffer and uses the helpers
// below to test and mutate individual bits within it; this package has no
// concept of a "volume" or a device to read those blocks from, only the
// byte-level format once they're in hand.
//
// Each bit here corresponds to one allocation cluster — a run of
// HomeBlock.ClusterSize consecutive blocks that the volume always
// allocates as a single unit, never individually. A cluster number is
// simply "how many clusters into the volume" a given cluster is (cluster 0
// is the volume's first ClusterSize blocks, cluster 1 the next
// ClusterSize, and so on); package volume is responsible for converting
// between a cluster number and the LBN range it covers.
//
// Bit 1 means the cluster is free, bit 0 means it's allocated — this
// polarity is a genuine on-disk convention (the reference implementation's
// update_freecount() counts set bits as free space), so it's kept here
// even though nothing else about this packing is ported from the
// reference. See PHASE-02.md's "what we're deliberately not porting"
// table: the reference packs bits into native-word-sized (int, normally 32
// bits, but a char on some big-endian builds) units, an
// endianness-and-word-size-dependent scheme this project doesn't
// reproduce. Instead, bits are packed one per cluster, LSB-first within
// each byte: cluster N's bit lives at bits[N/8], bit position N%8 of that
// byte (1<<(N%8)). This is simpler, host-independent, and — because no
// real writable test image was available while this project's bitmap
// support was being built — the layout PHASE-02.md flags as needing
// confirmation against a real volume's actual BITMAP.SYS bytes if one ever
// becomes available.
//
// None of the three functions below bounds-check cluster against len(bits)
// themselves: bits is a buffer the caller (package volume) sized to cover
// every valid cluster number up front, so an out-of-range cluster here
// means a bug in that caller, not corrupt on-disk data — the same
// distinction this package draws elsewhere between validating untrusted
// bytes read from disk (which it does) and trusting a caller-owned,
// correctly-sized buffer (which it doesn't re-validate). An out-of-range
// call panics with Go's ordinary "index out of range", same as any other
// bad slice index would.

// BitmapTest reports whether the given cluster's bit is set (free) within
// bits, a decoded storage-bitmap buffer. See this file's package-level
// comment for the bit-packing convention and the free/allocated polarity.
func BitmapTest(bits []byte, cluster uint32) bool {
	return bits[cluster/8]&(1<<(cluster%8)) != 0
}

// BitmapSet marks the given cluster free (sets its bit to 1) within bits.
func BitmapSet(bits []byte, cluster uint32) {
	bits[cluster/8] |= 1 << (cluster % 8)
}

// BitmapClear marks the given cluster allocated (clears its bit to 0)
// within bits.
func BitmapClear(bits []byte, cluster uint32) {
	bits[cluster/8] &^= 1 << (cluster % 8)
}
