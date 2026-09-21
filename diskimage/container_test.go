package diskimage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a small test helper that writes data to a new file inside
// t.TempDir() and returns the path. t.TempDir() gives each test its own
// scratch directory that Go automatically deletes when the test finishes,
// so tests never need to clean up after themselves.
func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing test fixture %s: %v", name, err)
	}
	return path
}

// blockFilledWith builds one BlockSize-byte block whose every byte has the
// given value, so tests can tell blocks apart just by looking at them.
func blockFilledWith(value byte) []byte {
	b := make([]byte, BlockSize)
	for i := range b {
		b[i] = value
	}
	return b
}

// buildPlainImage concatenates the given blocks into a single plain-image
// byte slice, i.e. exactly what a real plain ODS-2 volume dump looks like:
// blocks stored back-to-back with no extra framing.
func buildPlainImage(blocks [][]byte) []byte {
	var buf bytes.Buffer
	for _, b := range blocks {
		buf.Write(b)
	}
	return buf.Bytes()
}

// buildRawCDImage wraps 4 blocks (blocksPerSector) at a time into a
// synthetic raw CD-ROM sector, complete with the sync pattern, a
// placeholder 4-byte header, and a placeholder ECC tail, mirroring the
// physical layout described in rawcd.go. It panics if the block count
// isn't a multiple of blocksPerSector, since a real CD image can't have a
// partial sector either.
func buildRawCDImage(blocks [][]byte) []byte {
	if len(blocks)%blocksPerSector != 0 {
		panic("buildRawCDImage: block count must be a multiple of blocksPerSector")
	}

	var buf bytes.Buffer
	for sectorStart := 0; sectorStart < len(blocks); sectorStart += blocksPerSector {
		buf.Write(rawSyncPattern[:])
		buf.Write(make([]byte, rawHeaderLen)) // header contents don't matter for our purposes
		for i := 0; i < blocksPerSector; i++ {
			buf.Write(blocks[sectorStart+i])
		}
		eccLen := rawSectorSize - rawDataOffset - rawDataLen
		buf.Write(make([]byte, eccLen)) // ECC/EDC tail; contents are never read
	}
	return buf.Bytes()
}

func TestPlainImageReadBlock(t *testing.T) {
	blocks := [][]byte{blockFilledWith(0xAA), blockFilledWith(0xBB), blockFilledWith(0xCC)}
	path := writeFile(t, "plain.img", buildPlainImage(blocks))

	c, err := OpenFormat(path, FormatPlain)
	if err != nil {
		t.Fatalf("OpenFormat: %v", err)
	}
	defer c.Close()

	if got, want := c.Blocks(), uint32(len(blocks)); got != want {
		t.Fatalf("Blocks() = %d, want %d", got, want)
	}

	for i, want := range blocks {
		got := make([]byte, BlockSize)
		if err := c.ReadBlock(uint32(i), got); err != nil {
			t.Fatalf("ReadBlock(%d): %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadBlock(%d) returned wrong data", i)
		}
	}
}

func TestPlainImageOutOfRange(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1)}))
	c, err := OpenFormat(path, FormatPlain)
	if err != nil {
		t.Fatalf("OpenFormat: %v", err)
	}
	defer c.Close()

	buf := make([]byte, BlockSize)
	if err := c.ReadBlock(1, buf); !errors.Is(err, ErrBlockOutOfRange) {
		t.Fatalf("ReadBlock(1) error = %v, want ErrBlockOutOfRange", err)
	}
}

func TestPlainImageBufferTooSmall(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1)}))
	c, err := OpenFormat(path, FormatPlain)
	if err != nil {
		t.Fatalf("OpenFormat: %v", err)
	}
	defer c.Close()

	buf := make([]byte, BlockSize-1)
	if err := c.ReadBlock(0, buf); !errors.Is(err, ErrBufferTooSmall) {
		t.Fatalf("ReadBlock with short buffer error = %v, want ErrBufferTooSmall", err)
	}
}

func TestPlainImageRejectsUnalignedSize(t *testing.T) {
	// One full block plus one stray extra byte: not a whole number of
	// 512-byte blocks, so this can't be a valid plain image.
	data := append(buildPlainImage([][]byte{blockFilledWith(1)}), 0x00)
	path := writeFile(t, "bad.img", data)

	if _, err := OpenFormat(path, FormatPlain); err == nil {
		t.Fatal("OpenFormat(FormatPlain) on misaligned file: want error, got nil")
	}
}

func TestRawCDImageReadBlock(t *testing.T) {
	// Two full sectors' worth of blocks (8 blocks), so the test exercises
	// both intra-sector addressing (blocks 0-3 and 4-7 within a sector)
	// and crossing from one sector into the next (block 3 -> block 4).
	blocks := make([][]byte, 2*blocksPerSector)
	for i := range blocks {
		blocks[i] = blockFilledWith(byte(i + 1))
	}
	path := writeFile(t, "raw.img", buildRawCDImage(blocks))

	c, err := OpenFormat(path, FormatRawCD)
	if err != nil {
		t.Fatalf("OpenFormat: %v", err)
	}
	defer c.Close()

	if got, want := c.Blocks(), uint32(len(blocks)); got != want {
		t.Fatalf("Blocks() = %d, want %d", got, want)
	}

	for i, want := range blocks {
		got := make([]byte, BlockSize)
		if err := c.ReadBlock(uint32(i), got); err != nil {
			t.Fatalf("ReadBlock(%d): %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadBlock(%d) returned wrong data", i)
		}
	}
}

func TestRawCDImageOutOfRange(t *testing.T) {
	blocks := make([][]byte, blocksPerSector)
	for i := range blocks {
		blocks[i] = blockFilledWith(byte(i))
	}
	path := writeFile(t, "raw.img", buildRawCDImage(blocks))

	c, err := OpenFormat(path, FormatRawCD)
	if err != nil {
		t.Fatalf("OpenFormat: %v", err)
	}
	defer c.Close()

	buf := make([]byte, BlockSize)
	if err := c.ReadBlock(uint32(len(blocks)), buf); !errors.Is(err, ErrBlockOutOfRange) {
		t.Fatalf("ReadBlock past end error = %v, want ErrBlockOutOfRange", err)
	}
}

func TestOpenAutoDetectsPlainImage(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1), blockFilledWith(2)}))

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if _, ok := c.(*plainImage); !ok {
		t.Fatalf("Open auto-detected %T, want *plainImage", c)
	}
}

// TestOpenPlainImageDoesNotImplementWritableContainer guards against a bug
// where plainImage carried a WriteBlock method unconditionally: since a
// type assertion only inspects a value's method set, a Container obtained
// from the read-only Open would then satisfy WritableContainer regardless
// of whether the underlying file was actually opened for writing, defeating
// every write-path "is this device open for write" check in package volume
// (e.g. File.OpenForWrite) — the write would still reach WriteAt, and only
// fail there, confusingly, because the *os.File itself was opened
// read-only.
func TestOpenPlainImageDoesNotImplementWritableContainer(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1)}))

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if _, ok := c.(WritableContainer); ok {
		t.Fatalf("%T from Open unexpectedly implements WritableContainer", c)
	}
}

func TestOpenAutoDetectsRawCDImage(t *testing.T) {
	blocks := make([][]byte, blocksPerSector)
	for i := range blocks {
		blocks[i] = blockFilledWith(byte(i))
	}
	path := writeFile(t, "raw.img", buildRawCDImage(blocks))

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if _, ok := c.(*rawCDImage); !ok {
		t.Fatalf("Open auto-detected %T, want *rawCDImage", c)
	}
}

func TestOpenRejectsEmptyFile(t *testing.T) {
	path := writeFile(t, "empty.img", nil)
	if _, err := Open(path); err == nil {
		t.Fatal("Open on empty file: want error, got nil")
	}
}

func TestOpenRejectsMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "does-not-exist.img")); err == nil {
		t.Fatal("Open on missing file: want error, got nil")
	}
}

func TestPlainImageWriteBlock(t *testing.T) {
	blocks := [][]byte{blockFilledWith(0xAA), blockFilledWith(0xBB), blockFilledWith(0xCC)}
	path := writeFile(t, "plain.img", buildPlainImage(blocks))

	c, err := OpenFormatWritable(path, FormatPlain)
	if err != nil {
		t.Fatalf("OpenFormatWritable: %v", err)
	}
	defer c.Close()

	// Overwrite the first and last blocks; leave the middle one alone, to
	// confirm WriteBlock doesn't disturb neighboring blocks.
	if err := c.WriteBlock(0, blockFilledWith(0x11)); err != nil {
		t.Fatalf("WriteBlock(0): %v", err)
	}
	if err := c.WriteBlock(2, blockFilledWith(0x33)); err != nil {
		t.Fatalf("WriteBlock(2): %v", err)
	}

	want := [][]byte{blockFilledWith(0x11), blockFilledWith(0xBB), blockFilledWith(0x33)}
	for i, w := range want {
		got := make([]byte, BlockSize)
		if err := c.ReadBlock(uint32(i), got); err != nil {
			t.Fatalf("ReadBlock(%d): %v", i, err)
		}
		if !bytes.Equal(got, w) {
			t.Fatalf("block %d after write does not match what was written", i)
		}
	}
}

func TestPlainImageWriteBlockOutOfRange(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1)}))
	c, err := OpenFormatWritable(path, FormatPlain)
	if err != nil {
		t.Fatalf("OpenFormatWritable: %v", err)
	}
	defer c.Close()

	if err := c.WriteBlock(1, blockFilledWith(2)); !errors.Is(err, ErrBlockOutOfRange) {
		t.Fatalf("WriteBlock(1) error = %v, want ErrBlockOutOfRange", err)
	}
}

func TestPlainImageWriteBlockBufferTooSmall(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1)}))
	c, err := OpenFormatWritable(path, FormatPlain)
	if err != nil {
		t.Fatalf("OpenFormatWritable: %v", err)
	}
	defer c.Close()

	buf := make([]byte, BlockSize-1)
	if err := c.WriteBlock(0, buf); !errors.Is(err, ErrBufferTooSmall) {
		t.Fatalf("WriteBlock with short buffer error = %v, want ErrBufferTooSmall", err)
	}
}

func TestOpenWritableAutoDetectsPlainImage(t *testing.T) {
	path := writeFile(t, "plain.img", buildPlainImage([][]byte{blockFilledWith(1)}))

	c, err := OpenWritable(path)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer c.Close()

	if _, ok := c.(*writablePlainImage); !ok {
		t.Fatalf("OpenWritable auto-detected %T, want *writablePlainImage", c)
	}
}

func TestOpenWritableRejectsRawCD(t *testing.T) {
	blocks := make([][]byte, blocksPerSector)
	for i := range blocks {
		blocks[i] = blockFilledWith(byte(i))
	}
	path := writeFile(t, "raw.img", buildRawCDImage(blocks))

	if _, err := OpenWritable(path); err == nil {
		t.Fatal("OpenWritable on raw CD-ROM image: want error, got nil")
	}
}

func TestRawCDImageDoesNotImplementWritableContainer(t *testing.T) {
	var c Container = &rawCDImage{}
	if _, ok := c.(WritableContainer); ok {
		t.Fatal("*rawCDImage unexpectedly implements WritableContainer")
	}
}

func TestCreateProducesZeroFilledContainerOfExactSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.img")

	const wantBlocks = 4
	c, err := Create(path, wantBlocks)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer c.Close()

	if got := c.Blocks(); got != wantBlocks {
		t.Fatalf("Blocks() = %d, want %d", got, wantBlocks)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Size(), int64(wantBlocks*BlockSize); got != want {
		t.Fatalf("file size = %d, want %d", got, want)
	}

	buf := make([]byte, BlockSize)
	for i := uint32(0); i < wantBlocks; i++ {
		if err := c.ReadBlock(i, buf); err != nil {
			t.Fatalf("ReadBlock(%d): %v", i, err)
		}
		for _, b := range buf {
			if b != 0 {
				t.Fatalf("block %d of a freshly Created container is not zero-filled", i)
			}
		}
	}
}

func TestCreateThenWriteBlockRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.img")

	c, err := Create(path, 2)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer c.Close()

	want := blockFilledWith(0x42)
	if err := c.WriteBlock(1, want); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}

	got := make([]byte, BlockSize)
	if err := c.ReadBlock(1, got); err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("ReadBlock after WriteBlock returned wrong data")
	}
}

func TestCreateTruncatesExistingFile(t *testing.T) {
	// Create on a path that already holds a file with different (larger)
	// contents than the new size should truncate it, not append to or
	// merge with the old contents.
	path := writeFile(t, "existing.img", buildPlainImage([][]byte{
		blockFilledWith(0xFF), blockFilledWith(0xFF), blockFilledWith(0xFF),
	}))

	c, err := Create(path, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer c.Close()

	if got, want := c.Blocks(), uint32(1); got != want {
		t.Fatalf("Blocks() = %d, want %d", got, want)
	}

	buf := make([]byte, BlockSize)
	if err := c.ReadBlock(0, buf); err != nil {
		t.Fatalf("ReadBlock(0): %v", err)
	}
	for _, b := range buf {
		if b != 0 {
			t.Fatal("Create did not zero the truncated file's remaining block")
		}
	}
}

func TestCreateRejectsZeroBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.img")
	if _, err := Create(path, 0); err == nil {
		t.Fatal("Create(path, 0): want error, got nil")
	}
}
