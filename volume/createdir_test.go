package volume

import (
	"errors"
	"strings"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// createDirFixture is the writable volume (writeheader_test.go) with a
// parent directory, PARENT.DIR, to create directories in, and the bitmap
// caches CreateDirectory needs.
func createDirFixture(t *testing.T) (*Volume, *Directory, *Bitmap, *IndexBitmap, diskimage.WritableContainer) {
	t.Helper()

	dev, container := newWritableHeaderTestVolume(t)
	setIndexBitmapBits(t, container, []uint32{1, 2, 3})
	installWideTestBitmap(t, container)

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}

	parent := newWritableTestDirectory(t, dev, ib, "PARENT.DIR")

	return &Volume{Devices: []*Device{dev}}, parent, bm, ib, container
}

// TestCreateDirectoryLayoutMatchesVMS checks the new directory's header
// against what VMS 7.3's CREATE/DIRECTORY writes (read from directories it
// made on govax's Phase 33 oracle volume): directory and contiguous,
// variable-length no-span records of at most 512 bytes, one block
// allocated and used, IDENT revision 0, and version 1.
func TestCreateDirectoryLayoutMatchesVMS(t *testing.T) {
	vol, parent, bm, ib, container := createDirFixture(t)

	sub, err := vol.CreateDirectory(parent, "SUB.DIR", DirectoryOptions{}, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}

	h := sub.Header
	if want := ondisk.FchDirectory | ondisk.FchContig; h.FileCharacteristics != want {
		t.Errorf("FileCharacteristics = %#x, want %#x", h.FileCharacteristics, want)
	}

	ra := h.RecordAttributes
	if ra.Format != ondisk.RecordFormatVariable || ra.Attributes != ondisk.AttrNoSpan ||
		ra.RecordSize != 512 || ra.MaxRecordSize != 512 {
		t.Errorf("record attributes = %+v, want VAR, NOSPAN, 512, 512", ra)
	}

	if ra.HighestBlock != 1 || ra.EndOfFileBlock != 2 || ra.FirstFreeByte != 0 || h.HighWaterMark != 2 {
		t.Errorf("HIBLK/EFBLK/FFB/HWM = %d/%d/%d/%d, want 1/2/0/2",
			ra.HighestBlock, ra.EndOfFileBlock, ra.FirstFreeByte, h.HighWaterMark)
	}

	if h.Backlink != parent.Header.Fid {
		t.Errorf("Backlink = %v, want the parent, %v", h.Backlink, parent.Header.Fid)
	}

	// The volume's defaults, with no Owner or Protection given.
	if h.Owner != (ondisk.Uic{Group: 0o10, Member: 4}) || h.FileProtection != 0xFF00 {
		t.Errorf("owner/protection = %v/%#x, want the home block's [10,4]/0xFF00", h.Owner, h.FileProtection)
	}

	ident, err := h.Ident()
	if err != nil {
		t.Fatalf("Ident: %v", err)
	}

	if ident.Revision != 0 {
		t.Errorf("IDENT revision = %d, want 0", ident.Revision)
	}

	if got := strings.TrimSpace(ident.Filename + ident.FilenameExtension); got != "SUB.DIR;1" {
		t.Errorf("IDENT name = %q, want SUB.DIR;1", got)
	}

	// Block 1 is an empty directory block: the end-of-data marker, then
	// zeros.
	lbn, err := resolveExtentLBN(sub.Extents, 1)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, ondisk.BlockSize)
	if err := container.ReadBlock(lbn, buf); err != nil {
		t.Fatal(err)
	}

	if buf[0] != 0xFF || buf[1] != 0xFF || strings.Trim(string(buf[2:]), "\x00") != "" {
		t.Errorf("first block starts % x, want ff ff then zeros", buf[:8])
	}

	entry, err := parent.Lookup("SUB.DIR", 0)
	if err != nil {
		t.Fatalf("Lookup in parent: %v", err)
	}

	if entry.Version != 1 || entry.Fid != h.Fid {
		t.Errorf("parent's entry = %+v, want SUB.DIR;1 -> %v", entry, h.Fid)
	}

	if entries, err := sub.List(); err != nil || len(entries) != 0 {
		t.Errorf("new directory's List() = %v, %v, want empty", entries, err)
	}
}

func TestCreateDirectoryOwnerProtectionAndAllocation(t *testing.T) {
	vol, parent, bm, ib, _ := createDirFixture(t)

	owner := ondisk.Uic{Group: 0o200, Member: 0o201}
	protection := uint16(0xBA88)

	sub, err := vol.CreateDirectory(parent, "OWNED.DIR", DirectoryOptions{
		VersionLimit: 3,
		Owner:        &owner,
		Protection:   &protection,
		Allocation:   4,
	}, bm, ib)
	if err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}

	h := sub.Header
	if h.Owner != owner || h.FileProtection != protection {
		t.Errorf("owner/protection = %v/%#x, want %v/%#x", h.Owner, h.FileProtection, owner, protection)
	}

	if h.RecordAttributes.VersionLimit != 3 {
		t.Errorf("VersionLimit = %d, want 3", h.RecordAttributes.VersionLimit)
	}

	// Four blocks in one run, only the first of them used.
	if len(sub.Extents) != 1 || sub.Extents[0].Count != 4 {
		t.Errorf("extents = %+v, want one run of 4 blocks", sub.Extents)
	}

	// The whole allocation counts as written (VMS 7.3's [ALLOC], made
	// with /ALLOCATION=4, has its high-water mark at 5), and stays so as
	// entries are added.
	if ra := h.RecordAttributes; ra.HighestBlock != 4 || ra.EndOfFileBlock != 2 || h.HighWaterMark != 5 {
		t.Errorf("HIBLK/EFBLK/HWM = %d/%d/%d, want 4/2/5", ra.HighestBlock, ra.EndOfFileBlock, h.HighWaterMark)
	}

	// The directory is still usable, and keeps its single run as it fills.
	for i, name := range []string{"A.DAT", "B.DAT", "C.DAT"} {
		if err := sub.Insert(name, 1, ondisk.Fid{Num: uint16(90 + i), Seq: 1}, bm, ib); err != nil {
			t.Fatalf("Insert(%s): %v", name, err)
		}
	}

	if entries, err := sub.List(); err != nil || len(entries) != 3 {
		t.Errorf("List() = %+v, %v, want 3 entries", entries, err)
	}

	if sub.Header.HighWaterMark != 5 {
		t.Errorf("HWM after inserts = %d, want 5 still", sub.Header.HighWaterMark)
	}
}

func TestCreateDirectoryNameLength(t *testing.T) {
	vol, parent, bm, ib, _ := createDirFixture(t)

	longest := strings.Repeat("A", MaxDirectoryNameLength) + ".DIR"
	if _, err := vol.CreateDirectory(parent, longest, DirectoryOptions{}, bm, ib); err != nil {
		t.Errorf("CreateDirectory(%d-character name): %v", MaxDirectoryNameLength, err)
	}

	for _, name := range []string{strings.Repeat("B", MaxDirectoryNameLength+1) + ".DIR", ".DIR"} {
		if _, err := vol.CreateDirectory(parent, name, DirectoryOptions{}, bm, ib); !errors.Is(err, ErrDirectoryName) {
			t.Errorf("CreateDirectory(%q): err = %v, want ErrDirectoryName", name, err)
		}
	}
}

// TestCreateDirectoryFailureGivesSpaceBack: when the directory's
// allocation can't be had, nothing is left behind -- no entry in the
// parent, the header slot free again, and the disk's free space as before.
func TestCreateDirectoryFailureGivesSpaceBack(t *testing.T) {
	vol, parent, bm, ib, _ := createDirFixture(t)

	freeBefore := bm.FreeClusters()

	slotBefore, err := ib.FindFreeSlot()
	if err != nil {
		t.Fatal(err)
	}

	_, err = vol.CreateDirectory(parent, "HUGE.DIR", DirectoryOptions{Allocation: whVolumeSize}, bm, ib)
	if err == nil {
		t.Fatal("CreateDirectory with more blocks than the disk has: want an error, got none")
	}

	if _, err := parent.Lookup("HUGE.DIR", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("parent's HUGE.DIR entry: Lookup err = %v, want ErrNotFound", err)
	}

	if got := bm.FreeClusters(); got != freeBefore {
		t.Errorf("free clusters = %d, want %d as before", got, freeBefore)
	}

	if slot, err := ib.FindFreeSlot(); err != nil || slot != slotBefore {
		t.Errorf("first free header slot = %d, %v, want %d as before", slot, err, slotBefore)
	}
}

// TestCreateDirectoryEntryHasNoVersionLimit: a new directory's entry in its
// parent has no version limit, even when the parent has a default limit --
// as VMS 7.3 writes [LIMITED]'s entries for its subdirectories.
func TestCreateDirectoryEntryHasNoVersionLimit(t *testing.T) {
	vol, parent, bm, ib, _ := createDirFixture(t)

	limited, err := vol.CreateDirectory(parent, "LIMITED.DIR", DirectoryOptions{VersionLimit: 3}, bm, ib)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := vol.CreateDirectory(limited, "INHERIT.DIR", DirectoryOptions{VersionLimit: 3}, bm, ib); err != nil {
		t.Fatal(err)
	}

	entry, err := limited.Lookup("INHERIT.DIR", 0)
	if err != nil {
		t.Fatal(err)
	}

	if entry.VersionLimit != ondisk.NoVersionLimit {
		t.Errorf("INHERIT.DIR's entry version limit = %d, want none (%d)", entry.VersionLimit, ondisk.NoVersionLimit)
	}

	// An ordinary file's new name still takes the directory's default.
	if err := limited.Insert("FILE.DAT", 1, ondisk.Fid{Num: 90, Seq: 1}, bm, ib); err != nil {
		t.Fatal(err)
	}

	if entry, err := limited.Lookup("FILE.DAT", 0); err != nil || entry.VersionLimit != 3 {
		t.Errorf("FILE.DAT's entry = %+v, %v, want version limit 3", entry, err)
	}
}
