// Package ondisk decodes and encodes the byte-exact on-disk structures of
// an ODS-2 (Files-11 Structure Level 2) volume: the volume home block,
// file headers, file IDs, record attributes, retrieval pointers, the
// storage control block, and directory records.
//
// Every Decode* function has a symmetric Encode* inverse (DecodeFid /
// EncodeFid, DecodeHomeBlock / EncodeHomeBlock, and so on), added once this
// project needed to write ODS-2 volumes and not just read them — see
// docs/PHASE-02.md. Where a structure has variable-position content (a
// FileHeader's IDENT/map/ACL areas, most notably), encoding it means
// choosing where that content goes, not just replaying byte offsets that
// were already on disk; see EncodeFileHeader's own documentation for how
// that layout decision is made.
//
// All multi-byte fields are stored little-endian on disk and are
// decoded/encoded explicitly via encoding/binary, independent of host
// architecture. A small number of fields (RecAttr.Hiblk and RecAttr.Efblk)
// additionally use the VAX RMS "swapped longword" convention: the two
// 16-bit halves of the longword are stored in swapped order. That
// transform is applied on top of, and independently of, the little-endian
// byte decode.
package ondisk
