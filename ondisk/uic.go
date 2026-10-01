package ondisk

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
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

// EncodeUic encodes u into its 4-byte on-disk representation, the exact
// inverse of DecodeUic (note the same member-then-group field order).
func EncodeUic(u Uic) []byte {
	b := make([]byte, UicSize)
	binary.LittleEndian.PutUint16(b[0:2], u.Member)
	binary.LittleEndian.PutUint16(b[2:4], u.Group)
	return b
}

// MaxUicGroup and MaxUicMember are the largest group and member numbers a
// UIC can have (octal 37776 and 177776). The values one above them, octal
// 37777 and 177777, are reserved by VMS as wildcards ("any group", "any
// member") and never name a real account.
const (
	MaxUicGroup  = 0o37776
	MaxUicMember = 0o177776
)

// ParseUic reads a UIC as VMS writes one: "[group,member]", both numbers
// in octal, e.g. "[1,4]" or "[360,4]". Angle brackets ("<1,4>") are
// accepted too, as DCL accepts them, and spaces are ignored. Only the
// numeric form is read: VMS also lets a UIC be written as an identifier
// name ("[SYSTEM]"), but translating a name needs the system's rights
// database, which a disk image doesn't carry.
func ParseUic(text string) (Uic, error) {
	s := strings.ReplaceAll(strings.TrimSpace(text), " ", "")

	var inner string

	switch {
	case strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"),
		strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"):
		inner = s[1 : len(s)-1]
	default:
		return Uic{}, fmt.Errorf("ondisk: UIC %q: want [group,member]", text)
	}

	groupText, memberText, ok := strings.Cut(inner, ",")
	if !ok {
		return Uic{}, fmt.Errorf("ondisk: UIC %q: want [group,member]", text)
	}

	group, err := strconv.ParseUint(groupText, 8, 32)
	if err != nil || group > MaxUicGroup {
		return Uic{}, fmt.Errorf("ondisk: UIC %q: group must be an octal number from 0 to %o", text, MaxUicGroup)
	}

	member, err := strconv.ParseUint(memberText, 8, 32)
	if err != nil || member > MaxUicMember {
		return Uic{}, fmt.Errorf("ondisk: UIC %q: member must be an octal number from 0 to %o", text, MaxUicMember)
	}

	return Uic{Group: uint16(group), Member: uint16(member)}, nil
}
