package ondisk

import (
	"encoding/binary"
	"fmt"

	"github.com/tucats/ods2/vmstime"
)

// identSize is the number of bytes an Ident occupies on disk.
const identSize = 120

// Ident is a file header's "file identification area": the file's own
// name text as actually stored on disk (which a caller would normally
// already know, from whatever directory lookup found this file — Ident
// is mostly useful for cross-checking, or for recovering a file's name
// when working from its Fid alone) plus its creation, revision,
// expiration, and backup dates.
//
// Like RetrievalPointers, this decodes one of a FileHeader's
// variable-position areas — located via FileHeader.IdentOffset, a word
// offset from the start of the header, the same way MapOffset locates the
// retrieval-pointer area.
type Ident struct {
	Filename string

	// Revision is incremented each time the file's contents are modified
	// (RMS's own revision counter, unrelated to a directory version
	// number).
	Revision uint16

	CreationDate   vmstime.VMSTime
	RevisionDate   vmstime.VMSTime
	ExpirationDate vmstime.VMSTime
	BackupDate     vmstime.VMSTime

	// FilenameExtension holds any part of the filename that didn't fit in
	// the primary 20-byte Filename field. In practice this is essentially
	// always empty — VMS file names are short enough that this overflow
	// area is rarely, if ever, actually used.
	FilenameExtension string
}

// Byte offsets of each Ident field, relative to the start of the IDENT
// area (i.e. relative to FileHeader.IdentOffset*2, not to the start of
// the header block itself).
const (
	identOffFilename    = 0
	identOffRevision    = 20
	identOffCreDate     = 22
	identOffRevDate     = 30
	identOffExpDate     = 38
	identOffBakDate     = 46
	identOffFilenameExt = 54
)

// decodeNulPaddedString decodes a fixed-width text field that may be
// padded with trailing NUL bytes, trailing spaces, or a mix of both —
// unlike the home block's space-padded text fields (see
// decodePaddedString), this project has not confirmed which convention
// the file identification area actually uses in practice, so both kinds
// of padding are trimmed to be safe.
func decodeNulPaddedString(b []byte) string {
	end := len(b)
	for end > 0 && (b[end-1] == 0 || b[end-1] == ' ') {
		end--
	}
	return string(b[:end])
}

// Ident decodes this file header's file identification area.
func (h *FileHeader) Ident() (Ident, error) {
	start := int(h.IdentOffset) * 2
	end := start + identSize
	if start < 0 || end > len(h.raw) {
		return Ident{}, fmt.Errorf(
			"ondisk: file header IDENT area [%d:%d) is out of bounds for a %d-byte header",
			start, end, len(h.raw))
	}
	area := h.raw[start:end]

	return Ident{
		Filename:          decodeNulPaddedString(area[identOffFilename : identOffFilename+20]),
		Revision:          binary.LittleEndian.Uint16(area[identOffRevision:]),
		CreationDate:      decodeVMSTime(area[identOffCreDate:]),
		RevisionDate:      decodeVMSTime(area[identOffRevDate:]),
		ExpirationDate:    decodeVMSTime(area[identOffExpDate:]),
		BackupDate:        decodeVMSTime(area[identOffBakDate:]),
		FilenameExtension: decodeNulPaddedString(area[identOffFilenameExt : identOffFilenameExt+66]),
	}, nil
}

// EncodeIdent encodes id into its 120-byte on-disk representation — the
// fixed-size IDENT area (*FileHeader).Ident decodes, always identSize
// bytes regardless of how short id's actual text fields are. Like the home
// block's text fields (see encodePaddedString), the name fields are padded
// with spaces, as VMS INITIALIZE and the VMS file system write them
// (confirmed against a VMS-initialized volume); decode accepts either
// spaces or NULs (see decodeNulPaddedString).
//
// The only way this can fail is Filename longer than 20 bytes or
// FilenameExtension longer than 66 bytes — the fixed widths of those two
// fields on disk.
func EncodeIdent(id Ident) ([]byte, error) {
	if len(id.Filename) > 20 {
		return nil, fmt.Errorf("ondisk: Ident.Filename %q is %d bytes, want at most 20", id.Filename, len(id.Filename))
	}
	if len(id.FilenameExtension) > 66 {
		return nil, fmt.Errorf("ondisk: Ident.FilenameExtension %q is %d bytes, want at most 66", id.FilenameExtension, len(id.FilenameExtension))
	}

	b := make([]byte, identSize)

	// Both name fields are padded with spaces, as VMS writes them (see
	// IdentName).
	copy(b[identOffFilename:identOffFilename+20], fmt.Sprintf("%-20s", id.Filename))
	binary.LittleEndian.PutUint16(b[identOffRevision:], id.Revision)
	encodeVMSTime(b[identOffCreDate:], id.CreationDate)
	encodeVMSTime(b[identOffRevDate:], id.RevisionDate)
	encodeVMSTime(b[identOffExpDate:], id.ExpirationDate)
	encodeVMSTime(b[identOffBakDate:], id.BackupDate)
	copy(b[identOffFilenameExt:identOffFilenameExt+66], fmt.Sprintf("%-66s", id.FilenameExtension))

	return b, nil
}

// IdentName returns the Filename and FilenameExtension an Ident holds for
// the file name.typ;version, the way VMS stores a file's name in its
// header: the name, type, and version together ("INDEXF.SYS;1"), the
// first 20 characters in Filename and any more in FilenameExtension.
// EncodeIdent pads both with spaces. A version of 0 leaves the ";version"
// off, for a caller that doesn't know it. A name too long for both fields
// is cut to fit: the directory entry, not the header, is what records a
// file's name for lookup.
func IdentName(name string, version uint16) (filename, extension string) {
	full := name
	if version != 0 {
		full = fmt.Sprintf("%s;%d", name, version)
	}

	if len(full) > 20+66 {
		full = full[:20+66]
	}

	if len(full) <= 20 {
		return full, ""
	}

	return full[:20], full[20:]
}
