package ondisk

import (
	"bytes"
	"testing"
)

func TestDecodeUic(t *testing.T) {
	// On-disk field order is member-then-group, the reverse of how a UIC
	// is conventionally displayed ("[group,member]"). Member=4 (LE: 04 00),
	// Group=10 (LE: 0A 00).
	b := []byte{0x04, 0x00, 0x0A, 0x00}

	uic, err := DecodeUic(b)
	if err != nil {
		t.Fatalf("DecodeUic: %v", err)
	}

	if uic.Member != 4 {
		t.Errorf("Member = %d, want 4", uic.Member)
	}
	if uic.Group != 10 {
		t.Errorf("Group = %d, want 10", uic.Group)
	}
}

func TestDecodeUicShortBuffer(t *testing.T) {
	if _, err := DecodeUic([]byte{1, 2}); err == nil {
		t.Fatal("DecodeUic with too-short buffer: want error, got nil")
	}
}

func TestUicString(t *testing.T) {
	// VMS displays a UIC as "[group,member]" in octal.
	uic := Uic{Group: 8, Member: 1} // octal 10, octal 1
	if got, want := uic.String(), "[10,1]"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestEncodeUic(t *testing.T) {
	b := []byte{0x04, 0x00, 0x0A, 0x00} // same bytes TestDecodeUic decodes
	if got := EncodeUic(Uic{Member: 4, Group: 10}); !bytes.Equal(got, b) {
		t.Errorf("EncodeUic() = % x, want % x", got, b)
	}
}

func TestUicRoundTrip(t *testing.T) {
	want := Uic{Member: 0x1234, Group: 0x5678}

	got, err := DecodeUic(EncodeUic(want))
	if err != nil {
		t.Fatalf("DecodeUic(EncodeUic(want)): %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
