package ondisk

import (
	"encoding/binary"
	"fmt"
)

// FidSize is the number of bytes a Fid occupies on disk.
const FidSize = 6

// Fid (File ID) uniquely identifies one file's header within a volume or
// volume set — it plays the same role that an inode number plays on a Unix
// filesystem. Every file, every directory (directories are just files with
// a special characteristic bit set), and even the volume's own bookkeeping
// files, are all identified and located by their Fid.
//
// A Fid is not itself a byte offset. Instead, Num (and Nmx, its high-order
// extension) together give the 1-based index of this file's header record
// within the volume's index file, INDEXF.SYS — see package volume for how
// that lookup works. Seq is a generation/reuse counter: when a file is
// deleted and its header slot is later reused for a different file, Seq is
// incremented, so a stale Fid captured before the deletion can be detected
// as no longer valid (its Seq won't match). Rvn identifies which physical
// disk, within a multi-disk volume set, actually holds the file.
type Fid struct {
	Num uint16 // low 16 bits of the file number (header index in INDEXF.SYS)
	Seq uint16 // sequence/generation number, incremented when a header slot is reused
	Rvn uint8  // relative volume number: which disk in a volume set (0 = same disk)
	Nmx uint8  // high-order extension of the file number, for volumes with >65535 files
}

// IndexFileFid is the fixed file ID of the index file, INDEXF.SYS. Every
// ODS-2 volume dedicates file header slot 1 to its own index file, so this
// value is a constant across every volume — it's how mounting code finds
// INDEXF.SYS in the first place, before it has looked anything else up.
// (Rvn is left at zero here; callers fill it in with the relative volume
// number of whichever disk they're bootstrapping.)
var IndexFileFid = Fid{Num: 1, Seq: 1}

// Number combines Num and Nmx into the full file number: Nmx supplies the
// high-order bits for volumes large enough to need file numbers beyond
// 65535, the range a plain 16-bit Num can express on its own.
func (f Fid) Number() uint32 {
	return uint32(f.Nmx)<<16 | uint32(f.Num)
}

// IsZero reports whether f is the null/zero file ID, used on disk as a
// sentinel meaning "there is no such file" — for example, a FileHeader's
// ExtensionFid is the zero Fid when that header has no further extension
// segment. Only the file-number fields (Num, Nmx) are significant for this
// check, matching the on-disk convention (Seq and Rvn are ignored).
func (f Fid) IsZero() bool {
	return f.Num == 0 && f.Nmx == 0
}

// Equal reports whether f and other identify the same file. Two Fids are
// considered the same file if their file numbers, sequence numbers, and
// relative volume numbers all match; a difference in Seq means one of them
// refers to a since-deleted file whose header slot was reused.
func (f Fid) Equal(other Fid) bool {
	return f.Number() == other.Number() && f.Seq == other.Seq && f.Rvn == other.Rvn
}

// String renders a Fid the way VMS tools conventionally display one: the
// three-part "(file-number,sequence,relative-volume)" form, e.g. "(137,4,1)".
func (f Fid) String() string {
	return fmt.Sprintf("(%d,%d,%d)", f.Number(), f.Seq, f.Rvn)
}

// DecodeFid decodes a Fid from its 6-byte on-disk representation. b must be
// at least FidSize bytes long; only the first FidSize bytes are read.
func DecodeFid(b []byte) (Fid, error) {
	if len(b) < FidSize {
		return Fid{}, fmt.Errorf("ondisk: Fid requires %d bytes, got %d", FidSize, len(b))
	}
	return Fid{
		Num: binary.LittleEndian.Uint16(b[0:2]),
		Seq: binary.LittleEndian.Uint16(b[2:4]),
		Rvn: b[4],
		Nmx: b[5],
	}, nil
}
