package ondisk

import (
	"encoding/binary"
	"fmt"
)

// BlockSize is the size, in bytes, of one physical/logical block on an
// ODS-2 volume — and, not coincidentally, also the size of both the volume
// home block and every file header, since ODS-2 was designed so that each
// of those structures fits in exactly one block.
//
// This is intentionally the same value as diskimage.BlockSize, but this
// package defines its own copy rather than importing package diskimage,
// because ondisk's job is purely "decode these bytes according to the
// ODS-2 format" — it has no business knowing how those bytes were read
// from a container file. Keeping that separation means ondisk could, in
// principle, be reused against bytes obtained any other way (e.g. bytes
// embedded in a test fixture, or streamed from a network) without dragging
// in the diskimage package at all.
const BlockSize = 512

// Checksum computes the ODS-2 block checksum used to detect corruption in
// the volume home block and in every file header. Both of those
// structures reserve their very last 16-bit word on disk to hold a
// checksum of the rest of the block, computed by this exact algorithm.
//
// The algorithm (faithfully reproduced from the reference implementation,
// quirks and all): treat the first 510 bytes of the block — i.e. every
// 16-bit word except the last one, which is where the checksum value
// itself lives — as 255 little-endian 16-bit words, and add them together
// using plain unsigned 32-bit arithmetic. There is no wraparound applied
// during the summation (each partial sum is allowed to exceed 16 bits) and
// no "end-around carry" folding like an Internet/IP checksum uses. Only at
// the very end is the 32-bit running total narrowed down to a 16-bit
// result, by simply discarding its high 16 bits.
//
// block must be exactly BlockSize (512) bytes long — both HomeBlock and
// FileHeader are that size, and this function only ever operates on a
// whole block at a time.
func Checksum(block []byte) (uint16, error) {
	if len(block) != BlockSize {
		return 0, fmt.Errorf("ondisk: Checksum requires exactly %d bytes, got %d", BlockSize, len(block))
	}

	var sum uint32

	for i := range 255 {
		word := binary.LittleEndian.Uint16(block[i*2 : i*2+2])
		sum += uint32(word)
	}
	// Discarding the high bits here is deliberate, not a bug: the on-disk
	// checksum field is only 16 bits wide, so this is exactly what the
	// original algorithm does too.
	return uint16(sum), nil
}
