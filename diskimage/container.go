package diskimage

import (
	"errors"
	"fmt"
	"os"
)

// BlockSize is the fixed size, in bytes, of one ODS-2 "logical block".
//
// Background for readers new to VMS/ODS-2: an ODS-2 volume always numbers
// and addresses its contents in units of 512 bytes, called Logical Block
// Numbers (LBNs), no matter what the underlying physical media's sector
// size actually is. This is analogous to how a modern OS might address a
// disk in 512-byte or 4096-byte sectors, except ODS-2's choice of 512 is
// baked into the on-disk format itself (checksums, retrieval pointers, and
// so on all count in 512-byte units).
//
// Every package in this module above diskimage (ondisk, volume, ...) works
// exclusively in these 512-byte block numbers. Package diskimage is the one
// place that needs to know how a block number maps onto actual byte offsets
// in a particular container file, so that everything above it can pretend
// every container is just a flat array of 512-byte blocks.
const BlockSize = 512

// ErrBlockOutOfRange is returned by Container.ReadBlock when the requested
// block number is beyond the end of the container.
var ErrBlockOutOfRange = errors.New("diskimage: block number out of range")

// ErrBufferTooSmall is returned by Container.ReadBlock when the caller's
// buffer cannot hold a full block.
var ErrBufferTooSmall = errors.New("diskimage: destination buffer smaller than one block")

// Container is anything that can serve up the 512-byte logical blocks of an
// ODS-2 volume. It hides how those blocks are actually laid out in the
// backing file: a "plain" image stores blocks back-to-back, while a raw
// CD-ROM sector dump interleaves each block's data with sync/header/ECC
// bytes that have to be skipped over. Code above this package (ondisk,
// volume, ...) should never need to know which kind of file it opened.
type Container interface {
	// ReadBlock reads the logical block numbered lbn (blocks are numbered
	// starting at 0) into buf. buf must have length at least BlockSize;
	// only the first BlockSize bytes of buf are written.
	ReadBlock(lbn uint32, buf []byte) error

	// Blocks reports the total number of 512-byte logical blocks available
	// in the container.
	Blocks() uint32

	// Close releases any resources (such as an open file handle) held by
	// the container. After Close, no other method may be called.
	Close() error
}

// WritableContainer is a Container that also allows overwriting its logical
// blocks. Only plain images support this: rewriting a raw CD-ROM sector dump
// would mean recomputing the sync/header/ECC framing around every changed
// block, which isn't needed for this project's actual use case (see
// rawCDImage's doc comment), so *rawCDImage deliberately does not implement
// this interface. Code that needs to write should obtain a WritableContainer
// via OpenWritable/OpenFormatWritable/Create rather than type-asserting a
// Container obtained from Open, so that a raw-CD image fails with a clear
// error message up front instead of a confusing failure part-way through a
// write.
type WritableContainer interface {
	Container

	// WriteBlock writes the first BlockSize bytes of buf to the logical
	// block numbered lbn (blocks are numbered starting at 0). buf must have
	// length at least BlockSize.
	WriteBlock(lbn uint32, buf []byte) error
}

// Format selects which container framing Open should assume. Most callers
// should just use Open, which figures this out automatically; Format exists
// as an escape hatch for the rare case where a file's size happens to be
// ambiguous, or auto-detection otherwise guesses wrong.
type Format int

const (
	// FormatAuto inspects the file's size (and, for the raw-CD case, the
	// first sector's sync pattern) to decide automatically between
	// FormatPlain and FormatRawCD. This is what Open uses.
	FormatAuto Format = iota

	// FormatPlain treats the file as containing raw ODS-2 volume bytes,
	// with logical block N stored at byte offset N*BlockSize. This covers
	// both traditional 512-byte disk-block dumps and ISO images that were
	// already extracted to plain 2048-byte-sector user data (2048 is an
	// exact multiple of 512, so no special handling is needed for those).
	FormatPlain

	// FormatRawCD treats the file as an unprocessed CD-ROM sector dump:
	// 2352 bytes per physical sector, each sector holding a 12-byte sync
	// pattern, a 4-byte header, 2048 bytes of real (ODS-2) data, and
	// trailing error-detection/correction bytes that are discarded.
	FormatRawCD
)

// Open opens the disk image or CD-ROM sector dump at path and returns a
// Container that serves its contents as 512-byte ODS-2 logical blocks,
// automatically detecting whether the file is a plain image or a raw CD-ROM
// sector dump. Use OpenFormat instead if you need to force a specific
// framing.
func Open(path string) (Container, error) {
	return OpenFormat(path, FormatAuto)
}

// OpenFormat opens the disk image or CD-ROM sector dump at path using the
// given Format. Passing FormatAuto behaves exactly like Open.
func OpenFormat(path string, format Format) (Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("diskimage: opening %s: %w", path, err)
	}

	// Make sure we don't leak the *os.File if anything below fails before
	// we hand back a Container that owns it.
	closeOnError := func(err error) (Container, error) {
		_ = f.Close()

		return nil, err
	}

	format, size, err := detectFormat(f, path, format)
	if err != nil {
		return closeOnError(err)
	}

	switch format {
	case FormatRawCD:
		if size%rawSectorSize != 0 {
			return closeOnError(fmt.Errorf(
				"diskimage: %s is %d bytes, not a multiple of the raw CD-ROM sector size (%d)",
				path, size, rawSectorSize))
		}

		sectors := size / rawSectorSize

		return &rawCDImage{f: f, blocks: uint32(sectors) * blocksPerSector}, nil

	case FormatPlain:
		if size%BlockSize != 0 {
			return closeOnError(fmt.Errorf(
				"diskimage: %s is %d bytes, not a multiple of the ODS-2 block size (%d)",
				path, size, BlockSize))
		}

		return &plainImage{f: f, blocks: uint32(size / BlockSize)}, nil

	default:
		return closeOnError(fmt.Errorf("diskimage: unknown format %d", format))
	}
}

// detectFormat validates that the already-opened file f is non-empty and,
// when format is FormatAuto, inspects its size (and, for the raw-CD case,
// the first sector's sync pattern) to decide between FormatPlain and
// FormatRawCD. It returns the resolved format and the file's size in bytes.
//
// This is shared between the read-only Open/OpenFormat and the read-write
// OpenWritable/OpenFormatWritable so that both apply exactly the same
// detection rules regardless of which mode the file was opened in — format
// detection only ever reads bytes, so it doesn't care whether the
// underlying *os.File is also writable.
func detectFormat(f *os.File, path string, format Format) (Format, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("diskimage: stat %s: %w", path, err)
	}

	size := info.Size()
	if size <= 0 {
		return 0, 0, fmt.Errorf("diskimage: %s is empty", path)
	}

	if format == FormatAuto {
		if looksLikeRawCD(f, size) {
			format = FormatRawCD
		} else {
			format = FormatPlain
		}
	}

	return format, size, nil
}

// OpenWritable opens the disk image at path for both reading and writing,
// returning a WritableContainer, automatically detecting whether the file is
// a plain image (the only format this project can write). Use
// OpenFormatWritable instead if you need to force a specific framing.
//
// Opening a raw CD-ROM sector dump this way fails with a clear error rather
// than succeeding and then failing confusingly on the first WriteBlock: see
// WritableContainer's doc comment for why raw CD-ROM images can't be
// written at all.
func OpenWritable(path string) (WritableContainer, error) {
	return OpenFormatWritable(path, FormatAuto)
}

// OpenFormatWritable opens the disk image at path for both reading and
// writing using the given Format, returning a WritableContainer. Passing
// FormatAuto behaves exactly like OpenWritable.
func OpenFormatWritable(path string, format Format) (WritableContainer, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("diskimage: opening %s for write: %w", path, err)
	}

	closeOnError := func(err error) (WritableContainer, error) {
		_ = f.Close()

		return nil, err
	}

	format, size, err := detectFormat(f, path, format)
	if err != nil {
		return closeOnError(err)
	}

	switch format {
	case FormatRawCD:
		return closeOnError(fmt.Errorf(
			"diskimage: %s is a raw CD-ROM sector dump, which this project cannot write to (mounting it /WRITE is not supported)",
			path))

	case FormatPlain:
		if size%BlockSize != 0 {
			return closeOnError(fmt.Errorf(
				"diskimage: %s is %d bytes, not a multiple of the ODS-2 block size (%d)",
				path, size, BlockSize))
		}

		return &writablePlainImage{plainImage{f: f, blocks: uint32(size / BlockSize)}}, nil

	default:
		return closeOnError(fmt.Errorf("diskimage: unknown format %d", format))
	}
}

// Create creates (or truncates, if it already exists) the host file at path
// to hold exactly blocks logical blocks, zero-filled throughout, and returns
// it opened as a WritableContainer (always a plain image — a freshly
// created file has no CD-ROM sync/header/ECC framing to speak of).
//
// This is what INITIALIZE (building a brand-new ODS-2 volume from scratch)
// creates its backing file with, and what write-path unit tests use to get
// a synthetic container to build fixtures on top of, instead of hand-rolling
// an in-memory container or depending on a pre-existing volume dump.
func Create(path string, blocks uint32) (WritableContainer, error) {
	if blocks == 0 {
		return nil, fmt.Errorf("diskimage: Create requires at least one block, got 0")
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("diskimage: creating %s: %w", path, err)
	}

	// Truncate sets the file's length directly rather than writing
	// BlockSize*blocks actual zero bytes; on every filesystem this project
	// cares about, that yields a sparse file whose unwritten regions read
	// back as zero, which is all the "zero-filled" guarantee actually
	// requires.
	size := int64(blocks) * BlockSize
	if err := f.Truncate(size); err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("diskimage: sizing %s to %d bytes: %w", path, size, err)
	}

	return &writablePlainImage{plainImage{f: f, blocks: blocks}}, nil
}
