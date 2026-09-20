package vmstime

import (
	"fmt"
	"strings"
	"time"
)

// layout is a Go "reference time" format string describing VMS's
// conventional timestamp display, e.g. "17-NOV-1858 00:00:00.00":
// zero-padded day, 3-letter month abbreviation, 4-digit year, 24-hour
// time, and two digits of fractional seconds (hundredths of a second,
// which VMS calls "centiseconds").
//
// Go's time-formatting rules are unusual if you haven't seen them before:
// instead of format codes like "%Y" or "yyyy", you write out an example of
// how a specific, fixed reference moment
// (Mon Jan 2 15:04:05 MST 2006) should look, and Go matches your layout
// string's structure against that example to know what each part means.
// Here, "02" (not "2") means "always show 2 digits, zero-padded", "Jan"
// means "3-letter month abbreviation", and ".00" means "always show
// exactly 2 digits of fractional seconds".
const layout = "02-Jan-2006 15:04:05.00"

// String renders t in VMS's conventional display format, e.g.
// "17-NOV-1858 00:00:00.00". Note the month is in upper case, matching
// VMS's own convention — Go's "Jan" layout directive only capitalizes the
// first letter ("Nov"), so it's upper-cased by hand afterward.
func (t VMSTime) String() string {
	s := t.Time().Format(layout)
	return upperCaseMonth(s)
}

// ParseVMSTime parses a VMS-formatted timestamp string, such as
// "17-NOV-1858 00:00:00.00", into a VMSTime. It accepts the month
// abbreviation in any letter case (VMS itself always displays it in upper
// case, but this is more forgiving for hand-typed input).
func ParseVMSTime(s string) (VMSTime, error) {
	t, err := time.Parse(layout, lowerCaseMonthTail(s))
	if err != nil {
		return 0, fmt.Errorf("vmstime: parsing %q as a VMS timestamp: %w", s, err)
	}
	return FromTime(t), nil
}

// Both helper functions below rely on the layout's fixed field widths: the
// day is always exactly 2 digits, followed by a dash, followed by exactly
// 3 letters for the month, followed by another dash. That means the month
// letters always live at string positions [3:6], regardless of what values
// the surrounding fields hold.

// upperCaseMonth returns s with its 3-letter month abbreviation converted
// to all upper case.
func upperCaseMonth(s string) string {
	if len(s) < 6 {
		return s
	}
	return s[:3] + strings.ToUpper(s[3:6]) + s[6:]
}

// lowerCaseMonthTail returns s with its 3-letter month abbreviation
// converted to Go's expected "Jan"-style capitalization (first letter
// upper case, remaining two lower case), regardless of what case it
// started in. time.Parse requires an exact case match against the "Jan"
// layout directive, so a VMS-style all-caps month ("NOV") has to be
// converted to "Nov" before parsing will accept it.
func lowerCaseMonthTail(s string) string {
	if len(s) < 6 {
		return s
	}
	month := s[3:6]
	fixed := strings.ToUpper(month[:1]) + strings.ToLower(month[1:])
	return s[:3] + fixed + s[6:]
}
