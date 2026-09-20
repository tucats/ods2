// Package vmstime converts between VMS quadword timestamps and Go's time.Time.
//
// VMS represents an absolute time as a 64-bit count of 100-nanosecond ticks
// since 17-NOV-1858 00:00:00.00, and a relative (delta) time as the same
// representation negated. This package models the two as distinct types
// rather than relying on VMS's sign-overloading convention.
package vmstime
