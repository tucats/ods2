package volume

import (
	"testing"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// TestMemoryImageVolumeOutlivesDismount is a RAM disk's whole life over a
// diskimage.MemoryImage: initialized through one view, mounted through
// another, given a directory, dismounted (which closes that view), and
// mounted again through a third view, where the directory is still there.
// The image stays sparse throughout: it holds only the blocks the volume
// wrote, far less than its size.
func TestMemoryImageVolumeOutlivesDismount(t *testing.T) {
	const blocks = 20000

	image, err := diskimage.NewMemory(blocks)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}

	defer image.Release()

	if err := Initialize(image.View(), InitializeOptions{Label: "RAMDISK"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	vol, err := Mount(image.View())
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	dev := vol.Devices[0]

	bm, err := dev.Bitmap()
	if err != nil {
		t.Fatalf("Bitmap: %v", err)
	}

	ib, err := dev.IndexBitmap()
	if err != nil {
		t.Fatalf("IndexBitmap: %v", err)
	}

	mfd, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory(MFD): %v", err)
	}

	if _, err := vol.CreateDirectory(mfd, "WORK.DIR", DirectoryOptions{}, bm, ib); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}

	if err := vol.Dismount(); err != nil {
		t.Fatalf("Dismount: %v", err)
	}

	if image.Resident() == 0 {
		t.Fatal("the image holds nothing after the dismount")
	}

	again, err := Mount(image.View())
	if err != nil {
		t.Fatalf("Mount again: %v", err)
	}

	if got := again.Devices[0].Home.VolumeName; got != "RAMDISK" {
		t.Errorf("VolumeName after remounting = %q, want RAMDISK", got)
	}

	mfd, err = again.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		t.Fatalf("OpenDirectory(MFD) after remounting: %v", err)
	}

	entries, err := mfd.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	found := false

	for _, e := range entries {
		if e.Name == "WORK.DIR" {
			found = true
		}
	}

	if !found {
		t.Error("WORK.DIR is gone after a dismount and a mount")
	}

	if err := again.Dismount(); err != nil {
		t.Fatalf("second Dismount: %v", err)
	}

	if got, limit := image.Resident(), int64(blocks*diskimage.BlockSize/4); got > limit {
		t.Errorf("the image holds %d bytes, more than a quarter of its %d-byte size", got, blocks*diskimage.BlockSize)
	}
}
