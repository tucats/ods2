package volume

import "fmt"

// Bitmap returns dev's storage-bitmap cache (BITMAP.SYS — see the Bitmap
// type's own doc comment), opening it via OpenBitmap the first time it's
// asked for and returning that same instance on every later call within
// this mount session.
//
// This is the accessor write-path operations (file creation, extension,
// and so on) are expected to use to obtain a Bitmap to allocate from,
// rather than calling OpenBitmap directly: sharing one instance per device
// is what lets one operation's allocation be visible to the next within
// the same session (OpenBitmap on its own always re-reads the on-disk
// bitmap into a brand-new, independent instance, which is the right thing
// for a test that specifically wants an unaffected view of what has or
// hasn't actually reached disk, but the wrong thing for ordinary session
// use), and it's also what lets Dismount (below) find the cache afterward
// to flush it.
func (dev *Device) Bitmap() (*Bitmap, error) {
	if dev.bitmap == nil {
		bm, err := OpenBitmap(dev)
		if err != nil {
			return nil, err
		}
		dev.bitmap = bm
	}
	return dev.bitmap, nil
}

// IndexBitmap returns dev's index-file header-slot bitmap cache, following
// exactly the same open-once-and-reuse pattern as Bitmap above (see its
// doc comment) but for OpenIndexBitmap/IndexBitmap instead.
func (dev *Device) IndexBitmap() (*IndexBitmap, error) {
	if dev.indexBitmap == nil {
		ib, err := OpenIndexBitmap(dev)
		if err != nil {
			return nil, err
		}
		dev.indexBitmap = ib
	}
	return dev.indexBitmap, nil
}

// Dismount flushes every device's bitmap caches (see Device.Bitmap/
// IndexBitmap and docs/PHASE-02.md's "Caching strategy" section) and then
// closes every device's underlying container, releasing whatever
// operating-system resource (typically an open file handle) it holds.
//
// A volume that was never written to — mounted read-only in the first
// place, or mounted /WRITE but never actually used to allocate anything —
// never populates a Device's bitmap/indexBitmap fields at all (see their
// doc comment), so Dismount's flush step is a safe, cheap no-op for it;
// callers can call Dismount unconditionally on any mounted Volume without
// first checking how it was mounted or whether anything was ever written.
//
// Dismount attempts every device's flush and close even if an earlier one
// fails, so that a problem with one device in a multi-device volume set
// can't leave another device's already-pending writes stranded in memory,
// or its container needlessly left open. If anything went wrong, the
// first error encountered is returned.
func (vol *Volume) Dismount() error {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for _, dev := range vol.Devices {
		if dev.bitmap != nil {
			note(dev.bitmap.Flush())
		}
		if dev.indexBitmap != nil {
			note(dev.indexBitmap.Flush())
		}
	}

	for _, dev := range vol.Devices {
		note(dev.Container.Close())
	}

	if firstErr != nil {
		return fmt.Errorf("volume: dismount: %w", firstErr)
	}
	return nil
}
