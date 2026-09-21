package volume

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// TestInitializeProducesMountableVolume is this subtask's primary
// acceptance test, matching docs/PHASE-02.md subtask 12's own test plan:
// Initialize a fresh container, Mount it, and confirm every reserved file
// is exactly where the master file directory says it is -- then create a
// file on the freshly initialized volume (subtask 10), dismount it
// (subtask 11), and confirm a completely independent re-mount reads it
// back correctly.
func TestInitializeProducesMountableVolume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.dsk")
	c, err := diskimage.Create(path, 400)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := Initialize(c, InitializeOptions{Label: "TESTVOL"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	dev := vol.Devices[0]
	if got, want := dev.Home.VolumeName, "TESTVOL"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
	if got, want := dev.Home.ReservedFiles, uint16(ondisk.ReservedFileCount); got != want {
		t.Errorf("ReservedFiles = %d, want %d", got, want)
	}

	dir, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory(MasterFileDirectoryFid): %v", err)
	}
	entries, err := dir.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != len(reservedFiles) {
		t.Fatalf("master file directory has %d entries, want %d", len(entries), len(reservedFiles))
	}

	byName := make(map[string]ondisk.DirEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	for _, spec := range reservedFiles {
		e, ok := byName[spec.name]
		if !ok {
			t.Errorf("master file directory is missing %s", spec.name)
			continue
		}
		if e.Fid.Number() != spec.fid.Number() {
			t.Errorf("%s has file number %d, want %d", spec.name, e.Fid.Number(), spec.fid.Number())
		}

		f, err := vol.OpenFID(e.Fid)
		if err != nil {
			t.Errorf("OpenFID(%s): %v", spec.name, err)
			continue
		}
		if f.Header.Backlink.Number() != ondisk.MasterFileDirectoryFid.Number() {
			t.Errorf("%s Backlink = %v, want the master file directory", spec.name, f.Header.Backlink)
		}
	}

	// Create a file on the freshly initialized volume, using the same
	// write-path machinery (subtask 10) any other mounted-/WRITE volume
	// would.
	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}
	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap: %v", err)
	}
	f, err := vol.CreateFile(dir, "HELLO.TXT", ondisk.RecAttr{Format: ondisk.RecordFormatFixed}, bm, ib)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	content := bytes.Repeat([]byte{0x42}, ondisk.BlockSize)
	if err := f.WriteBlock(1, content); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := vol.Dismount(); err != nil {
		t.Fatalf("Dismount: %v", err)
	}

	// Re-open completely independently -- a fresh container, a fresh
	// Mount -- so nothing here could be sharing in-memory state with the
	// Volume above; if HELLO.TXT reads back correctly now, it's because
	// Initialize+CreateFile+Dismount actually wrote a self-consistent
	// volume to disk.
	reopened, err := diskimage.Open(path)
	if err != nil {
		t.Fatalf("diskimage.Open (reopened): %v", err)
	}
	defer func() { _ = reopened.Close() }()

	vol2, err := Mount(reopened)
	if err != nil {
		t.Fatalf("Mount (reopened): %v", err)
	}
	dir2, err := vol2.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory (reopened): %v", err)
	}
	entry, err := dir2.Lookup("HELLO.TXT", 0)
	if err != nil {
		t.Fatalf("Lookup(HELLO.TXT) (reopened): %v", err)
	}
	f2, err := vol2.OpenFID(entry.Fid)
	if err != nil {
		t.Fatalf("OpenFID(HELLO.TXT) (reopened): %v", err)
	}
	buf := make([]byte, ondisk.BlockSize)
	if err := f2.ReadBlock(1, buf); err != nil {
		t.Fatalf("ReadBlock(1) (reopened): %v", err)
	}
	if !bytes.Equal(buf, content) {
		t.Error("HELLO.TXT's content doesn't round-trip through Initialize+CreateFile+Dismount+Mount")
	}
}

// TestInitializeAppliesDefaults confirms every InitializeOptions field's
// documented default applies when left at its zero value.
func TestInitializeAppliesDefaults(t *testing.T) {
	c, err := diskimage.Create(filepath.Join(t.TempDir(), "defaults.dsk"), 300)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := Initialize(c, InitializeOptions{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	home := vol.Devices[0].Home

	if home.VolumeName != "NONAME" {
		t.Errorf("VolumeName = %q, want %q", home.VolumeName, "NONAME")
	}
	if home.VolumeOwner != (ondisk.Uic{Group: 1, Member: 1}) {
		t.Errorf("VolumeOwner = %v, want [1,1]", home.VolumeOwner)
	}
	if home.FileProtection != defaultFileProtection {
		t.Errorf("FileProtection = %#x, want %#x", home.FileProtection, uint16(defaultFileProtection))
	}
	if home.ClusterSize != 1 {
		t.Errorf("ClusterSize = %d, want 1", home.ClusterSize)
	}
	if home.MaxFiles != defaultMaxFiles(c.Blocks()) {
		t.Errorf("MaxFiles = %d, want %d", home.MaxFiles, defaultMaxFiles(c.Blocks()))
	}
}

// TestInitializeHonorsOptions confirms every InitializeOptions field, when
// given explicitly, actually reaches the resulting home block -- the
// mirror image of TestInitializeAppliesDefaults.
func TestInitializeHonorsOptions(t *testing.T) {
	c, err := diskimage.Create(filepath.Join(t.TempDir(), "options.dsk"), 400)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	opts := InitializeOptions{
		Label:          "MYVOL",
		Owner:          ondisk.Uic{Group: 0o10, Member: 4},
		FileProtection: 0xFF00,
		ClusterSize:    2,
		MaxFiles:       64,
	}
	if err := Initialize(c, opts); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	home := vol.Devices[0].Home

	if home.VolumeName != opts.Label {
		t.Errorf("VolumeName = %q, want %q", home.VolumeName, opts.Label)
	}
	if home.VolumeOwner != opts.Owner {
		t.Errorf("VolumeOwner = %v, want %v", home.VolumeOwner, opts.Owner)
	}
	if home.FileProtection != opts.FileProtection {
		t.Errorf("FileProtection = %#x, want %#x", home.FileProtection, opts.FileProtection)
	}
	if home.ClusterSize != opts.ClusterSize {
		t.Errorf("ClusterSize = %d, want %d", home.ClusterSize, opts.ClusterSize)
	}
	if home.MaxFiles != opts.MaxFiles {
		t.Errorf("MaxFiles = %d, want %d", home.MaxFiles, opts.MaxFiles)
	}
}

// TestInitializeRejectsUndersizedVolume confirms Initialize fails clearly,
// rather than writing a truncated/inconsistent volume, when the container
// is too small for even the minimal reserved layout to fit.
func TestInitializeRejectsUndersizedVolume(t *testing.T) {
	c, err := diskimage.Create(filepath.Join(t.TempDir(), "tiny.dsk"), 5)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := Initialize(c, InitializeOptions{}); err == nil {
		t.Error("Initialize on a 5-block volume: want an error, got nil")
	}
}

// TestInitializeRejectsMaxFilesBelowReservedCount confirms an explicit
// MaxFiles too small to even hold the 9 reserved files is rejected with a
// clear error rather than silently corrupting the reserved-file layout.
func TestInitializeRejectsMaxFilesBelowReservedCount(t *testing.T) {
	c, err := diskimage.Create(filepath.Join(t.TempDir(), "toofew.dsk"), 300)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := Initialize(c, InitializeOptions{MaxFiles: 3}); err == nil {
		t.Error("Initialize with MaxFiles=3: want an error, got nil")
	}
}

// TestInitializeMarksReservedSpaceAllocated confirms the reserved system
// area (home block, INDEXF.SYS, BITMAP.SYS, 000000.DIR) is actually marked
// allocated in the storage bitmap, and every reserved header slot in the
// index-file bitmap -- docs/PHASE-02.md subtask 12 step 5 -- by checking
// the raw bits directly (this test is in-package specifically to reach
// them), the same way dismount_test.go's own bitmap-content checks do.
func TestInitializeMarksReservedSpaceAllocated(t *testing.T) {
	c, err := diskimage.Create(filepath.Join(t.TempDir(), "marks.dsk"), 300)
	if err != nil {
		t.Fatalf("diskimage.Create: %v", err)
	}
	defer func() { _ = c.Close() }()

	opts := InitializeOptions{ClusterSize: 1, MaxFiles: 32}
	if err := Initialize(c, opts); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	layout, err := computeLayout(c.Blocks(), opts.ClusterSize, opts.MaxFiles)
	if err != nil {
		t.Fatalf("computeLayout: %v", err)
	}

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	dev := vol.Devices[0]

	bm, err := OpenBitmap(dev)
	if err != nil {
		t.Fatalf("OpenBitmap: %v", err)
	}
	for cluster := uint32(0); cluster < layout.reservedClusters; cluster++ {
		if ondisk.BitmapTest(bm.bits, cluster) {
			t.Errorf("cluster %d (within the reserved prefix) reads as free, want allocated", cluster)
		}
	}
	// One cluster past the reserved prefix should be free -- otherwise
	// nothing on this small a volume could ever be allocated to a new
	// file.
	if !ondisk.BitmapTest(bm.bits, layout.reservedClusters) {
		t.Errorf("cluster %d (just past the reserved prefix) reads as allocated, want free", layout.reservedClusters)
	}

	ib, err := OpenIndexBitmap(dev)
	if err != nil {
		t.Fatalf("OpenIndexBitmap: %v", err)
	}
	for slot := uint32(0); slot < ondisk.ReservedFileCount; slot++ {
		if !ondisk.BitmapTest(ib.bits, slot) {
			t.Errorf("header slot %d (file number %d, reserved) reads as free, want in-use", slot, slot+1)
		}
	}
	if ondisk.BitmapTest(ib.bits, ondisk.ReservedFileCount) {
		t.Errorf("header slot %d (file number %d, the first non-reserved one) reads as in-use, want free",
			ondisk.ReservedFileCount, ondisk.ReservedFileCount+1)
	}
}
