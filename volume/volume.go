package volume

import (
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// homeBlockScanLimit bounds how many candidate logical blocks Mount will
// examine while searching for a device's volume home block: LBNs 1 through
// homeBlockScanLimit are tried, in order. LBN 0 is deliberately never
// tried — by convention it holds a boot block, not volume structure data.
// This range matches the reference implementation's own search.
const homeBlockScanLimit = 100

// Device is one member disk of a mounted Volume. An ordinary, single-disk
// volume has exactly one Device; a "volume set" (several disks presented
// to VMS as one logical volume) has one Device per member disk.
type Device struct {
	Container diskimage.Container
	Home      ondisk.HomeBlock

	// Rvn is this device's relative volume number: its 1-based position
	// within the volume set. An ordinary single-disk volume's one Device
	// has Rvn 1.
	Rvn uint8

	// IndexFile is this device's own index file, INDEXF.SYS — the file
	// whose data (a sequence of file headers, one per file number) is
	// consulted to locate every other file's header. Mount bootstraps it
	// once per device (see mountDevice); every subsequent file lookup on
	// this device goes through it.
	IndexFile *File

	// bitmap and indexBitmap are this device's storage- and index-file
	// bitmap caches (see docs/PHASE-02.md's "Two bitmaps, not one"), lazily
	// opened by the Bitmap/IndexBitmap methods (dismount.go) the first time
	// a write-path operation asks for one, and left nil for the rest of the
	// mount session otherwise — in particular, a device that was only ever
	// read from (or mounted read-only in the first place) never populates
	// either field. Dismount uses this to know which caches actually need
	// flushing: nil here means "never opened, so nothing in memory could
	// possibly be dirty," not "opened but clean."
	bitmap      *Bitmap
	indexBitmap *IndexBitmap
}

// Volume is a mounted ODS-2 volume, spanning one or more member Devices.
type Volume struct {
	Devices []*Device
}

// Mount opens an ODS-2 volume by locating and validating each container's
// volume home block. Passing a single container mounts an ordinary
// volume; passing several mounts a "volume set" spanning multiple member
// disks, which VMS presents to users as a single logical volume with
// files potentially spread across any of its members.
//
// containers must be given in volume-set order: the first is relative
// volume 1, the second relative volume 2, and so on — this matches the
// order VMS's own MOUNT command expects a device list to be given in.
func Mount(containers ...diskimage.Container) (*Volume, error) {
	if len(containers) == 0 {
		return nil, fmt.Errorf("volume: Mount requires at least one container")
	}

	vol := &Volume{}
	for i, c := range containers {
		rvn := uint8(i + 1)

		home, err := findHomeBlock(c)
		if err != nil {
			return nil, fmt.Errorf("volume: mounting device %d: %w", i, err)
		}

		// Cross-check the relative volume number the home block itself
		// claims against this device's actual position in the container
		// list we were asked to mount, the same sanity check the
		// reference mount() performs. A lone, single-device volume is
		// permitted to record its relative volume number as either 0 or
		// 1 (both mean "not part of a multi-disk set"); every other
		// device's recorded number must match its position exactly, or
		// the caller has almost certainly listed the volume set's member
		// disks in the wrong order.
		if home.RelativeVolumeNumber != uint16(rvn) {
			isToleratedSingleVolumeCase := i == 0 && home.RelativeVolumeNumber <= 1
			if !isToleratedSingleVolumeCase {
				return nil, fmt.Errorf(
					"volume: device %d: home block's relative volume number is %d, expected %d (check the container order)",
					i, home.RelativeVolumeNumber, rvn)
			}
		}

		dev := &Device{
			Container: c,
			Home:      home,
			Rvn:       rvn,
		}

		if err := bootstrapIndexFile(dev); err != nil {
			return nil, fmt.Errorf("volume: bootstrapping index file for device %d: %w", i, err)
		}

		vol.Devices = append(vol.Devices, dev)
	}

	return vol, nil
}

// bootstrapIndexFile reads and validates a device's own index file header
// and resolves its complete Extents, storing the result on dev.IndexFile.
//
// Every other file's header is located by treating INDEXF.SYS as an
// ordinary file and consulting ITS retrieval pointers — but that requires
// already having INDEXF.SYS's own header and retrieval pointers in hand,
// which obviously can't be obtained by looking them up in INDEXF.SYS
// (there is nothing else to consult yet). The on-disk format breaks this
// chicken-and-egg problem with a guarantee: INDEXF.SYS's own header is
// always located at a fixed, directly-computable absolute logical block,
// immediately following the volume's index bitmap (the region home block
// fields IndexBitmapLBN/IndexBitmapSize describe). Reading that one block
// directly — with no retrieval-pointer indirection at all — is enough to
// get started; from there, buildFile can resolve the rest of INDEXF.SYS's
// own extents (and any further file's header) the ordinary way.
func bootstrapIndexFile(dev *Device) error {
	lbn := dev.Home.IndexBitmapLBN + uint32(dev.Home.IndexBitmapSize)

	buf := make([]byte, ondisk.BlockSize)
	if err := dev.Container.ReadBlock(lbn, buf); err != nil {
		return fmt.Errorf("reading index file header at LBN %d: %w", lbn, err)
	}

	header, err := ondisk.DecodeFileHeader(buf)
	if err != nil {
		return fmt.Errorf("decoding index file header at LBN %d: %w", lbn, err)
	}
	if header.Fid.Number() != ondisk.IndexFileFid.Number() || header.Fid.Seq != ondisk.IndexFileFid.Seq {
		return fmt.Errorf("index file header at LBN %d has unexpected file ID %v", lbn, header.Fid)
	}

	indexFile, err := buildFile(dev, header)
	if err != nil {
		return fmt.Errorf("resolving index file's own data extents: %w", err)
	}

	dev.IndexFile = indexFile
	return nil
}

// deviceByRvn returns the mounted Device with the given relative volume
// number. Rvn 0 is treated the same as Rvn 1: several on-disk fields
// (directory backlinks, for instance) conventionally leave a reference's
// relative volume number as 0 to mean "the volume's first member", rather
// than always writing 1 explicitly.
func (vol *Volume) deviceByRvn(rvn uint8) (*Device, error) {
	target := rvn
	if target == 0 {
		target = 1
	}
	for _, d := range vol.Devices {
		if d.Rvn == target {
			return d, nil
		}
	}
	return nil, fmt.Errorf("volume: no device with relative volume number %d is mounted", target)
}

// OpenFID opens the file identified by fid.
func (vol *Volume) OpenFID(fid ondisk.Fid) (*File, error) {
	dev, err := vol.deviceByRvn(fid.Rvn)
	if err != nil {
		return nil, fmt.Errorf("volume: opening file %v: %w", fid, err)
	}

	header, err := readFileHeaderViaIndex(dev, dev.IndexFile.Extents, fid)
	if err != nil {
		return nil, fmt.Errorf("volume: opening file %v: %w", fid, err)
	}

	f, err := buildFile(dev, header)
	if err != nil {
		return nil, fmt.Errorf("volume: opening file %v: %w", fid, err)
	}
	return f, nil
}

// findHomeBlock searches a container's first homeBlockScanLimit logical
// blocks for a valid, self-consistent ODS-2 volume home block.
//
// "Self-consistent" means the candidate block's own HomeLBN field must
// equal the LBN it was actually read from. Combined with
// ondisk.DecodeHomeBlock's format-identifier and checksum validation, this
// confirms a candidate block really is THE home block, rather than, say,
// unrelated data that happens to pass the checksum by pure coincidence
// (vanishingly unlikely on its own, but this check is nearly free and the
// reference implementation performs it too).
func findHomeBlock(c diskimage.Container) (ondisk.HomeBlock, error) {
	buf := make([]byte, ondisk.BlockSize)

	limit := uint32(homeBlockScanLimit)
	if c.Blocks() < limit {
		limit = c.Blocks()
	}

	var lastErr error
	for lbn := uint32(1); lbn <= limit; lbn++ {
		if err := c.ReadBlock(lbn, buf); err != nil {
			lastErr = err
			continue
		}

		home, err := ondisk.DecodeHomeBlock(buf)
		if err != nil {
			lastErr = err
			continue
		}

		if home.HomeLBN != lbn {
			lastErr = fmt.Errorf("home block at LBN %d reports its own location as LBN %d", lbn, home.HomeLBN)
			continue
		}

		return home, nil
	}

	if lastErr != nil {
		return ondisk.HomeBlock{}, fmt.Errorf("no valid ODS-2 home block found in the first %d blocks: %w", limit, lastErr)
	}
	return ondisk.HomeBlock{}, fmt.Errorf("no valid ODS-2 home block found in the first %d blocks", limit)
}
