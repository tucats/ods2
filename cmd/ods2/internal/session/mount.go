package session

import (
	"fmt"
	"strings"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{Name: "mount", MinAbbrev: 3, MinArgs: 1, MaxArgs: 2, Qualifiers: []string{"write"}, Run: cmdMount},
		Command{Name: "dismount", MinAbbrev: 3, MinArgs: 1, MaxArgs: 1, Run: cmdDismount},
	)
}

// cmdMount implements `mount device[,device...] [label[,label...]]`.
// Multiple comma-separated device names mount a volume set, in the order
// given (matching VMS's own MOUNT command); labels are accepted for
// compatibility but, as in the reference implementation, are not
// validated against the volume's actual label.
//
// The /write qualifier is accepted for command-line compatibility but has
// no effect: this project is read-only (see the project README), so
// there is no write mode to enable.
func cmdMount(s *Session, args []string, quals Qualifiers) error {
	deviceNames := splitDeviceList(args[0])

	containers := make([]diskimage.Container, 0, len(deviceNames))
	for _, name := range deviceNames {
		c, err := diskimage.Open(strings.TrimSuffix(name, ":"))
		if err != nil {
			for _, opened := range containers {
				_ = opened.Close()
			}
			return fmt.Errorf("mount: opening %s: %w", name, err)
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

// cmdDismount implements `dismount device`, releasing the volume's
// underlying containers (closing their open file handles) and forgetting
// it.
func cmdDismount(s *Session, args []string, quals Qualifiers) error {
	key := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(args[0]), ":"))

	vol, ok := s.Volumes[key]
	if !ok {
		return fmt.Errorf("dismount: %s is not mounted", key)
	}

	for _, dev := range vol.Devices {
		_ = dev.Container.Close()
	}
	delete(s.Volumes, key)

	return nil
}
