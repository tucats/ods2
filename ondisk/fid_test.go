package ondisk

import "testing"

func TestDecodeFid(t *testing.T) {
	// Bytes chosen so every field is a distinct, recognizable value:
	// Num=0x0005 (LE: 05 00), Seq=0x000A (LE: 0A 00), Rvn=1, Nmx=2.
	b := []byte{0x05, 0x00, 0x0A, 0x00, 0x01, 0x02}

	fid, err := DecodeFid(b)
	if err != nil {
		t.Fatalf("DecodeFid: %v", err)
	}

	if fid.Num != 5 {
		t.Errorf("Num = %d, want 5", fid.Num)
	}
	if fid.Seq != 10 {
		t.Errorf("Seq = %d, want 10", fid.Seq)
	}
	if fid.Rvn != 1 {
		t.Errorf("Rvn = %d, want 1", fid.Rvn)
	}
	if fid.Nmx != 2 {
		t.Errorf("Nmx = %d, want 2", fid.Nmx)
	}
}

func TestDecodeFidShortBuffer(t *testing.T) {
	if _, err := DecodeFid([]byte{1, 2, 3}); err == nil {
		t.Fatal("DecodeFid with too-short buffer: want error, got nil")
	}
}

func TestFidNumber(t *testing.T) {
	// Nmx supplies the high-order bits above Num's 16-bit range.
	fid := Fid{Num: 5, Nmx: 2}
	want := uint32(2)<<16 | 5
	if got := fid.Number(); got != want {
		t.Errorf("Number() = %d, want %d", got, want)
	}
}

func TestFidEqual(t *testing.T) {
	a := Fid{Num: 5, Seq: 1, Rvn: 0, Nmx: 0}
	b := Fid{Num: 5, Seq: 1, Rvn: 0, Nmx: 0}
	if !a.Equal(b) {
		t.Error("identical Fids should be Equal")
	}

	// A different sequence number means a's header slot was reused by a
	// different (later) file, even though the file number is the same.
	c := Fid{Num: 5, Seq: 2, Rvn: 0, Nmx: 0}
	if a.Equal(c) {
		t.Error("Fids with different Seq should not be Equal")
	}

	// A different relative volume number means the "same" file number
	// refers to a file on a different disk within a volume set.
	d := Fid{Num: 5, Seq: 1, Rvn: 1, Nmx: 0}
	if a.Equal(d) {
		t.Error("Fids with different Rvn should not be Equal")
	}
}

func TestFidString(t *testing.T) {
	fid := Fid{Num: 137, Seq: 4, Rvn: 1}
	if got, want := fid.String(), "(137,4,1)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
