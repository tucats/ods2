package ondisk

import (
	"encoding/binary"
	"fmt"
)

// The access control entries (ACEs) an ACL is made of (see acl.go for
// where the list lives in a file header). Every ACE starts with the same
// eight bytes:
//
//	byte 0      its length in bytes, these eight included
//	byte 1      its type (ACETypeIdentifier, ...)
//	word 2      its flags: the options (ACEOptionDefault, ...) in the high
//	            byte, and in the low byte flags of the type's own (an
//	            alarm's or audit's ACEAlarmSuccess and ACEAlarmFailure)
//	longword 4  the access it concerns, ACEAccessRead and the rest
//
// and then whatever its type needs:
//
//   - An identifier ACE grants the access at 4 to a process that holds
//     every identifier listed after it, a longword each (one ACE can name
//     several: "IDENTIFIER=[200,1]+PROT93_TEAM").
//   - A default protection ACE, on a directory, gives the files created in
//     it their protection code. The longword at 4 is unused (0), and the
//     four longwords at 8 are the system, owner, group, and world
//     categories' DENIED bits, each with control (ACEAccessControl)
//     denied too: (S:RWED,O:RWED,G:RE,W:) is 0x10, 0x10, 0x1A, 0x1F.
//   - A creator ACE, on a directory, is the eight bytes alone: the access
//     a file's creator gets when it isn't the file's owner.
//   - An alarm or audit ACE names a security journal after its eight
//     bytes ("SECURITY"), and asks for an alarm or audit record when the
//     access at 4 succeeds or fails (the low flag bits). This package
//     only carries them.
//
// These layouts are what VMS V7.1's $DISPLAY returned for ACEs its SET
// SECURITY/ACL made (govax's Phase 93 probe); the numbers are VMS's
// $ACEDEF values.

// ACEType is an ACE's type, its second byte.
type ACEType byte

// The ACE types this package knows. Others are kept as they are (ACE.Raw).
const (
	ACETypeIdentifier        ACEType = 1  // ACE$C_KEYID
	ACETypeAudit             ACEType = 5  // ACE$C_AUDIT
	ACETypeAlarm             ACEType = 6  // ACE$C_ALARM
	ACETypeDefaultProtection ACEType = 9  // ACE$C_DIRDEF
	ACETypeCreator           ACEType = 13 // ACE$C_NEW_OWNER
)

// The options, in ACE.Flags's high byte, as DCL's OPTIONS= names them.
const (
	// ACEOptionDefault: the ACE is for the files made in a directory, not
	// the directory itself. An identifier ACE with it takes no part in an
	// access check; a new file gets it with this option cleared.
	ACEOptionDefault uint16 = 0x100

	// ACEOptionProtected: the ACE survives a deletion of the whole ACL
	// (SET SECURITY/ACL/DELETE=ALL) and is kept by /LIKE's copy.
	ACEOptionProtected uint16 = 0x200

	// ACEOptionHidden: the ACE is an application's, not shown by DCL.
	ACEOptionHidden uint16 = 0x400

	// ACEOptionNoPropagate: the ACE isn't copied to a new version of the
	// file or to a new subdirectory.
	ACEOptionNoPropagate uint16 = 0x800
)

// The flags of an alarm or audit ACE's own, in ACE.Flags's low byte: an
// alarm or audit record on a successful access, on a failed one.
const (
	ACEAlarmSuccess uint16 = 0x1
	ACEAlarmFailure uint16 = 0x2
)

// The access bits of ACE.Access (VMS's ARM$M_ values for files). Unlike a
// protection code's fields, a set bit here GRANTS the access.
const (
	ACEAccessRead    uint32 = 0x1
	ACEAccessWrite   uint32 = 0x2
	ACEAccessExecute uint32 = 0x4
	ACEAccessDelete  uint32 = 0x8
	ACEAccessControl uint32 = 0x10
)

// aceHeaderSize is the eight bytes every ACE starts with, and the whole of
// a creator ACE.
const aceHeaderSize = 8

// aceDefaultProtectionSize is a default protection ACE's length: the
// header and the four categories' longwords.
const aceDefaultProtectionSize = aceHeaderSize + 16

// ACE is one access control entry, decoded.
type ACE struct {
	Type   ACEType
	Flags  uint16
	Access uint32

	// Identifiers are an identifier ACE's identifiers, all of which a
	// process must hold for the ACE to apply.
	Identifiers []Identifier

	// DefaultProtection is a default protection ACE's protection code, in
	// FileHeader.FileProtection's form (a set bit denies).
	DefaultProtection uint16

	// Journal is an alarm or audit ACE's journal name.
	Journal string

	// Raw is the entry as stored, which DecodeACE fills in. Encode uses
	// it for a type this package doesn't know, so such an entry is
	// written back unchanged.
	Raw []byte
}

// Has reports whether every option in opts is set in a's flags.
func (a ACE) Has(opts uint16) bool { return a.Flags&opts == opts }

// NewIdentifierACE is an identifier ACE granting access to the holder of
// every identifier in ids, with the options in opts.
func NewIdentifierACE(ids []Identifier, access uint32, opts uint16) ACE {
	return ACE{Type: ACETypeIdentifier, Flags: opts, Access: access, Identifiers: append([]Identifier(nil), ids...)}
}

// NewDefaultProtectionACE is a default protection ACE giving the files
// made in a directory the protection mask protection (a set bit denies).
func NewDefaultProtectionACE(protection uint16, opts uint16) ACE {
	return ACE{Type: ACETypeDefaultProtection, Flags: opts, DefaultProtection: protection}
}

// DecodeACE reads the ACE at the start of b, which must hold at least its
// whole length.
func DecodeACE(b []byte) (ACE, error) {
	if len(b) < 1 || int(b[0]) < aceHeaderSize || int(b[0]) > len(b) {
		return ACE{}, fmt.Errorf("ondisk: ACE has a bad length")
	}

	data := b[:b[0]]
	le := binary.LittleEndian

	a := ACE{
		Type:   ACEType(data[1]),
		Flags:  le.Uint16(data[2:]),
		Access: le.Uint32(data[4:]),
		Raw:    append([]byte(nil), data...),
	}

	switch a.Type {
	case ACETypeIdentifier:
		if (len(data)-aceHeaderSize)%4 != 0 || len(data) == aceHeaderSize {
			return ACE{}, fmt.Errorf("ondisk: identifier ACE has a bad length, %d", len(data))
		}

		for off := aceHeaderSize; off < len(data); off += 4 {
			a.Identifiers = append(a.Identifiers, Identifier(le.Uint32(data[off:])))
		}
	case ACETypeDefaultProtection:
		if len(data) != aceDefaultProtectionSize {
			return ACE{}, fmt.Errorf("ondisk: default protection ACE has a bad length, %d", len(data))
		}

		// Each category's longword holds its denied bits; the code keeps
		// the low four, read to delete.
		for c := 0; c < 4; c++ {
			denied := le.Uint32(data[aceHeaderSize+4*c:]) & 0xF
			a.DefaultProtection |= uint16(denied) << (4 * uint(c))
		}
	case ACETypeAlarm, ACETypeAudit:
		a.Journal = string(data[aceHeaderSize:])
	}

	return a, nil
}

// Encode is a as stored: the inverse of DecodeACE. An ACE of a type this
// package doesn't know is its Raw bytes.
func (a ACE) Encode() []byte {
	var data []byte

	switch a.Type {
	case ACETypeIdentifier:
		data = make([]byte, aceHeaderSize+4*len(a.Identifiers))
		for i, id := range a.Identifiers {
			binary.LittleEndian.PutUint32(data[aceHeaderSize+4*i:], uint32(id))
		}
	case ACETypeDefaultProtection:
		data = make([]byte, aceDefaultProtectionSize)
		for c := 0; c < 4; c++ {
			denied := uint32(a.DefaultProtection>>(4*uint(c)))&0xF | ACEAccessControl
			binary.LittleEndian.PutUint32(data[aceHeaderSize+4*c:], denied)
		}
	case ACETypeCreator:
		data = make([]byte, aceHeaderSize)
	case ACETypeAlarm, ACETypeAudit:
		data = make([]byte, aceHeaderSize+len(a.Journal))
		copy(data[aceHeaderSize:], a.Journal)
	default:
		return append([]byte(nil), a.Raw...)
	}

	data[0] = byte(len(data))
	data[1] = byte(a.Type)
	binary.LittleEndian.PutUint16(data[2:], a.Flags)
	binary.LittleEndian.PutUint32(data[4:], a.Access)

	return data
}

// DecodeACL reads an ACL's entries one after another, as FileHeader.ACL
// returns them. A length byte of 0 ends the list.
func DecodeACL(acl []byte) ([]ACE, error) {
	var aces []ACE

	for off := 0; off < len(acl) && acl[off] != 0; {
		a, err := DecodeACE(acl[off:])
		if err != nil {
			return aces, fmt.Errorf("ondisk: ACE at offset %d: %w", off, err)
		}

		aces = append(aces, a)
		off += len(a.Raw)
	}

	return aces, nil
}

// EncodeACL is aces one after another, as a header's ACL area holds them
// (before ACLArea's padding).
func EncodeACL(aces []ACE) []byte {
	var acl []byte

	for _, a := range aces {
		acl = append(acl, a.Encode()...)
	}

	return acl
}

// ACEs is DecodeACL of this header's ACL.
func (h *FileHeader) ACEs() ([]ACE, error) {
	acl, err := h.ACL()
	if err != nil {
		return nil, err
	}

	return DecodeACL(acl)
}

// What a new file or directory takes of an existing ACL. These are VMS's
// rules for the ACL alone; who owns the new file and its protection code
// are the caller's.

// ACLForNewVersion is the ACL a new version of a file takes from the
// previous version's, acl: every entry but those with the NOPROPAGATE
// option, protected ones included.
func ACLForNewVersion(acl []byte) ([]byte, error) {
	return filterACL(acl, func(a ACE) (ACE, bool) {
		return a, !a.Has(ACEOptionNoPropagate)
	})
}

// ACLFromDirectoryDefaults is the ACL a new file (the first version of its
// name) takes from its directory's, acl: each entry with the DEFAULT
// option, that option cleared, the others kept, NOPROPAGATE among them.
// A default protection ACE gives a protection code, not an entry, and
// isn't copied.
func ACLFromDirectoryDefaults(acl []byte) ([]byte, error) {
	return filterACL(acl, func(a ACE) (ACE, bool) {
		if a.Type == ACETypeDefaultProtection || !a.Has(ACEOptionDefault) {
			return a, false
		}

		a.Flags &^= ACEOptionDefault

		return a, true
	})
}

// ACLForSubdirectory is the ACL a new directory takes from its parent's,
// acl: every entry but those with the NOPROPAGATE option, the DEFAULT
// ones keeping DEFAULT (so they reach the files made in it too) and the
// default protection ACE among them.
func ACLForSubdirectory(acl []byte) ([]byte, error) {
	return ACLForNewVersion(acl)
}

// filterACL decodes acl, keeps (and maybe changes) the entries keep says
// to, and encodes the result: nil when none is kept.
func filterACL(acl []byte, keep func(ACE) (ACE, bool)) ([]byte, error) {
	aces, err := DecodeACL(acl)
	if err != nil {
		return nil, err
	}

	var kept []ACE

	for _, a := range aces {
		if b, ok := keep(a); ok {
			kept = append(kept, b)
		}
	}

	if len(kept) == 0 {
		return nil, nil
	}

	return EncodeACL(kept), nil
}

// Identifier is a rights identifier, as an identifier ACE names it: a
// longword whose high bits say its kind. A UIC identifier (bit 31 clear)
// is a UIC, group in bits 16 to 29 and member in bits 0 to 15, so [200,1]
// is 0x00800001; a group or member of all ones is a wildcard
// ([200,*] is 0x0080FFFF, [*,*] 0x3FFFFFFF). Any other (bit 31 set) is a
// general identifier, such as INTERACTIVE (0x80000003), which only the
// system's rights database can name.
type Identifier uint32

// UicIdentifier is u as an identifier.
func UicIdentifier(u Uic) Identifier {
	return Identifier(uint32(u.Group&WildGroup)<<16 | uint32(u.Member))
}

// IsUic reports whether id is a UIC identifier.
func (id Identifier) IsUic() bool { return id&0x80000000 == 0 }

// Uic is a UIC identifier's UIC, wildcards included.
func (id Identifier) Uic() Uic {
	return Uic{Group: uint16(id>>16) & WildGroup, Member: uint16(id)}
}

// String is a UIC identifier as its UIC ("[200,*]"), and any other as
// "%X" and its value, as VMS shows an identifier it can't name.
func (id Identifier) String() string {
	if id.IsUic() {
		return id.Uic().String()
	}

	return fmt.Sprintf("%%X%08X", uint32(id))
}
