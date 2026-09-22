package vmstime

import (
	"testing"
	"time"
)

func TestZeroTicksIsVMSEpoch(t *testing.T) {
	// Tick count 0 is, by definition, the VMS epoch itself:
	// 17-NOV-1858 00:00:00.00 UTC.
	got := VMSTime(0).Time()
	want := time.Date(1858, time.November, 17, 0, 0, 0, 0, time.UTC)

	if !got.Equal(want) {
		t.Errorf("VMSTime(0).Time() = %v, want %v", got, want)
	}
}

func TestUnixEpochConversion(t *testing.T) {
	unixEpoch := time.Unix(0, 0).UTC()

	got := FromTime(unixEpoch)
	want := VMSTime(vmsToUnixOffsetTicks)
	
	if got != want {
		t.Errorf("FromTime(unix epoch) = %d, want %d", got, want)
	}

	// And the conversion should round-trip back to the same instant.
	if roundTripped := got.Time(); !roundTripped.Equal(unixEpoch) {
		t.Errorf("round-tripped time = %v, want %v", roundTripped, unixEpoch)
	}
}

func TestRoundTripArbitraryTime(t *testing.T) {
	// An arbitrary, easy-to-recognize instant with sub-second precision,
	// to check that fractional seconds survive the round trip too (VMS
	// only has 100ns resolution, and this value is an exact multiple of
	// 100ns, so no precision should be lost).
	original := time.Date(1998, time.March, 24, 20, 15, 23, 500_000_000, time.UTC)

	vmsTime := FromTime(original)
	roundTripped := vmsTime.Time()

	if !roundTripped.Equal(original) {
		t.Errorf("round-tripped time = %v, want %v", roundTripped, original)
	}
}

func TestTimeBeforeUnixEpoch(t *testing.T) {
	// VMS's epoch long predates Unix's, so converting a date from that
	// range exercises the negative-remainder correction in Time().
	original := time.Date(1900, time.January, 1, 12, 30, 0, 0, time.UTC)

	vmsTime := FromTime(original)
	roundTripped := vmsTime.Time()

	if !roundTripped.Equal(original) {
		t.Errorf("round-tripped time = %v, want %v", roundTripped, original)
	}
}
