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

// Per-format limits, derived directly from the bit layout RetrievalPointers
// decodes above: how many blocks (Count) and how high an LBN (StartLBN)
// each of the three real formats can represent. Format 0 (placeholder) is
// never a target of encoding — it carries no extent, so there's nothing an
// Extent would ever choose it to represent.
const (
	// Format 1 ("byte" format): an 8-bit count field (0-255, i.e. Count
	// 1-256) and a 22-bit LBN (6 bits packed into word0, 16 into word1).
	retrievalMaxCountFormat1 = 0x100    // Count must be <= this
	retrievalMaxLBNFormat1   = 0x3FFFFF // StartLBN must be <= this

	// Format 2 ("word" format): a 14-bit count field (Count 1-16384) but a
	// full 32-bit LBN, so only Count constrains whether this format fits.
	retrievalMaxCountFormat2 = 0x4000

	// Format 3 ("longword" format): a 30-bit count field (split across two
	// words) and a full 32-bit LBN — the widest format, always able to
	// represent any Extent this project can construct (Count is a uint32,
	// and 0xFFFFFFFF+1 doesn't fit in 30 bits either, so a Count above this
	// ceiling is rejected as unrepresentable rather than silently wrapping).
	retrievalMaxCountFormat3 = 0x40000000
)

// EncodeRetrievalPointers encodes extents into the packed on-disk
// retrieval-pointer ("map") area bytes — the inverse of
// FileHeader.RetrievalPointers, and (once chased across every extension
// segment — package volume's job, not this one) of a file's complete
// extent list. Unlike the reference implementation, which always emits the
// widest ("longword") format regardless of whether a narrower one would
// represent the same extent (see PHASE-02.md's "what we're deliberately
// not porting" table), this picks the smallest of the three real formats
// that fits each extent's Count and StartLBN, to avoid wasting header
// space that a large or fragmented file may need for other extents.
//
// The result's length in words (len(result)/2) is what a caller should
// store in FileHeader.MapWordsInUse (or FileHeaderAreas.MapBytes, whose
// length EncodeFileHeader already derives that field from).
func EncodeRetrievalPointers(extents []Extent) ([]byte, error) {
	var b []byte

	for i, e := range extents {
		enc, err := encodeExtent(e)
		if err != nil {
			return nil, fmt.Errorf("ondisk: encoding retrieval pointer %d: %w", i, err)
		}
		b = append(b, enc...)
	}

	return b, nil
}

// encodeExtent encodes a single Extent into 2, 3, or 4 words (format 1, 2,
// or 3 respectively — see RetrievalPointers' documentation for the exact
// bit layout each format uses; this is its precise inverse), choosing the
// narrowest format that can represent e.
func encodeExtent(e Extent) ([]byte, error) {
	if e.Count == 0 {
		return nil, fmt.Errorf("ondisk: extent Count must be at least 1, got 0")
	}

	switch {
	case e.Count <= retrievalMaxCountFormat1 && e.StartLBN <= retrievalMaxLBNFormat1:
		b := make([]byte, 4)
		word0 := uint16(1)<<14 | uint16((e.StartLBN>>16)&0x3F)<<8 | uint16(e.Count-1)
		binary.LittleEndian.PutUint16(b[0:2], word0)
		binary.LittleEndian.PutUint16(b[2:4], uint16(e.StartLBN))
		return b, nil

	case e.Count <= retrievalMaxCountFormat2:
		b := make([]byte, 6)
		word0 := uint16(2)<<14 | uint16(e.Count-1)
		binary.LittleEndian.PutUint16(b[0:2], word0)
		binary.LittleEndian.PutUint16(b[2:4], uint16(e.StartLBN))
		binary.LittleEndian.PutUint16(b[4:6], uint16(e.StartLBN>>16))
		return b, nil

	case e.Count <= retrievalMaxCountFormat3:
		b := make([]byte, 8)
		countMinus1 := e.Count - 1
		word0 := uint16(3)<<14 | uint16((countMinus1>>16)&0x3FFF)
		binary.LittleEndian.PutUint16(b[0:2], word0)
		binary.LittleEndian.PutUint16(b[2:4], uint16(countMinus1))
		binary.LittleEndian.PutUint16(b[4:6], uint16(e.StartLBN))
		binary.LittleEndian.PutUint16(b[6:8], uint16(e.StartLBN>>16))
		return b, nil

	default:
		return nil, fmt.Errorf(
			"ondisk: extent Count %d exceeds the largest representable retrieval-pointer count (%d)",
			e.Count, retrievalMaxCountFormat3)
	}
}
