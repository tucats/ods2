package volume

import (
	"errors"
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// This file is how several users share one open file (docs/PHASE-05.md):
// the access arbitration VMS's file system does when a file is opened,
// and the one in-memory copy of an open file's header that every user of
// it reads and changes.
//
// # Accessing a file
//
// On VMS, opening a file is "accessing" it (the IO$_ACCESS function of
// the file system's ACP or XQP). Each accessor says whether it will write
// the file, and whether it will let anyone else read it (NoRead clear) or
// write it (NoWrite clear). The file system keeps, per open file, a count
// of its accessors, of its writers, and of the accessors denying reading
// or writing to others, and refuses an access that conflicts with them
// (SS$_ACCONFLICT; RMS reports it as RMS$_FLK, "file currently locked by
// another user").
//
// # One file control block
//
// VMS keeps one file control block (FCB) per open file, which every
// accessor shares: the file's header, its map of extents, its end of file.
// Here the FCB is the File itself: while a file is accessed, every Access
// of it, and every OpenFID of its file ID, returns the same *File, so an
// extension, an end of file moved, or a header field changed through one
// is what all of them see. The header goes back to disk when it changes
// (Extend, UpdateHeader, WriteAttributes) and when the last accessor
// deaccesses it. A file nobody has accessed has no shared File: OpenFID
// reads its header from the disk, which is then current.
//
// # Deleting an open file
//
// A file deleted while it's accessed (DeleteFile, DeleteHeader) loses its
// directory entry at once, as VMS's does, but keeps its header and blocks
// until its last accessor deaccesses it: "marked for delete". Its
// accessors can go on reading and writing it meanwhile.

// AccessMode is how one accessor uses a file: Write if it will write it,
// NoRead if no one else may read it while it has it, NoWrite if no one
// else may write it. Every accessor may read.
type AccessMode struct {
	Write   bool
	NoRead  bool
	NoWrite bool
}

// ErrAccessConflict is the error Access wraps when the access asked for
// conflicts with the file's current accessors (VMS's SS$_ACCONFLICT).
var ErrAccessConflict = errors.New("file is accessed in a conflicting way")

// ErrMarkedForDelete is the error Access wraps for a file that's been
// deleted while open, and only waits for its accessors to go (VMS
// refuses to access such a file: SS$_NOSUCHFILE).
var ErrMarkedForDelete = errors.New("file is marked for delete")

// Access is one accessor's hold on a file: what Volume.Access returns,
// and what Deaccess ends.
type Access struct {
	// File is the shared, open file.
	File *File

	// Mode is how this accessor uses it.
	Mode AccessMode

	done bool
}

// sharing is a File's accessor counts: how many accessors it has, how
// many of them write, and how many deny reading or writing to others.
// Only a File in its Device's accessed table has any.
type sharing struct {
	accessors, writers, noRead, noWrite int

	// deletePending is set when the file was deleted while accessed: its
	// storage is freed by the last Deaccess.
	deletePending bool

	// attributesChanged is set when SetEndOfFile changed the header in
	// memory without writing it.
	attributesChanged bool
}

// conflicts reports whether an accessor using mode m can join s's: it
// can't if someone denies reading (every accessor reads), if it would
// write and someone denies writing, if it would deny reading and anyone
// has the file, or if it would deny writing and someone writes.
func (s *sharing) conflicts(m AccessMode) bool {
	return s.noRead > 0 ||
		m.Write && s.noWrite > 0 ||
		m.NoRead && s.accessors > 0 ||
		m.NoWrite && s.writers > 0
}

// add counts an accessor using m; remove uncounts it.
func (s *sharing) add(m AccessMode, delta int) {
	s.accessors += delta

	if m.Write {
		s.writers += delta
	}

	if m.NoRead {
		s.noRead += delta
	}

	if m.NoWrite {
		s.noWrite += delta
	}
}

// accessedFile returns the shared File for fid on dev, if it's accessed.
// A File whose sequence number doesn't match fid's is a different file
// that once had the same number: not it.
func (dev *Device) accessedFile(fid ondisk.Fid) (*File, bool) {
	f, ok := dev.accessed[fid.Number()]
	if !ok || f.Header.Fid.Seq != fid.Seq {
		return nil, false
	}

	return f, true
}

// Accessed reports whether the file with ID fid is accessed now (open, by
// Volume.Access).
func (vol *Volume) Accessed(fid ondisk.Fid) bool {
	dev, err := vol.deviceByRvn(fid.Rvn)
	if err != nil {
		return false
	}

	_, ok := dev.accessedFile(fid)

	return ok
}

// Access opens the file with ID fid for an accessor using mode. It
// fails, wrapping ErrAccessConflict, if mode conflicts with the file's
// other accessors (see AccessMode), and wrapping ErrMarkedForDelete if
// the file has been deleted while open. An accessor that writes gets the
// File armed for writing (OpenForWrite), with the device's own bitmaps.
//
// Every Access must be ended by Deaccess.
func (vol *Volume) Access(fid ondisk.Fid, mode AccessMode) (*Access, error) {
	dev, err := vol.deviceByRvn(fid.Rvn)
	if err != nil {
		return nil, fmt.Errorf("volume: accessing file %v: %w", fid, err)
	}

	f, open := dev.accessedFile(fid)
	if !open {
		if f, err = vol.OpenFID(fid); err != nil {
			return nil, err
		}
	}

	return dev.access(f, mode)
}

// AccessFile is Access for a File already in hand: one CreateFile just
// made, typically, so that its creator is its first accessor. f must be
// the shared File if the file is already accessed (any File from OpenFID
// or Access is).
func (vol *Volume) AccessFile(f *File, mode AccessMode) (*Access, error) {
	if shared, ok := f.Device.accessedFile(f.Header.Fid); ok && shared != f {
		return nil, fmt.Errorf("volume: accessing file %v: not the file's shared File", f.Header.Fid)
	}

	return f.Device.access(f, mode)
}

// access adds an accessor using mode to f, entering f in dev's accessed
// table if it's the first.
func (dev *Device) access(f *File, mode AccessMode) (*Access, error) {
	if f.share.deletePending {
		return nil, fmt.Errorf("volume: accessing file %v: %w", f.Header.Fid, ErrMarkedForDelete)
	}

	if f.share.conflicts(mode) {
		return nil, fmt.Errorf("volume: accessing file %v: %w", f.Header.Fid, ErrAccessConflict)
	}

	if mode.Write && f.bm == nil {
		bm, err := dev.Bitmap()
		if err != nil {
			return nil, fmt.Errorf("volume: accessing file %v: %w", f.Header.Fid, err)
		}

		ib, err := dev.IndexBitmap()
		if err != nil {
			return nil, fmt.Errorf("volume: accessing file %v: %w", f.Header.Fid, err)
		}

		if err := f.OpenForWrite(bm, ib); err != nil {
			return nil, err
		}
	}

	if dev.accessed == nil {
		dev.accessed = map[uint32]*File{}
	}

	dev.accessed[f.Header.Fid.Number()] = f
	f.share.add(mode, 1)

	return &Access{File: f, Mode: mode}, nil
}

// Deaccess ends a's access. When it's the file's last, the file leaves
// the shared table: a file marked for delete is deleted now (its header
// and blocks freed), and otherwise a file that was written has its header
// written back (end of file and high-water mark), as WriteAttributes
// does. Deaccessing again does nothing.
func (a *Access) Deaccess() error {
	if a.done {
		return nil
	}

	a.done = true
	f := a.File
	f.share.add(a.Mode, -1)

	if f.share.accessors > 0 {
		return nil
	}

	dev := f.Device
	delete(dev.accessed, f.Header.Fid.Number())

	if f.share.deletePending {
		f.share = sharing{}

		bm, err := dev.Bitmap()
		if err != nil {
			return err
		}

		ib, err := dev.IndexBitmap()
		if err != nil {
			return err
		}

		f.bm, f.ib = nil, nil

		if err := freeFileStorage(dev, f.Header, bm, ib); err != nil {
			return fmt.Errorf("volume: deleting file %v at its last deaccess: %w", f.Header.Fid, err)
		}

		return nil
	}

	var err error
	if f.bm != nil || f.share.attributesChanged {
		err = f.WriteAttributes()
	}

	f.share = sharing{}
	f.bm, f.ib = nil, nil

	return err
}

// MarkedForDelete reports whether f was deleted while accessed, and
// waits for its last Deaccess to go.
func (f *File) MarkedForDelete() bool {
	return f.share.deletePending
}

// Accessors is how many accessors f has.
func (f *File) Accessors() int {
	return f.share.accessors
}

// markForDelete marks the accessed file fid, if it is one, for deletion
// at its last deaccess, reporting whether it was.
func (dev *Device) markForDelete(fid ondisk.Fid) bool {
	f, ok := dev.accessedFile(fid)
	if ok {
		f.share.deletePending = true
	}

	return ok
}

// SetEndOfFile records, in f's header in memory, that its data ends at
// byte ffb of virtual block ebk (ebk 0: no data; ffb 0 with ebk N: the
// data ends exactly at the end of block N-1, the convention CloseWithFinalByte
// and Directory.recordUsedBlocks use), moving the high-water mark past
// it. Everyone sharing f sees it at once; the header is written by
// WriteAttributes, or by the last Deaccess.
func (f *File) SetEndOfFile(ebk uint32, ffb uint16) {
	f.Header.RecordAttributes.EndOfFileBlock = ebk
	f.Header.RecordAttributes.FirstFreeByte = ffb

	// The high-water mark is the first block never written: one past the
	// last block holding data.
	last := ebk
	if ffb == 0 && last > 0 {
		last--
	}

	if f.Header.HighWaterMark < last+1 {
		f.Header.HighWaterMark = last + 1
	}

	f.share.attributesChanged = true
}

// WriteAttributes writes f's header, as it is in memory, back to the
// disk: what changed by SetEndOfFile and WriteBlock (the end of file and
// the high-water mark) since the header was last written. (Extend and
// UpdateHeader write the header themselves.)
func (f *File) WriteAttributes() error {
	container, ok := f.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: writing the attributes of file %v: device is not open for write", f.Header.Fid)
	}

	areas, err := existingAreas(f.Header)
	if err != nil {
		return fmt.Errorf("volume: writing the attributes of file %v: %w", f.Header.Fid, err)
	}

	decoded, err := writeHeader(f.Device, container, f.Header.Fid.Number(), f.Header, areas)
	if err != nil {
		return fmt.Errorf("volume: writing the attributes of file %v: %w", f.Header.Fid, err)
	}

	f.Header = decoded
	f.share.attributesChanged = false

	return nil
}
