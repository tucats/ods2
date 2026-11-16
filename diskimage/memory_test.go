package diskimage

import (
	"bytes"
	"errors"
	"testing"
)

// TestNewMemoryRejectsZeroBlocks confirms a memory image must have at least
// one block, as Create requires of a file.
func TestNewMemoryRejectsZeroBlocks(t *testing.T) {
	if _, err := NewMemory(0); err == nil {
		t.Fatal("NewMemory(0) succeeded, want an error")
	}
}

// TestMemoryImageIsWritableContainer confirms both the image and its views
// satisfy WritableContainer, which is what volume.Initialize and
// volume.Mount's write path look for.
func TestMemoryImageIsWritableContainer(t *testing.T) {
	m, err := NewMemory(10)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	var c Container = m
	if _, ok := c.(WritableContainer); !ok {
		t.Error("*MemoryImage is not a WritableContainer")
	}

	c = m.View()
	if _, ok := c.(WritableContainer); !ok {
		t.Error("a view is not a WritableContainer")
	}
}

// TestMemoryImageReadsZerosAndUsesNoMemory confirms a new image reads as
// zeros everywhere and holds no chunks.
func TestMemoryImageReadsZerosAndUsesNoMemory(t *testing.T) {
	m, err := NewMemory(1000)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	if got := m.Blocks(); got != 1000 {
		t.Errorf("Blocks() = %d, want 1000", got)
	}

	buf := blockFilledWith(0xAA)

	for _, lbn := range []uint32{0, 127, 128, 999} {
		if err := m.ReadBlock(lbn, buf); err != nil {
			t.Fatalf("ReadBlock(%d): %v", lbn, err)
		}

		if !allZero(buf) {
			t.Errorf("block %d of a new image isn't zero", lbn)
		}
	}

	if got := m.Resident(); got != 0 {
		t.Errorf("Resident() of a new image = %d, want 0", got)
	}
}

// TestMemoryImageWriteThenRead confirms each written block reads back,
// within one chunk and across chunk boundaries, and that its neighbors
// are untouched.
func TestMemoryImageWriteThenRead(t *testing.T) {
	m, err := NewMemory(300)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	writes := map[uint32]byte{0: 1, 1: 2, 127: 3, 128: 4, 299: 5}

	for lbn, fill := range writes {
		if err := m.WriteBlock(lbn, blockFilledWith(fill)); err != nil {
			t.Fatalf("WriteBlock(%d): %v", lbn, err)
		}
	}

	buf := make([]byte, BlockSize)

	for lbn := uint32(0); lbn < 300; lbn++ {
		if err := m.ReadBlock(lbn, buf); err != nil {
			t.Fatalf("ReadBlock(%d): %v", lbn, err)
		}

		want := blockFilledWith(writes[lbn]) // 0 for a block not written

		if !bytes.Equal(buf, want) {
			t.Errorf("block %d reads %#x..., want %#x...", lbn, buf[0], want[0])
		}
	}

	// Blocks 0 to 299 span chunks 0, 1, and 2, and each was written.
	if got, want := m.Resident(), int64(3*memoryChunkBlocks*BlockSize); got != want {
		t.Errorf("Resident() = %d, want %d", got, want)
	}
}

// TestMemoryImageOverwriteWithZeros confirms zeros written over data
// replace it (the chunk exists, so the write must happen).
func TestMemoryImageOverwriteWithZeros(t *testing.T) {
	m, err := NewMemory(10)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	if err := m.WriteBlock(4, blockFilledWith(0x55)); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}

	if err := m.WriteBlock(4, make([]byte, BlockSize)); err != nil {
		t.Fatalf("WriteBlock of zeros: %v", err)
	}

	buf := blockFilledWith(0xFF)
	if err := m.ReadBlock(4, buf); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}

	if !allZero(buf) {
		t.Error("block 4 still holds data after zeros were written over it")
	}
}

// TestMemoryImageZerosAllocateNothing confirms writing zeros where nothing
// has been written leaves the image sparse, as volume.Initialize's zeroing
// of the index file and bitmap needs.
func TestMemoryImageZerosAllocateNothing(t *testing.T) {
	m, err := NewMemory(10000)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	zeros := make([]byte, BlockSize)

	for lbn := uint32(0); lbn < 10000; lbn++ {
		if err := m.WriteBlock(lbn, zeros); err != nil {
			t.Fatalf("WriteBlock(%d): %v", lbn, err)
		}
	}

	if got := m.Resident(); got != 0 {
		t.Errorf("Resident() after writing only zeros = %d, want 0", got)
	}
}

// TestMemoryImageWritesOnlyBlockSize confirms a longer buffer's bytes past
// the first block are neither stored nor read back.
func TestMemoryImageWritesOnlyBlockSize(t *testing.T) {
	m, err := NewMemory(2)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	long := append(blockFilledWith(0x11), blockFilledWith(0x22)...)
	if err := m.WriteBlock(0, long); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}

	buf := blockFilledWith(0)
	if err := m.ReadBlock(1, buf); err != nil {
		t.Fatalf("ReadBlock(1): %v", err)
	}

	if !allZero(buf) {
		t.Error("WriteBlock(0) spilled into block 1")
	}
}

// TestMemoryImageArgumentErrors confirms the same errors the file
// containers give: a block past the end, and a buffer shorter than a block.
func TestMemoryImageArgumentErrors(t *testing.T) {
	m, err := NewMemory(5)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	block := make([]byte, BlockSize)
	short := make([]byte, BlockSize-1)

	for name, err := range map[string]error{
		"ReadBlock past the end":  m.ReadBlock(5, block),
		"WriteBlock past the end": m.WriteBlock(5, block),
	} {
		if !errors.Is(err, ErrBlockOutOfRange) {
			t.Errorf("%s: %v, want ErrBlockOutOfRange", name, err)
		}
	}

	for name, err := range map[string]error{
		"ReadBlock short buffer":  m.ReadBlock(0, short),
		"WriteBlock short buffer": m.WriteBlock(0, short),
	} {
		if !errors.Is(err, ErrBufferTooSmall) {
			t.Errorf("%s: %v, want ErrBufferTooSmall", name, err)
		}
	}
}

// TestMemoryViewCloseKeepsImage confirms closing a view ends that view
// only: the image, and a new view of it, still read what was written.
func TestMemoryViewCloseKeepsImage(t *testing.T) {
	m, err := NewMemory(20)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	first := m.View()
	if err := first.WriteBlock(7, blockFilledWith(0x77)); err != nil {
		t.Fatalf("WriteBlock through the first view: %v", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close of the first view: %v", err)
	}

	buf := make([]byte, BlockSize)

	if err := first.ReadBlock(7, buf); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadBlock through a closed view: %v, want ErrClosed", err)
	}

	if err := first.WriteBlock(7, buf); !errors.Is(err, ErrClosed) {
		t.Errorf("WriteBlock through a closed view: %v, want ErrClosed", err)
	}

	if err := first.Close(); err != nil {
		t.Errorf("a second Close of a view: %v, want nil", err)
	}

	second := m.View()
	if got := second.Blocks(); got != 20 {
		t.Errorf("view's Blocks() = %d, want 20", got)
	}

	if err := second.ReadBlock(7, buf); err != nil {
		t.Fatalf("ReadBlock through the second view: %v", err)
	}

	if !bytes.Equal(buf, blockFilledWith(0x77)) {
		t.Error("the second view doesn't read what the first wrote")
	}

	if m.Resident() == 0 {
		t.Error("closing a view freed the image's memory")
	}
}

// TestMemoryImageRelease confirms Release frees the memory and closes the
// image and all its views, and that Close is the same as Release.
func TestMemoryImageRelease(t *testing.T) {
	for _, how := range []string{"Release", "Close"} {
		t.Run(how, func(t *testing.T) {
			m, err := NewMemory(20)
			if err != nil {
				t.Fatalf("NewMemory: %v", err)
			}

			view := m.View()
			if err := view.WriteBlock(3, blockFilledWith(0x33)); err != nil {
				t.Fatalf("WriteBlock: %v", err)
			}

			if how == "Release" {
				m.Release()
			} else if err := m.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if got := m.Resident(); got != 0 {
				t.Errorf("Resident() after %s = %d, want 0", how, got)
			}

			if got := m.Blocks(); got != 20 {
				t.Errorf("Blocks() after %s = %d, want 20", how, got)
			}

			buf := make([]byte, BlockSize)

			for name, err := range map[string]error{
				"image ReadBlock":  m.ReadBlock(3, buf),
				"image WriteBlock": m.WriteBlock(3, buf),
				"view ReadBlock":   view.ReadBlock(3, buf),
				"view WriteBlock":  view.WriteBlock(3, buf),
				"new view":         m.View().ReadBlock(3, buf),
			} {
				if !errors.Is(err, ErrClosed) {
					t.Errorf("%s after %s: %v, want ErrClosed", name, how, err)
				}
			}

			m.Release() // a second release is harmless
		})
	}
}
