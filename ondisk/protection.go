package ondisk

import (
	"fmt"
	"strings"
)

// A file's protection (FileHeader.FileProtection, and the defaults in the
// home block) is a 16-bit mask in four 4-bit fields, one per category of
// user, from the low bits up:
//
//	bits  0-3   SYSTEM  the system manager's accounts (a low UIC group)
//	bits  4-7   OWNER   the file's owner (the same UIC as FileHeader.Owner)
//	bits  8-11  GROUP   other accounts in the owner's UIC group
//	bits 12-15  WORLD   everyone else
//
// Within each field, a SET bit DENIES one kind of access: read, write,
// execute, and delete, from the low bit up. So 0x0000 lets everyone do
// everything, and VMS's common default (S:RWED,O:RWED,G:RE,W) is 0xFA00:
// group denied write and delete (0xA), world denied everything (0xF).

// Protection access bits, within one category's 4-bit field.
const (
	ProtectionNoRead    = 0x1
	ProtectionNoWrite   = 0x2
	ProtectionNoExecute = 0x4
	ProtectionNoDelete  = 0x8
)

// ProtectionNoDeleteAll denies delete access to every category: a
// protection mask ORed with it is the same protection less delete, which is
// what VMS gives a new directory from its parent's.
const ProtectionNoDeleteAll = 0x8888

// protectionCategories are the four categories in field order, with the
// names DCL accepts for them (any leading abbreviation of the full name).
var protectionCategories = []struct {
	name  string
	short string
	shift uint
}{
	{"SYSTEM", "S", 0},
	{"OWNER", "O", 4},
	{"GROUP", "G", 8},
	{"WORLD", "W", 12},
}

// protectionAccess are the access letters in bit order.
const protectionAccess = "RWED"

// ParseProtection reads a protection the way DCL's /PROTECTION qualifier
// is written -- "(S:RWED,O:RWED,G:RE,W)" -- and returns its mask. Each
// category (SYSTEM, OWNER, GROUP, WORLD, or any abbreviation of one) is
// followed by ":" or "=" and the access letters it's granted (R, W, E, D),
// or by nothing at all for no access. The parentheses can be left off a
// single category ("W:RE").
//
// A category the text doesn't mention keeps its field from base, so
// "(G:RE)" changes only the group field of base. Naming a category twice
// is an error, as it is to DCL.
func ParseProtection(text string, base uint16) (uint16, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = s[1 : len(s)-1]
	}

	if s == "" {
		return 0, fmt.Errorf("ondisk: protection %q: no categories given", text)
	}

	mask := base
	seen := map[string]bool{}

	for _, item := range strings.Split(s, ",") {
		name, access, _ := strings.Cut(strings.Replace(item, "=", ":", 1), ":")

		category := -1

		for i, c := range protectionCategories {
			if name != "" && strings.HasPrefix(c.name, name) {
				category = i
			}
		}

		if category < 0 {
			return 0, fmt.Errorf("ondisk: protection %q: %q is not SYSTEM, OWNER, GROUP, or WORLD", text, name)
		}

		c := protectionCategories[category]
		if seen[c.name] {
			return 0, fmt.Errorf("ondisk: protection %q: %s given twice", text, c.name)
		}

		seen[c.name] = true

		field := uint16(0xF) // every access denied, until granted

		for _, letter := range access {
			bit := strings.IndexRune(protectionAccess, letter)
			if bit < 0 {
				return 0, fmt.Errorf("ondisk: protection %q: %q is not an access (R, W, E, or D)", text, letter)
			}

			field &^= 1 << bit
		}

		mask = mask&^(0xF<<c.shift) | field<<c.shift
	}

	return mask, nil
}

// FormatProtection writes mask the way ParseProtection reads it, every
// category given: "(S:RWED,O:RWED,G:RE,W)".
func FormatProtection(mask uint16) string {
	var b strings.Builder

	b.WriteByte('(')

	for i, c := range protectionCategories {
		if i > 0 {
			b.WriteByte(',')
		}

		b.WriteString(c.short)

		if granted := ProtectionAccess(mask, i); granted != "" {
			b.WriteByte(':')
			b.WriteString(granted)
		}
	}

	b.WriteByte(')')

	return b.String()
}

// ProtectionAccess returns the access letters mask grants category (0 for
// SYSTEM, 1 OWNER, 2 GROUP, 3 WORLD), in "RWED" order: "RE" for read and
// execute, "" for none.
func ProtectionAccess(mask uint16, category int) string {
	field := mask >> protectionCategories[category].shift & 0xF

	var b strings.Builder

	for bit, letter := range protectionAccess {
		if field&(1<<bit) == 0 {
			b.WriteRune(letter)
		}
	}

	return b.String()
}
