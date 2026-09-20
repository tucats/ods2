package diskimage

import "os"

// The following constants describe the physical layout of one sector in a
// "raw" CD-ROM image: a byte-for-byte dump of every sector exactly as it
// comes off the disc, rather than just the useful data. This is what you
// get from some CD-imaging tools (cdrdao and similar) instead of a plain
// ISO file. Each 2352-byte physical sector looks like this on disc:
//
//	byte offset   length   contents
//	0             12       sync pattern (always the same fixed bytes)
//	12            4        header (sector address + mode byte)
//	16            2048     the actual 2048 bytes of user data we want
//	2064          288      error-detection/correction bytes we discard
//
// A plain ISO image, by contrast, contains only the 2048-byte user-data
// portions of every sector, concatenated with no sync/header/ECC bytes in
// between. So to read logical data out of a raw CD dump we have to skip
// over the 16 bytes of sync+header at the start of every 2352-byte sector,
// and stop after 2048 bytes rather than reading the ECC tail.
const (
	rawSectorSize = 2352 // total bytes per physical sector, including framing
	rawSyncLen    = 12   // length of the sync pattern at the start of a sector
	rawHeaderLen  = 4    // length of the header that follows the sync pattern
	rawDataLen    = 2048 // length of the real user data within a sector

	// rawDataOffset is where the 2048 bytes of real data begin within a
	// 2352-byte physical sector, i.e. right after the sync pattern and
	// header.
	rawDataOffset = rawSyncLen + rawHeaderLen

	// blocksPerSector is how many 512-byte ODS-2 logical blocks fit inside
	// one sector's 2048 bytes of user data. 2048 divides evenly into 512
	// four times, which is what makes it possible to serve ODS-2 blocks
	// out of a raw CD image at all without re-slicing block boundaries
	// across sectors.
	blocksPerSector = rawDataLen / BlockSize
)

// rawSyncPattern is the fixed 12-byte pattern that begins every CD-ROM
// sector: one zero byte, ten 0xFF bytes, and a final zero byte. Real CD
// images always start every sector with this exact sequence, so checking
// for it is a reliable (if not infallible) way to confirm a file really is
// a raw sector dump and not, say, a plain image that merely happens to have
// a size divisible by 2352.
var rawSyncPattern = [rawSyncLen]byte{
	0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00,
}

// looksLikeRawCD reports whether f appears to be a raw CD-ROM sector dump:
// its size must be an exact multiple of the 2352-byte physical sector size,
// and its very first sector must begin with the expected sync pattern.
//
// Checking only the size would already be a strong signal on its own: 2352
// is not a multiple of 512 (2352 / 512 = 4.59...), so an ordinary ODS-2
// volume image — whose size is always a whole number of 512-byte blocks —
// could only coincidentally also be an exact multiple of 2352, which is
// vanishingly unlikely in practice. The sync-pattern check is a cheap extra
// confirmation before we commit to interpreting the whole file as raw
// sectors.
func looksLikeRawCD(f *os.File, size int64) bool {
	if size <= 0 || size%rawSectorSize != 0 {
		return false
	}

	var header [rawSyncLen]byte
	if _, err := f.ReadAt(header[:], 0); err != nil {
		return false
	}
	return header == rawSyncPattern
}

// rawCDImage is a Container backed by a raw CD-ROM sector dump. It presents
// the same flat "array of 512-byte blocks" view as plainImage, but has to
// compute, for each requested block, which physical sector it falls in and
// how far into that sector's 2048-byte data region it starts.
type rawCDImage struct {
	f      *os.File
	blocks uint32
}

func (r *rawCDImage) ReadBlock(lbn uint32, buf []byte) error {
	if lbn >= r.blocks {
		return ErrBlockOutOfRange
	}
	if len(buf) < BlockSize {
		return ErrBufferTooSmall
	}

	// Every physical sector holds exactly blocksPerSector (4) ODS-2 blocks
	// worth of data, back to back, starting at rawDataOffset. So block lbn
	// lives in sector (lbn / blocksPerSector), at a position
	// (lbn % blocksPerSector) blocks into that sector's data region.
	sector := lbn / blocksPerSector
	blockWithinSector := lbn % blocksPerSector

	offset := int64(sector)*rawSectorSize +
		rawDataOffset +
		int64(blockWithinSector)*BlockSize

	_, err := r.f.ReadAt(buf[:BlockSize], offset)
	return err
}

func (r *rawCDImage) Blocks() uint32 {
	return r.blocks
}

func (r *rawCDImage) Close() error {
	return r.f.Close()
}
