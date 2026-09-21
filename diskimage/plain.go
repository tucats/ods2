package diskimage

import "os"

// plainImage is a Container backed by a file whose bytes are already the
// raw ODS-2 volume content, stored back-to-back with no extra framing.
// Logical block N simply lives at byte offset N*BlockSize.
type plainImage struct {
	f      *os.File
	blocks uint32
}

func (p *plainImage) ReadBlock(lbn uint32, buf []byte) error {
	if lbn >= p.blocks {
		return ErrBlockOutOfRange
	}
	if len(buf) < BlockSize {
		return ErrBufferTooSmall
	}

	// ReadAt reads from an absolute file offset without disturbing (or
	// depending on) the file's current read position. That matters here
	// because, unlike a simple sequential reader, callers may read blocks
	// of a volume in any order (following retrieval pointers can jump
	// around the file), and a future concurrent reader could be reading a
	// different block of the same open file at the same time. Using
	// ReadAt instead of Seek+Read avoids a whole class of bugs where two
	// reads race over a shared file-position cursor.
	_, err := p.f.ReadAt(buf[:BlockSize], int64(lbn)*BlockSize)
	return err
}

func (p *plainImage) WriteBlock(lbn uint32, buf []byte) error {
	if lbn >= p.blocks {
		return ErrBlockOutOfRange
	}
	if len(buf) < BlockSize {
		return ErrBufferTooSmall
	}

	// WriteAt, like ReadAt above, writes at an absolute file offset without
	// touching the file's current position, so callers can write blocks in
	// any order (e.g. following a file's retrieval pointers, or extending a
	// file past its previous end) without needing a Seek in between.
	_, err := p.f.WriteAt(buf[:BlockSize], int64(lbn)*BlockSize)
	return err
}

func (p *plainImage) Blocks() uint32 {
	return p.blocks
}

func (p *plainImage) Close() error {
	return p.f.Close()
}
