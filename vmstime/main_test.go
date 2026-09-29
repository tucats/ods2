package vmstime

import (
	"os"
	"testing"
	"time"
)

// TestMain runs this package's tests with Location set to UTC, so the
// expected tick counts and times they check don't depend on the time zone
// of the machine running them. TestLocalWallClock checks the time zone
// behavior itself.
func TestMain(m *testing.M) {
	Location = time.UTC

	os.Exit(m.Run())
}

// TestLocalWallClock checks that a VMSTime is a wall-clock time in
// Location: 13:00 in New York is stored as 13:00, reads back as the same
// instant, and a VMS time string names a wall-clock time there too.
func TestLocalWallClock(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}

	saved := Location
	Location = ny

	defer func() { Location = saved }()

	instant := time.Date(2026, 9, 29, 13, 0, 0, 0, ny) // 17:00 UTC
	v := FromTime(instant)

	if got := v.String(); got != "29-SEP-2026 13:00:00.00" {
		t.Errorf("FromTime(13:00 New York).String() = %q, want the New York wall-clock time", got)
	}

	if !v.Time().Equal(instant) {
		t.Errorf("Time() = %v, want %v", v.Time(), instant)
	}

	if u := FromTime(instant.UTC()); u != v {
		t.Errorf("FromTime depends on its argument's own zone: %d for the UTC form, %d for the New York form", u, v)
	}

	parsed, err := ParseVMSTime("29-SEP-2026 13:00:00.00")
	if err != nil || parsed != v {
		t.Errorf("ParseVMSTime = %d, %v; want %d", parsed, err, v)
	}
}
