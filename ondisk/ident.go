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
