// Package ondisk decodes the byte-exact on-disk structures of an ODS-2
// (Files-11 Structure Level 2) volume: the volume home block, file headers,
// file IDs, record attributes, retrieval pointers, the storage control
// block, and directory records.
//
// All multi-byte fields are stored little-endian on disk and are decoded
// explicitly via encoding/binary, independent of host architecture. A small
// number of fields (RecAttr.Hiblk and RecAttr.Efblk) additionally use the
// VAX RMS "swapped longword" convention: the two 16-bit halves of the
// longword are stored in swapped order. That transform is applied on top of,
// and independently of, the little-endian byte decode.
package ondisk
