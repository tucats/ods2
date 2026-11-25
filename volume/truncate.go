package volume

import (
	"fmt"

	"github.com/tucats/ods2/diskimage"
	"github.com/tucats/ods2/ondisk"
)

// Truncate gives back the blocks of f past its first blocks virtual blocks,
// rounded up to a whole cluster: RMS's truncate on close (FAB$V_TEF), which
// VMS's DCL asks for when it closes a file it wrote (a file of one line keeps
// a cluster, however far it grew; govax's testdata/dcl50, lnm91, and ppf71).
// The freed extents go back to bm (in memory; see Bitmap.Flush), and f's
// header and map record its new size. A file whose map runs on into
// extension headers is left as it is, as is one no bigger than that already.
func Truncate(f *File, bm *Bitmap, blocks uint32) error {
	if !f.Header.ExtensionFid.IsZero() || bm.clusterSize == 0 {
		return nil
	}

	cluster := bm.clusterSize
	keep := (blocks + cluster - 1) / cluster * cluster

	if keep >= f.Header.RecordAttributes.HighestBlock {
		return nil
	}

	container, ok := f.Device.Container.(diskimage.WritableContainer)
	if !ok {
		return fmt.Errorf("volume: truncating file %v: device is not open for write", f.Header.Fid)
	}

	pointers, err := f.Header.RetrievalPointers()
	if err != nil {
		return fmt.Errorf("volume: truncating file %v: %w", f.Header.Fid, err)
	}

	var kept, freed []ondisk.Extent

	left := keep

	for _, e := range pointers {
		switch {
		case left == 0:
			freed = append(freed, e)
		case e.Count <= left:
			kept = append(kept, e)
			left -= e.Count
		default:
			kept = append(kept, ondisk.Extent{StartLBN: e.StartLBN, Count: left})
			freed = append(freed, ondisk.Extent{StartLBN: e.StartLBN + left, Count: e.Count - left})
			left = 0
		}
	}

	areas, err := existingAreas(f.Header)
	if err != nil {
		return fmt.Errorf("volume: truncating file %v: %w", f.Header.Fid, err)
	}

	if areas.MapBytes, err = ondisk.EncodeRetrievalPointers(kept); err != nil {
		return fmt.Errorf("volume: truncating file %v: %w", f.Header.Fid, err)
	}

	h := f.Header
	h.RecordAttributes.HighestBlock = keep
	h.HighWaterMark = min(h.HighWaterMark, keep+1)

	decoded, err := writeHeader(f.Device, container, h.Fid.Number(), h, areas)
	if err != nil {
		return fmt.Errorf("volume: truncating file %v: %w", f.Header.Fid, err)
	}

	for _, e := range freed {
		if err := bm.MarkFree(e); err != nil {
			return fmt.Errorf("volume: truncating file %v: freeing its blocks: %w", f.Header.Fid, err)
		}
	}

	f.Header = decoded
	f.Extents = f.Extents[:0]

	for _, e := range kept {
		f.Extents = append(f.Extents, ExtentLocation{Extent: e, Rvn: f.Device.Rvn})
	}

	return nil
}
