package ondisk

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// DirEntry is one (name, version) -> Fid mapping recorded in a directory.
// On ODS-2, a directory is just an ordinary file (see
// FileHeader.IsDirectory) whose data — read as whole 512-byte blocks — is a
// packed sequence of these records rather than arbitrary bytes; decoding
// a directory's contents means decoding every block of its data with
// DecodeDirectoryBlock. VMS keeps every version of a file that hasn't been
// purged, so a directory can (and often does) list the same Name multiple
// times, once per surviving Version.
type DirEntry struct {
	// Name is the file's name and type together, e.g. "README.TXT" or
	// "SUBDIR.DIR" — VMS stores them as one text field on disk, with no
	// version suffix (Version is a separate field).
	Name    string
	Version uint16
	Fid     Fid
}

// Sizes of the two fixed-format pieces of a directory block, named to
// match the on-disk structures dir$rec and dir$ent from the original
// implementation:
//
//   - A "name record" starts with a 6-byte fixed header (a 2-byte overall
//     size, a 2-byte version limit this project doesn't need, a 1-byte
//     flags field, and a 1-byte name length), followed by that many bytes
//     of name text.
//   - Immediately after the name record (padded — see below) comes one or
//     more 8-byte "version entries", each recording one existing version
//     of that name: a 2-byte version number and that version's 6-byte Fid.
//
// All versions of one name are grouped together under a single name
// record this way, rather than each version repeating the name text.
const (
	dirRecHeaderSize = 6
	dirEntSize       = 8

	// dirMaxRecordSize is the largest legitimate value of a name record's
	// on-disk size field. Any value larger than this (in practice, the
	// sentinel value 0xFFFF) marks the end of the meaningfully-used data
	// in a directory block — everything from there to the end of the
	// 512-byte block is unused filler.
	dirMaxRecordSize = BlockSize - 2
)

// roundUpToEven rounds n up to the next even number, leaving it unchanged
// if it's already even. Used because a name record pads its name text to
// an even length before the version entries begin — keeping every
// subsequent field on a 2-byte boundary, which is convenient for a format
// otherwise built entirely out of 16-bit and 32-bit fields.
func roundUpToEven(n int) int {
	return (n + 1) &^ 1
}

// DecodeDirectoryBlock decodes every DirEntry recorded in one 512-byte
// directory data block. There is no header at the start of a directory
// block — its record data begins immediately at byte 0 — and no
// cross-block state: each block can be decoded independently, in any
// order, and the results from every block of a directory's data
// concatenated to get the directory's full listing.
//
// Scanning stops as soon as it reaches a name record whose size field
// exceeds dirMaxRecordSize (in a well-formed block, this is the 0xFFFF
// end-of-data sentinel); everything from that point to the end of the
// block is unused space, not further records.
func DecodeDirectoryBlock(block []byte) ([]DirEntry, error) {
	if len(block) != BlockSize {
		return nil, fmt.Errorf("ondisk: directory block requires exactly %d bytes, got %d", BlockSize, len(block))
	}

	var entries []DirEntry

	offset := 0

	for offset+dirRecHeaderSize <= len(block) {
		recordSize := binary.LittleEndian.Uint16(block[offset : offset+2])
		if recordSize > dirMaxRecordSize {
			// End-of-data sentinel (or corrupt data indistinguishable
			// from one) — either way, there's nothing more to read in
			// this block.
			break
		}

		nameCount := int(block[offset+5])
		nameStart := offset + dirRecHeaderSize

		nameEnd := nameStart + nameCount
		if nameEnd > len(block) {
			return nil, fmt.Errorf("ondisk: directory record's name runs past the end of the block")
		}

		name := string(block[nameStart:nameEnd])

		// recordEnd is where the NEXT record starts: on disk, a record's
		// stored size field is defined as (this record's total length in
		// bytes) - 2, so the next record begins recordSize+2 bytes after
		// this one starts.
		recordEnd := offset + int(recordSize) + 2
		entriesStart := nameStart + roundUpToEven(nameCount)

		if recordEnd > len(block) || entriesStart > recordEnd {
			return nil, fmt.Errorf("ondisk: directory record size is inconsistent with its name length")
		}

		entryAreaSize := recordEnd - entriesStart
		if entryAreaSize%dirEntSize != 0 {
			return nil, fmt.Errorf("ondisk: directory record's version-entry area is not a whole number of entries")
		}

		for pos := entriesStart; pos < recordEnd; pos += dirEntSize {
			fid, err := DecodeFid(block[pos+2 : pos+dirEntSize])
			if err != nil {
				return nil, fmt.Errorf("ondisk: decoding directory entry Fid: %w", err)
			}

			entries = append(entries, DirEntry{
				Name:    name,
				Version: binary.LittleEndian.Uint16(block[pos : pos+2]),
				Fid:     fid,
			})
		}

		offset = recordEnd
	}

	return entries, nil
}

// dirNameLenMax is the largest name a directory record can hold: its
// length is stored in a single on-disk byte (see this file's package-level
// comment on the name record layout).
const dirNameLenMax = 255

// EncodeDirectoryBlock encodes entries into one 512-byte directory data
// block, the inverse of DecodeDirectoryBlock. Entries are grouped by Name
// into one name record per distinct name — each holding every version of
// that name found in entries — with the groups themselves ordered by Name
// ascending and, within a group, versions ordered descending. Both match
// the on-disk convention a real volume's directories already follow (see
// Directory.List's doc comment in package volume, and Directory.Lookup's
// "version 0 means highest" rule, which finding the highest version first
// serves directly): DecodeDirectoryBlock itself doesn't require any
// particular order to decode correctly, but producing one that already
// matches convention means a block this function writes is
// indistinguishable, byte for byte, from one a real, well-behaved VMS
// system would have written for the same entries.
//
// Like the reference implementation's own equivalent, this is a
// whole-block "encode this set of entries" function, not an in-place
// byte-splicing API: it has no notion of an existing block to slot new
// entries into, and no opinion on what to do when entries don't fit in one
// block — both are package volume's job (allocating another block and
// calling this again), not this package's. An entries slice that can't fit
// in a single block, or a Name too long for its 1-byte length field,
// produces an error rather than a silently truncated or corrupt block.
func EncodeDirectoryBlock(entries []DirEntry) ([]byte, error) {
	byName := make(map[string][]DirEntry, len(entries))
	names := make([]string, 0, len(entries))

	for _, e := range entries {
		if _, seen := byName[e.Name]; !seen {
			names = append(names, e.Name)
		}

		byName[e.Name] = append(byName[e.Name], e)
	}

	sort.Strings(names)

	var block []byte

	for _, name := range names {
		nameBytes := []byte(name)
		if len(nameBytes) > dirNameLenMax {
			return nil, fmt.Errorf("ondisk: directory entry name %q is %d bytes, longer than the %d-byte on-disk limit", name, len(nameBytes), dirNameLenMax)
		}

		versions := byName[name]
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].Version > versions[j].Version
		})

		paddedNameLen := roundUpToEven(len(nameBytes))
		entriesStart := dirRecHeaderSize + paddedNameLen
		recordLen := entriesStart + len(versions)*dirEntSize

		record := make([]byte, recordLen)
		binary.LittleEndian.PutUint16(record[0:2], uint16(recordLen-2)) // dir$size = total record length - 2
		// Bytes [2:4] (version limit) and byte [4] (flags) are left zero:
		// this project's own DecodeDirectoryBlock never reads them, the
		// same "not needed" call this file's own package comment already
		// makes about the version-limit field.
		record[5] = uint8(len(nameBytes))
		copy(record[dirRecHeaderSize:dirRecHeaderSize+len(nameBytes)], nameBytes)

		for i, v := range versions {
			pos := entriesStart + i*dirEntSize
			binary.LittleEndian.PutUint16(record[pos:pos+2], v.Version)
			copy(record[pos+2:pos+dirEntSize], EncodeFid(v.Fid))
		}

		block = append(block, record...)
	}

	if len(block) > BlockSize {
		return nil, fmt.Errorf("ondisk: directory entries need %d bytes, more than fit in one %d-byte block", len(block), BlockSize)
	}

	out := make([]byte, BlockSize)
	copy(out, block)

	if len(block)+2 <= BlockSize {
		// The 0xFFFF end-of-data sentinel. When the encoded records
		// happen to fill the block exactly, there's no room (or need) for
		// it: DecodeDirectoryBlock's scan loop stops on its own once
		// there's no room left for even a record header.
		binary.LittleEndian.PutUint16(out[len(block):len(block)+2], 0xFFFF)
	}

	return out, nil
}
