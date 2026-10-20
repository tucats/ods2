package ondisk

import "fmt"

// An access control list (ACL) refines a file's protection: beyond the
// four-category mask in FileProtection, it can grant or deny access to
// particular users or groups ("identifiers"). On ODS-2 the list lives in
// the file header's ACL area, between AclOffset and EndOffset, as a run of
// access control entries (ACEs) packed one after another. Each ACE starts
// with a byte giving its own length in bytes (the length byte included),
// then a byte giving its type (1 is an identifier ACE), then type-specific
// content. A length byte of 0 ends the list early, when the ACEs don't fill
// the area.
//
// This package doesn't interpret the ACEs: it hands the list back as the
// bytes the header holds, for a caller that knows their layout.

// ACL returns this header's access control entries, the ACL area's bytes
// up to the end of its last entry (nil if it has none). A length byte of 0,
// or one that would run past the area's end, ends the list.
func (h *FileHeader) ACL() ([]byte, error) {
	start := int(h.AclOffset) * 2
	end := int(h.EndOffset) * 2

	if start == end {
		return nil, nil
	}

	if start > end || end > fhOffChecksum {
		return nil, fmt.Errorf("ondisk: file header ACL area [%d:%d) is out of bounds", start, end)
	}

	area := h.raw[start:end]

	used := 0
	for used < len(area) {
		size := int(area[used])
		if size == 0 || used+size > len(area) {
			break
		}

		used += size
	}

	if used == 0 {
		return nil, nil
	}

	acl := make([]byte, used)
	copy(acl, area[:used])

	return acl, nil
}

// ACLArea is acl as a header's ACL area holds it (FileHeaderAreas.
// AclBytes): padded with a zero byte to a whole number of words when its
// length is odd. The zero reads as a length byte of 0, ending the list.
func ACLArea(acl []byte) []byte {
	if len(acl) == 0 {
		return nil
	}

	area := make([]byte, len(acl)+len(acl)%2)
	copy(area, acl)

	return area
}
