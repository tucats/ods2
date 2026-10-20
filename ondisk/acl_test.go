package ondisk

import (
	"bytes"
	"testing"
)

// identifierACE builds a 12-byte identifier ACE granting access to the UIC
// [group,member]: length, type 1, flags, the access mask, and the UIC as
// member word then group word.
func identifierACE(access uint32, group, member uint16) []byte {
	return []byte{
		12, 1, 0, 0,
		byte(access), byte(access >> 8), byte(access >> 16), byte(access >> 24),
		byte(member), byte(member >> 8), byte(group), byte(group >> 8),
	}
}

func TestACLRoundTrip(t *testing.T) {
	acl := append(identifierACE(3, 1, 4), identifierACE(1, 0o200, 0o201)...)

	b, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{Ident: &Ident{Filename: "ACL.TXT;1"}, AclBytes: ACLArea(acl)})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	got, err := h.ACL()
	if err != nil {
		t.Fatalf("ACL: %v", err)
	}

	if !bytes.Equal(got, acl) {
		t.Errorf("ACL = % x, want % x", got, acl)
	}
}

func TestACLNone(t *testing.T) {
	b, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{Ident: &Ident{Filename: "A.TXT;1"}})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	if got, err := h.ACL(); err != nil || got != nil {
		t.Errorf("ACL = % x, %v; want none", got, err)
	}
}

// An odd-length ACE is padded to a word, and the pad byte ends the list.
func TestACLOddLength(t *testing.T) {
	acl := []byte{5, 9, 1, 2, 3}

	area := ACLArea(acl)
	if len(area) != 6 || area[5] != 0 {
		t.Fatalf("ACLArea = % x, want the ACE and a zero byte", area)
	}

	b, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{AclBytes: area})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	if got, _ := h.ACL(); !bytes.Equal(got, acl) {
		t.Errorf("ACL = % x, want % x", got, acl)
	}
}

// An ACE whose length runs past the area ends the list there.
func TestACLTruncated(t *testing.T) {
	area := append(identifierACE(3, 1, 4), 40, 1)

	b, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{AclBytes: area})
	if err != nil {
		t.Fatalf("EncodeFileHeader: %v", err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatalf("DecodeFileHeader: %v", err)
	}

	if got, _ := h.ACL(); !bytes.Equal(got, area[:12]) {
		t.Errorf("ACL = % x, want the first ACE", got)
	}
}
