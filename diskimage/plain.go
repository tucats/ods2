package diskimage

import "os"

// plainImage is a read-only Container backed by a file whose bytes are
// already the raw ODS-2 volume content, stored back-to-back with no extra
// framing. Logical block N simply lives at byte offset N*BlockSize.
//
// It deliberately has no WriteBlock method, so a Container obtained from
// Open/OpenFormat (which always construct a plainImage, never a
// writablePlainImage below) can never satisfy WritableContainer via a type
// assertion — the way package volume's write path decides whether it's
// allowed to touch a given device (e.g. File.OpenForWrite). If plainImage
// carried WriteBlock itself, that check would trivially succeed for every
// plain image regardless of whether it was actually opened for writing,
// since a type assertion only inspects a value's method set, not how the
// *os.File underneath it happened to be opened; the write would then only
// fail later, confusingly, when the OS rejects a WriteAt against a file
// descriptor opened read-only.
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

func (p *plainImage) Blocks() uint32 {
	return p.blocks
}

func (p *plainImage) Close() error {
	return p.f.Close()
}

// writablePlainImage is a plainImage that also allows overwriting its
// blocks. OpenWritable/OpenFormatWritable/Create construct this type
// instead of plainImage (embedding it to reuse ReadBlock/Blocks/Close
// unchanged), precisely so that WriteBlock's availability tracks whether a
// container was actually opened for writing — see plainImage's own doc
// comment for why that distinction has to live at the type level rather
// than depending on the underlying *os.File's open mode alone.
type writablePlainImage struct {
	plainImage
}

func (p *writablePlainImage) WriteBlock(lbn uint32, buf []byte) error {
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
