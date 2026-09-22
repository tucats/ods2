package session

import (
	"fmt"
	"strings"

	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "analyze",
			MinAbbrev:  3,
			MinArgs:    1,
			MaxArgs:    1,
			Qualifiers: []string{"disk", "repair"},
			Run:        cmdAnalyze,
		},
	)
}

// cmdAnalyze implements `analyze device /disk [/repair]`, reporting VMS's
// real command name for this, ANALYZE/DISK, in its own error text: real
// DCL glues a command's qualifiers directly onto its verb with no space
// ("ANALYZE/DISK"), but this project's own command-line parser (tokenize.go)
// recognizes a "/name" qualifier anywhere on the line regardless of
// spacing, so "ANALYZE device /DISK" works exactly the same way "ANALYZE/
// DISK device" does. /DISK is required, not optional, even though it's the
// only ANALYZE mode this project implements (unlike real VMS's ANALYZE,
// which also has RMS_FILE, OBJECT, and other unrelated structure types) --
// requiring it keeps this command's own name self-documenting, and leaves
// room for a future ANALYZE mode to be added without silently changing
// what a bare "ANALYZE device" means.
//
// device must already be mounted (see cmdMount) -- ANALYZE/DISK doesn't
// take a host path directly, matching DISMOUNT's own convention for a
// command that operates on something already mounted rather than
// mounting it itself.
//
// Without /REPAIR, this only reads: it walks every in-use file header on
// the volume, decodes the storage-bitmap allocation state their retrieval
// pointers imply, and compares that against BITMAP.SYS's actual on-disk
// bits (volume.AnalyzeDisk), printing every discrepancy found without
// changing anything. With /REPAIR, it additionally rewrites BITMAP.SYS to
// match the computed-correct state (volume.RepairDisk) -- which needs the
// volume mounted /WRITE; a device that isn't fails with a clear error from
// deep inside volume.RepairDisk itself (it tries to obtain the device's
// Bitmap cache, which requires a diskimage.WritableContainer), the same
// way every other write-path command in this package relies on that one
// check rather than a separate "is this volume writable" flag (see
// docs/PHASE-02.md subtask 14's own write-up).
//
// This project's own non-goals (docs/PHASE-02.md) scope ANALYZE/DISK, like
// the rest of Phase 2's write support, to a single-device volume --
// multi-device volume sets are rejected here with a clear error rather
// than silently analyzing (or, worse, "repairing") only the first member.
func cmdAnalyze(s *Session, args []string, quals Qualifiers) error {
	if !quals.Has("disk") {
		return fmt.Errorf("analyze: /DISK is required (only ANALYZE/DISK is supported)")
	}

	key := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(args[0]), ":"))

	vol, ok := s.Volumes[key]
	if !ok {
		return fmt.Errorf("analyze: %s is not mounted", key)
	}

	if len(vol.Devices) != 1 {
		return fmt.Errorf("analyze: %s is a %d-device volume set; ANALYZE/DISK only supports a single-device volume", key, len(vol.Devices))
	}

	dev := vol.Devices[0]
	repair := quals.Has("repair")

	var (
		report *volume.DiskReport
		err    error
	)

	if repair {
		report, err = volume.RepairDisk(dev)
	} else {
		report, err = volume.AnalyzeDisk(dev)
	}

	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	if report.Clean() {
		fmt.Fprintf(s.Stdout, "%%ANALYZE-I-CLEAN, no discrepancies found (%d cluster(s) examined)\n", report.TotalClusters)

		return nil
	}

	action := "found"
	if repair {
		action = "found and repaired"
	}

	fmt.Fprintf(s.Stdout, "%%ANALYZE-W-DISCREP, %d discrepancy(ies) %s (%d cluster(s) examined)\n",
		len(report.Discrepancies), action, report.TotalClusters)

	for _, d := range report.Discrepancies {
		fmt.Fprintf(s.Stdout, "  %s\n", d)
	}

	return nil
}
