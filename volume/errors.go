package volume

import "errors"

// The volume's own refusals, which a caller tests for with errors.Is (the
// error returned wraps one of these, with the details in its text).
var (
	// ErrNoSpace is an allocation the storage bitmap has too few free
	// clusters for (VMS's SS$_DEVICEFULL).
	ErrNoSpace = errors.New("not enough free space")

	// ErrHeaderFull is a new header slot the index file can't grow to
	// reach because its header's map area has no room for another
	// retrieval pointer (VMS's SS$_HEADERFULL). VMS 7.1 never gives
	// INDEXF.SYS an extension header: on a volume whose free space was all
	// single blocks, its index file grew a block at a time until its map
	// held 76 pointers, 154 of its 155 words, and the next file's create
	// failed so (govax's testdata/err113, round 2's HDRFULL).
	ErrHeaderFull = errors.New("file header is full")

	// ErrIndexFileFull is a new file on a volume whose index file bitmap
	// has no free slot: it holds MaxFiles files (VMS's SS$_IDXFILEFULL).
	ErrIndexFileFull = errors.New("index file is full")
)
