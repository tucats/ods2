package session

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{Name: "mount", MinAbbrev: 3, MinArgs: 2, MaxArgs: 2, Qualifiers: []string{"write"}, Run: cmdMount},
		Command{Name: "dismount", MinAbbrev: 3, MinArgs: 1, MaxArgs: 1, Run: cmdDismount},
	)
}

// cmdMount implements `mount device[,device...] container[,container...]
// [/write]`. device is the VMS-style name the volume is mounted under
// (e.g. "DUA0"); container is the host file path to the disk image or
// CD-ROM sector dump backing it (an ISO, a plain block dump, or a raw
// sector dump — both are detected automatically). The two are always
// given separately — unlike the reference implementation, which lets a
// single MOUNT device name double as an actual VMS device that already
// knows its own backing media, this port has no such device registry, so
// the container path must always be spelled out.
//
// Multiple comma-separated names mount a volume set (several member disks
// presented as one logical volume), in the order given (matching VMS's
// own MOUNT command); device and container must list the same number of
// entries, paired up positionally.
//
// Without /write (the default), every container is opened read-only
// (diskimage.Open): the resulting volume can be read from, but any
// write-path operation on it (CreateFile, WriteBlock, ...) fails, since
// those check for a diskimage.WritableContainer underneath — see
// volume.File.OpenForWrite. With /write, every container is instead
// opened via diskimage.OpenWritable, which returns a WritableContainer
// that satisfies that check. A container whose backing image can't be
// written to at all — a raw CD-ROM sector dump; see WritableContainer's
// doc comment for why — fails right here with a clear error, rather than
// mounting successfully and only failing later, confusingly, on the first
// actual write attempt.
func cmdMount(s *Session, args []string, quals Qualifiers) error {
	deviceNames := splitDeviceList(args[0])
	containerPaths := splitDeviceList(args[1])

	// If the device name is singular, but the container list has multiple
	// items, expand the device list to match. If the device name already
	// has a trailing unit number (like DUA3) then 3 is the first unit number
	// in the generated list, followed by DUA4, DUA5, etc.
	//
	// If the device name doesn't already include a unit number (like DUA)
	// then unit numbers are synthesized starting at zero.
	if len(deviceNames) == 1 && len(containerPaths) > 1 {
		// Figure out the base device name. If it already has a unit number
		// on it, that's our starting unit number. If no unit number is
		// given, assume 0 is the start. The unit number is the full run of
		// trailing digits, not just the last one, so "DUA10" starts at unit
		// 10, not unit 0.
		deviceBaseUnit := 0
		deviceBaseName := strings.TrimSpace(deviceNames[0])

		digitsAt := len(deviceBaseName)
		for digitsAt > 0 && unicode.IsDigit(rune(deviceBaseName[digitsAt-1])) {
			digitsAt--
		}

		if digitsAt < len(deviceBaseName) {
			deviceBaseUnit, _ = strconv.Atoi(deviceBaseName[digitsAt:])
			deviceBaseName = deviceBaseName[:digitsAt]
		}

		synthesized := make([]string, len(containerPaths))
		for deviceIndex := range len(containerPaths) {
			synthesized[deviceIndex] = fmt.Sprintf("%s%d", deviceBaseName, deviceBaseUnit+deviceIndex)
		}
		deviceNames = synthesized
	}

	if len(containerPaths) != len(deviceNames) {
		return fmt.Errorf("mount: %d device name(s) but %d container name(s); give one container per device", len(deviceNames), len(containerPaths))
	}

	writable := quals.Has("write")

	containers := make([]diskimage.Container, 0, len(containerPaths))
	for _, path := range containerPaths {
		var (
			c   diskimage.Container
			err error
		)
		if writable {
			c, err = diskimage.OpenWritable(path)
		} else {
			c, err = diskimage.Open(path)
		}
		if err != nil {
			for _, opened := range containers {
				_ = opened.Close()
			}
			return fmt.Errorf("mount: opening %s: %w", path, err)
		}
		containers = append(containers, c)
	}

	return mountContainers(s, deviceNames, containers)
}

// splitDeviceList splits a comma-separated device list into trimmed
// device names.
func splitDeviceList(s string) []string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// mountContainers finishes mounting: given already-open containers, one
// per device name in volume-set order, it mounts them as a single Volume
// and registers the result in the session (keyed by the first device
// name, upper-cased). It's split out from cmdMount so tests can exercise
// the session-registration logic — including the auto-default-directory
// behavior below — against in-memory containers, without needing real
// files on disk.
//
// The first successful mount also becomes the session's default device,
// with directory [000000], if no default has been set yet — mirroring
// the reference implementation's own "first mount sets the default"
// convenience.
func mountContainers(s *Session, deviceNames []string, containers []diskimage.Container) error {
	vol, err := volume.Mount(containers...)
	if err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	key := strings.ToUpper(strings.TrimSuffix(deviceNames[0], ":"))
	s.Volumes[key] = vol

	for i, dev := range vol.Devices {
		deviceName := deviceNames[0]
		if i < len(deviceNames) {
			deviceName = deviceNames[i]
		}
		fmt.Fprintf(s.Stdout, "%%MOUNT-I-MOUNTED, Volume %s mounted on %s\n",
			strings.TrimSpace(dev.Home.VolumeName), deviceName)
	}

	if s.Default.Device == "" {
		s.Default = filespec.Spec{Device: key}
	}

	return nil
}

// cmdDismount implements `dismount device`, flushing any pending bitmap
// writes (volume.Volume.Dismount — a no-op if the volume was never mounted
// /write or never actually written to), releasing the volume's underlying
// containers (closing their open file handles), and forgetting it.
func cmdDismount(s *Session, args []string, quals Qualifiers) error {
	key := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(args[0]), ":"))

	vol, ok := s.Volumes[key]
	if !ok {
		return fmt.Errorf("dismount: %s is not mounted", key)
	}

	if err := vol.Dismount(); err != nil {
		return fmt.Errorf("dismount: %w", err)
	}
	delete(s.Volumes, key)

	return nil
}
