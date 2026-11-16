package diskimage

import (
	"errors"
	"fmt"
	"sync"
)

// ErrClosed is returned by every method of a MemoryImage after Release (or
// Close), and by ReadBlock and WriteBlock of a view after the view's own
// Close.
var ErrClosed = errors.New("diskimage: container is closed")

// memoryChunkBlocks is how many logical blocks a MemoryImage allocates at a
// time: 128 blocks, 64 KB. A chunk is the unit of the image's sparseness. It
// is allocated the first time a block in it is written with anything but
// zeros, and until then reads as zeros without using any memory. 64 KB is
// large enough that a full image's map stays small (a 250 MB disk has at
// most 4,000 chunks) and small enough that a volume's scattered structures
// (the home blocks, the index file, the bitmap, each directory) don't pull
// in much memory around them.
const memoryChunkBlocks = 128

// MemoryImage is an ODS-2 container whose blocks are held in Go memory
// rather than in a host file: a RAM disk. It starts with every block zero,
// as Create's sparse file does, so volume.Initialize can format it and
// volume.Mount can mount it like any other WritableContainer.
//
// # Sparse storage
//
// The blocks live in chunks of memoryChunkBlocks, kept in a map by chunk
// number. A chunk exists only once some block in it has been written with
// data that isn't all zeros. Writing zeros to a block in a missing chunk
// changes nothing (it already reads as zeros), so it allocates nothing. That
// matters because volume.Initialize zeroes the blocks it lays out, the whole
// index file and storage bitmap among them. An image therefore costs about
// what has been written to it, however large it was made. A chunk is never
// given back while the image lives, even after the files in it are deleted:
// the container can't know which blocks the volume considers free, so an
// image's memory is its high-water mark.
//
// # Views
//
// A volume closes its containers when it is dismounted (volume.Dismount).
// For a file that's right: the data is safe on the host. For a MemoryImage,
// closing would throw the data away, and a RAM disk that is dismounted and
// mounted again should find its files still there. So a MemoryImage hands
// out views (View): each is a WritableContainer over the image's blocks
// whose Close ends only that view. Give volume.Initialize and volume.Mount a
// view, and the image outlives every dismount until its owner calls
// Release.
//
// A MemoryImage is also a WritableContainer itself, its Close the same as
// Release, for a caller that wants a plain memory disk with no remounting.
//
// # Concurrency
//
// A mutex guards the image, so views of it may be used from several
// goroutines. The cost is small next to the 512-byte copy each call makes.
type MemoryImage struct {
	mu       sync.Mutex
	blocks   uint32
	chunks   map[uint32][]byte
	released bool
}

// NewMemory returns a MemoryImage of blocks logical blocks, every one zero,
// using no memory for them yet. It fails for a size of 0 blocks.
func NewMemory(blocks uint32) (*MemoryImage, error) {
	if blocks == 0 {
		return nil, fmt.Errorf("diskimage: NewMemory requires at least one block, got 0")
	}

	return &MemoryImage{blocks: blocks, chunks: map[uint32][]byte{}}, nil
}

// Blocks reports the image's size in logical blocks. It stays the same after
// Release, since it describes the image rather than its memory.
func (m *MemoryImage) Blocks() uint32 {
	return m.blocks
}

// ReadBlock copies block lbn into buf, giving zeros for a block never
// written.
func (m *MemoryImage) ReadBlock(lbn uint32, buf []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.read(lbn, buf)
}

// WriteBlock copies the first BlockSize bytes of buf into block lbn.
func (m *MemoryImage) WriteBlock(lbn uint32, buf []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.write(lbn, buf)
}

// Close is Release, so that a MemoryImage used directly as a container
// frees its memory when the volume on it is dismounted. It always returns
// nil.
func (m *MemoryImage) Close() error {
	m.Release()

	return nil
}

// Release frees the image's memory. Every later call on the image, or on a
// view of it, returns ErrClosed. Releasing an image twice is harmless.
func (m *MemoryImage) Release() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.released = true
	m.chunks = nil
}

// Resident reports how many bytes of block data the image holds now: its
// allocated chunks' size, 0 after Release.
func (m *MemoryImage) Resident() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return int64(len(m.chunks)) * memoryChunkBlocks * BlockSize
}

// View returns a new WritableContainer over the image's blocks. Closing the
// view ends that view only: the image and its blocks stay, and a later view
// reads what this one wrote.
func (m *MemoryImage) View() WritableContainer {
	return &memoryView{image: m}
}

// read is ReadBlock's work, with m.mu held.
func (m *MemoryImage) read(lbn uint32, buf []byte) error {
	if err := m.check(lbn, buf); err != nil {
		return err
	}

	chunk, ok := m.chunks[lbn/memoryChunkBlocks]
	if !ok {
		clear(buf[:BlockSize])

		return nil
	}

	offset := (lbn % memoryChunkBlocks) * BlockSize
	copy(buf[:BlockSize], chunk[offset:offset+BlockSize])

	return nil
}

// write is WriteBlock's work, with m.mu held.
func (m *MemoryImage) write(lbn uint32, buf []byte) error {
	if err := m.check(lbn, buf); err != nil {
		return err
	}

	number := lbn / memoryChunkBlocks

	chunk, ok := m.chunks[number]
	if !ok {
		// A block of zeros written where nothing has been written yet
		// leaves the block as it already reads, so it needs no chunk.
		if allZero(buf[:BlockSize]) {
			return nil
		}

		chunk = make([]byte, memoryChunkBlocks*BlockSize)
		m.chunks[number] = chunk
	}

	offset := (lbn % memoryChunkBlocks) * BlockSize
	copy(chunk[offset:offset+BlockSize], buf[:BlockSize])

	return nil
}

// check validates a block transfer's arguments, in the order the file
// containers check them.
func (m *MemoryImage) check(lbn uint32, buf []byte) error {
	if m.released {
		return ErrClosed
	}

	if lbn >= m.blocks {
		return ErrBlockOutOfRange
	}

	if len(buf) < BlockSize {
		return ErrBufferTooSmall
	}

	return nil
}

// allZero reports whether every byte of b is zero.
func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}

	return true
}

// memoryView is one View of a MemoryImage. Its closed flag is guarded by
// the image's mutex, like everything else about the image.
type memoryView struct {
	image  *MemoryImage
	closed bool
}

func (v *memoryView) ReadBlock(lbn uint32, buf []byte) error {
	v.image.mu.Lock()
	defer v.image.mu.Unlock()

	if v.closed {
		return ErrClosed
	}

	return v.image.read(lbn, buf)
}

func (v *memoryView) WriteBlock(lbn uint32, buf []byte) error {
	v.image.mu.Lock()
	defer v.image.mu.Unlock()

	if v.closed {
		return ErrClosed
	}

	return v.image.write(lbn, buf)
}

func (v *memoryView) Blocks() uint32 {
	return v.image.blocks
}

// Close ends the view; the image keeps its blocks. Closing a view twice is
// harmless.
func (v *memoryView) Close() error {
	v.image.mu.Lock()
	defer v.image.mu.Unlock()

	v.closed = true

	return nil
}
