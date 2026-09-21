package session

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "initialize",
			MinAbbrev:  4,
			MinArgs:    2,
			MaxArgs:    3,
			Qualifiers: []string{"cluster"},
			Run:        cmdInitialize,
		},
	)
}

// cmdInitialize implements `initialize path size-in-blocks [label]
// [/cluster=n]`: creates a new, zero-filled host file of size-in-blocks
// 512-byte blocks (via diskimage.Create) and builds a minimal but valid,
// freshly formatted ODS-2 volume on it (volume.Initialize).
//
// Unlike every other command in this package, INITIALIZE doesn't operate
// on an already-mounted volume -- it creates one from nothing -- so,
// matching real VMS's own INITIALIZE (which formats a device without
// mounting it), this command doesn't mount the volume it just built; a
// following `MOUNT path` picks it up like any other image.
func cmdInitialize(s *Session, args []string, quals Qualifiers) error {
	path := args[0]

	blocks, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil {
		return fmt.Errorf("initialize: invalid size %q: %w", args[1], err)
	}

	opts := volume.InitializeOptions{}
	if len(args) > 2 {
		opts.Label = strings.ToUpper(args[2])
	}
	if quals.Has("cluster") {
		clusterSize, err := strconv.ParseUint(quals.Value("cluster"), 10, 16)
		if err != nil {
			return fmt.Errorf("initialize: invalid /CLUSTER value %q: %w", quals.Value("cluster"), err)
		}
		opts.ClusterSize = uint16(clusterSize)
	}

	c, err := diskimage.Create(path, uint32(blocks))
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	defer func() { _ = c.Close() }()

	if err := volume.Initialize(c, opts); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	label := opts.Label
	if label == "" {
		label = "NONAME"
	}
	fmt.Fprintf(s.Stdout, "%%INITIALIZE-I-DONE, Volume %s initialized on %s (%d block%s, %d reserved file%s)\n",
		label, path, blocks, plural(blocks), ondisk.ReservedFileCount, plural(ondisk.ReservedFileCount))
	return nil
}

// plural returns "s" unless n is exactly 1, for the trivial pluralization
// cmdInitialize's own confirmation message needs.
func plural(n uint64) string {
	if n == 1 {
		return ""
	}
	return "s"
}
