package rms

// FormatVFCRecord reconstructs the plain-text rendering of one VFC
// ("Variable with Fixed Control") record.
//
// A VFC record's first few bytes — vfc, normally exactly 2 bytes (see
// ondisk.RecAttr.VfcSize) — aren't part of the record's actual content.
// They're Fortran-style carriage-control information describing how the
// record should be laid out as text: whether a blank line or form feed
// precedes it, and what should follow it (nothing, a plain newline, a
// specific control character, ...). text is everything in the record
// AFTER those control bytes — the record's real content.
//
// FormatVFCRecord returns the bytes a plain-text rendering of the record
// should contain: whatever leading bytes vfc[0] calls for, then text
// itself, then whatever trailing bytes vfc[1] calls for. This only
// defines a transformation for the standard 2-byte case; for any other
// VfcSize (vanishingly rare in practice — the reference implementation
// this project is based on has only ever observed 2), text is returned
// unchanged, with no leading or trailing bytes added, since there is no
// documented meaning to fall back on.
func FormatVFCRecord(vfc, text []byte) []byte {
	if len(vfc) != 2 {
		return text
	}

	out := make([]byte, 0, len(text)+4)
	out = append(out, vfcLeading(vfc[0])...)
	out = append(out, text...)
	out = append(out, vfcTrailing(vfc[1])...)
	return out
}

// vfcLeading decodes a VFC record's first control byte (conventionally
// called "vfc0"), which determines what precedes the record's text.
func vfcLeading(vfc0 byte) []byte {
	switch vfc0 {
	case 0:
		// No leading carriage control at all.
		return nil
	case '+':
		// Overstrike: the record prints on top of the previous line, so
		// nothing (no newline) precedes it.
		return nil
	case '0':
		// Double space: a blank line, then the record.
		return []byte{'\n', '\n'}
	case '1':
		// New page: a form feed, then the record.
		return []byte{'\f'}
	default:
		// ' ' (normal), '$' (prompt — differs from normal only in its
		// trailing behavior, which vfc1 controls independently), and any
		// other value all behave the same way here: a single leading
		// newline.
		return []byte{'\n'}
	}
}

// vfcTrailing decodes a VFC record's second control byte (conventionally
// called "vfc1"), which determines what follows the record's text. Unlike
// vfc0, this is a bit-coded field rather than a small set of named
// values.
func vfcTrailing(vfc1 byte) []byte {
	if vfc1 == 0 {
		// The all-zero value is its own special case, meaning "no
		// trailing carriage control at all" — distinct from the general
		// rule below, which would otherwise read this as "zero newlines
		// then one CR" (i.e. just a lone '\r').
		return nil
	}

	if vfc1&0x80 == 0 {
		// Top bit clear: the remaining 7 bits count how many newlines to
		// emit, always followed by one carriage return.
		n := int(vfc1 & 0x7F)
		out := make([]byte, 0, n+1)
		for i := 0; i < n; i++ {
			out = append(out, '\n')
		}
		return append(out, '\r')
	}

	if vfc1&0xE0 == 0x80 { // top 3 bits: 1 0 0
		// The remaining 5 bits give the exact end-of-record character to
		// emit (conventionally 0x0D, a carriage return, but not
		// necessarily).
		return []byte{vfc1 & 0x1F}
	}

	// Every other top-3-bit pattern (101, 1100, 1101, 111) reduces to
	// "just emit one carriage return" for this project's purposes. The
	// 1100xxxx pattern is technically "send these bits to a vertical
	// format unit on the output device, or just one CR if there is no
	// such device" — this project never drives a VFU, so it always takes
	// the fallback.
	return []byte{'\r'}
}
