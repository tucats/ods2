package ondisk

import (
	"encoding/binary"
	"fmt"
)

// Extent describes one contiguous run of a file's data blocks: Count
// consecutive logical blocks (LBNs) on the device, starting at StartLBN.
//
// Background: ODS-2 doesn't store a file's block layout as a simple flat
// list ("block 0 is at LBN X, block 1 is at LBN X+1, block 2 is at LBN
// Y, ..."). Instead it stores "retrieval pointers": a compact run-length
// encoding that says "the next N virtual blocks of this file are stored
// starting at logical block M", repeated for as many contiguous runs as
// the file's data happens to be split across (a perfectly unfragmented
// file needs only one such entry; a heavily fragmented one needs many).
// Extent is this project's name for one such run, after decoding.
type Extent struct {
	Count    uint32
	StartLBN uint32
}

// RetrievalPointers decodes this file header's retrieval-pointer ("map")
// area into an ordered list of Extents, describing where the file's data
// actually lives on disk, in virtual-block order (the first Extent
// describes the file's first blocks, and so on).
//
// A single file header has a fixed amount of room for retrieval pointers.
// A file large or fragmented enough to need more continues into one or
// more "extension header" segments, chased via ExtensionFid — ordinary
// FileHeader values here don't know how to fetch another header segment
// from disk (that requires access to the volume, which this package has no
// concept of), so RetrievalPointers only ever decodes what fits in THIS
// header. A caller that needs a file's complete extent list should decode
// this header's Extents, then, if ExtensionFid is not the zero Fid (see
// Fid.IsZero), look up and decode that header too, and repeat, appending
// each segment's Extents in order. Package volume implements that walk.
func (h *FileHeader) RetrievalPointers() ([]Extent, error) {
	mapStart := int(h.MapOffset) * 2
	mapEnd := mapStart + int(h.MapWordsInUse)*2

	if mapStart < 0 || mapEnd > len(h.raw) {
		return nil, fmt.Errorf(
			"ondisk: file header map area [%d:%d) is out of bounds for a %d-byte header",
			mapStart, mapEnd, len(h.raw))
	}

	// readWord reads the 16-bit little-endian word at the given BYTE
	// offset within the header's raw bytes. Retrieval pointer entries can
	// be 1 to 4 words long, and — matching the original implementation —
	// a word belonging to one entry is allowed to be read even if it
	// falls slightly past the nominal end of the map area (mapEnd), as
	// long as it's still within the 512-byte header block; this can only
	// happen with corrupt data, since a well-formed map area always ends
	// exactly on an entry boundary.
	readWord := func(byteOffset int) (uint16, error) {
		if byteOffset < 0 || byteOffset+2 > len(h.raw) {
			return 0, fmt.Errorf("ondisk: retrieval pointer entry runs past the end of the file header")
		}
		return binary.LittleEndian.Uint16(h.raw[byteOffset : byteOffset+2]), nil
	}

	var extents []Extent
	pos := mapStart
	for pos < mapEnd {
		word0, err := readWord(pos)
		if err != nil {
			return nil, err
		}

		// The top 2 bits of an entry's first word select one of four
		// on-disk encodings for "how many consecutive blocks, and
		// starting where". The formats trade off how large a count or
		// LBN they can represent against how many words (and therefore
		// how much header space) they consume — a volume only uses the
		// more compact formats when the values involved are small enough
		// to fit.
		switch word0 >> 14 {
		case 0:
			// Format 0: a placeholder/filler entry with no meaning of
			// its own. It contributes no extent and is just skipped.
			// One word.
			pos += 2

		case 1:
			// Format 1 ("byte" format): a short run of up to 256 blocks,
			// for volumes small enough that a 22-bit LBN suffices. Two
			// words: the low 8 bits of word0 are the block count minus
			// one; bits 8-13 of word0 supply the top 6 bits of the LBN,
			// and word1 supplies the low 16 bits.
			word1, err := readWord(pos + 2)
			if err != nil {
				return nil, err
			}
			count := uint32(word0&0x00FF) + 1
			lbn := uint32(word0&0x3F00)<<8 | uint32(word1)
			extents = append(extents, Extent{Count: count, StartLBN: lbn})
			pos += 4

		case 2:
			// Format 2 ("word" format): a 14-bit count (word0's low 14
			// bits, minus one) plus a full 32-bit LBN split across two
			// more words (word1 = low 16 bits, word2 = high 16 bits).
			// Three words total.
			word1, err := readWord(pos + 2)
			if err != nil {
				return nil, err
			}
			word2, err := readWord(pos + 4)
			if err != nil {
				return nil, err
			}
			count := uint32(word0&0x3FFF) + 1
			lbn := uint32(word2)<<16 | uint32(word1)
			extents = append(extents, Extent{Count: count, StartLBN: lbn})
			pos += 6

		case 3:
			// Format 3 ("longword" format): for extents whose block
			// count needs more than 14 bits to express, the count is
			// itself split across two words (word0's low 14 bits become
			// the high part, word1 the low 16 bits, minus one overall),
			// and the LBN is a full 32 bits split across word2/word3.
			// Four words total.
			word1, err := readWord(pos + 2)
			if err != nil {
				return nil, err
			}
			word2, err := readWord(pos + 4)
			if err != nil {
				return nil, err
			}
			word3, err := readWord(pos + 6)
			if err != nil {
				return nil, err
			}
			count := uint32(word0&0x3FFF)<<16 + uint32(word1) + 1
			lbn := uint32(word3)<<16 | uint32(word2)
			extents = append(extents, Extent{Count: count, StartLBN: lbn})
			pos += 8
		}
	}

	return extents, nil
}
