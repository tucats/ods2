package ondisk

import (
	"encoding/binary"
	"fmt"
)

// UicSize is the number of bytes a Uic occupies on disk.
const UicSize = 4

// Uic is a VMS User Identification Code: the (group, member) pair that
// identifies an account, similar in spirit to a Unix (uid, gid) pair. It
// appears in a volume's home block (the volume's owner) and in a file
// header (the file's owner), and is used to evaluate protection checks —
// though this project, being read-only, never needs to enforce those
// checks itself; it only decodes and reports the UIC values it finds.
type Uic struct {
	Member uint16
	Group  uint16
}

// String renders a Uic the way VMS conventionally displays one: as
// "[group,member]" with both numbers in octal, e.g. "[360,4]".
func (u Uic) String() string {
	return fmt.Sprintf("[%o,%o]", u.Group, u.Member)
}

// DecodeUic decodes a Uic from its 4-byte on-disk representation. b must be
// at least UicSize bytes long; only the first UicSize bytes are read. Note
// that the on-disk field order is member-then-group, not group-then-member.
func DecodeUic(b []byte) (Uic, error) {
	if len(b) < UicSize {
		return Uic{}, fmt.Errorf("ondisk: Uic requires %d bytes, got %d", UicSize, len(b))
	}
	return Uic{
		Member: binary.LittleEndian.Uint16(b[0:2]),
		Group:  binary.LittleEndian.Uint16(b[2:4]),
	}, nil
}
