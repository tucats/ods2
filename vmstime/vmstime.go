package vmstime

import "time"

// VMSTime represents an absolute VMS timestamp: a signed 64-bit count of
// 100-nanosecond "ticks" (VMS's traditional time unit — sometimes written
// as "hundreds of nanoseconds") counted from the VMS base time of
// 17-NOV-1858 00:00:00.00.
//
// Why 1858? That date is the start of the "Modified Julian Date" (MJD)
// astronomical calendar, a numbering scheme where each date is simply a
// day count from that reference point. VMS reused MJD day 0 as its own
// time epoch. This turns out to be a convenient fact for converting to and
// from Go's time.Time (which is anchored at a different, arbitrary
// reference point): the Unix epoch, 1-JAN-1970, happens to be exactly
// 40587 days after the VMS epoch — a well-known, fixed constant — which is
// all the information needed to convert between the two.
//
// A VMSTime is only meaningful as an absolute point in time. VMS also has
// a "delta time" concept (e.g. "how long is 2 hours and 30 minutes") that
// reuses the same on-disk representation but negated; this package does
// not yet model delta times, since nothing in this project currently needs
// to read or write one. Add a distinct type for that if/when it's needed,
// rather than overloading the sign of VMSTime the way the original VMS
// software does — a separate type makes "this value can never be negative"
// a property the compiler can help enforce.
type VMSTime int64

// vmsToUnixOffsetTicks is the number of 100-nanosecond ticks between the
// VMS epoch (17-NOV-1858 00:00:00.00) and the Unix epoch (1-JAN-1970
// 00:00:00 UTC): 40587 days, converted to 100ns ticks.
//
//	40587 days * 86400 seconds/day * 10,000,000 ticks/second
//	    = 35,067,168,000,000,000 ticks
const vmsToUnixOffsetTicks int64 = 40587 * 86400 * 10_000_000

// ticksPerSecond is how many 100-nanosecond ticks make up one second. VMS
// measures time in units of 100 nanoseconds, and there are 10,000,000 such
// units in a second (100ns * 10,000,000 = 1,000,000,000ns = 1 second).
const ticksPerSecond = 10_000_000

// nanosecondsPerTick converts a tick count into nanoseconds: each tick is
// 100 nanoseconds.
const nanosecondsPerTick = 100

// Time converts a VMSTime into a Go time.Time, in UTC. VMS timestamps
// don't carry explicit time zone information (they're conventionally
// interpreted in whatever local time zone the system that wrote them was
// set to), so this conversion simply treats the tick count as an absolute
// offset from the VMS epoch with no time zone adjustment — the same
// assumption the original C implementation makes.
func (t VMSTime) Time() time.Time {
	unixTicks := int64(t) - vmsToUnixOffsetTicks

	seconds := unixTicks / ticksPerSecond
	remainderTicks := unixTicks % ticksPerSecond

	// Go's integer division truncates toward zero, so for a negative tick
	// count (a date before the Unix epoch) the remainder can come out
	// negative too (e.g. -1 tick is -1 second + 9,999,999 remaining
	// ticks, not 0 seconds + -1 tick). Correct for that so the
	// nanosecond value we hand to time.Unix is always non-negative, as
	// that function expects.
	if remainderTicks < 0 {
		seconds--
		remainderTicks += ticksPerSecond
	}

	nanoseconds := remainderTicks * nanosecondsPerTick
	return time.Unix(seconds, nanoseconds).UTC()
}

// FromTime converts a Go time.Time into a VMSTime. The input is first
// converted to UTC, for the same reason Time returns UTC: a VMSTime has no
// time zone of its own.
func FromTime(t time.Time) VMSTime {
	u := t.UTC()
	unixTicks := u.Unix()*ticksPerSecond + int64(u.Nanosecond())/nanosecondsPerTick
	return VMSTime(unixTicks + vmsToUnixOffsetTicks)
}
