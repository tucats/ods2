package vmstime

import "testing"

func TestString(t *testing.T) {
	tm := VMSTime(0) // the VMS epoch
	if got, want := tm.String(), "17-NOV-1858 00:00:00.00"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseVMSTime(t *testing.T) {
	got, err := ParseVMSTime("17-NOV-1858 00:00:00.00")
	if err != nil {
		t.Fatalf("ParseVMSTime: %v", err)
	}

	if got != 0 {
		t.Errorf("ParseVMSTime(epoch string) = %d, want 0", got)
	}
}

func TestParseVMSTimeIsCaseInsensitive(t *testing.T) {
	inputs := []string{
		"17-NOV-1858 00:00:00.00",
		"17-Nov-1858 00:00:00.00",
		"17-nov-1858 00:00:00.00",
	}

	for _, in := range inputs {
		got, err := ParseVMSTime(in)
		if err != nil {
			t.Errorf("ParseVMSTime(%q): %v", in, err)

			continue
		}

		if got != 0 {
			t.Errorf("ParseVMSTime(%q) = %d, want 0", in, got)
		}
	}
}

func TestStringParseRoundTrip(t *testing.T) {
	original := VMSTime(vmsToUnixOffsetTicks) // the Unix epoch, expressed as a VMSTime

	s := original.String()

	parsed, err := ParseVMSTime(s)
	if err != nil {
		t.Fatalf("ParseVMSTime(%q): %v", s, err)
	}
	
	if parsed != original {
		t.Errorf("round trip through String()/ParseVMSTime() = %d, want %d", parsed, original)
	}
}

func TestParseVMSTimeRejectsGarbage(t *testing.T) {
	if _, err := ParseVMSTime("not a timestamp"); err == nil {
		t.Fatal("ParseVMSTime on garbage input: want error, got nil")
	}
}
