package ondisk

import "encoding/binary"

// decodeSwappedLongword decodes a 4-byte on-disk field that uses the VAX
// RMS "swapped longword" convention, rather than being an ordinary
// little-endian 32-bit integer.
//
// Background for readers unfamiliar with this: a few fields inherited from
// very old PDP-11 RMS code (RecAttr's HighestBlock and EndOfFileBlock, in
// this package) represent a 32-bit value as two 16-bit words with the MORE
// significant word stored FIRST. That is the opposite of ordinary
// little-endian encoding, where the LESS significant part comes first. When
// VAX/VMS adopted these structures, it kept that historical layout for
// compatibility rather than "fixing" it.
//
// So decoding one of these fields means: read two ordinary little-endian
// 16-bit words, then treat the FIRST word as the high half and the SECOND
// word as the low half of the result — backwards from what you'd get by
// just decoding all 4 bytes as one little-endian uint32.
//
// This is a completely separate concern from little-endian-vs-big-endian
// byte order (every platform this project targets is little-endian
// already, so that question never even comes up). The swap has to be
// applied by hand, in code, for the handful of fields that are documented
// as using it — most fields are plain little-endian and must NOT be run
// through this function.
func decodeSwappedLongword(b []byte) uint32 {
	firstWord := binary.LittleEndian.Uint16(b[0:2])
	secondWord := binary.LittleEndian.Uint16(b[2:4])
	return uint32(firstWord)<<16 | uint32(secondWord)
}
