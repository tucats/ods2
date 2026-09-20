package ondisk

import (
	"encoding/binary"
	"fmt"

	"github.com/tucats/ods2/vmstime"
)

// StorageControlBlock (SCB) is the first block of a volume's storage
// bitmap file, BITMAP.SYS, which — despite its name — describes the
// volume's overall physical geometry and free-space bookkeeping, not just
// the bitmap itself (the actual free/allocated bits follow in the blocks
// after this one). It's mainly useful for reporting a volume's total and
// physical capacity; this project's read-only file access doesn't need to
// consult it at all, since RetrievalPointers already says exactly which
// blocks a file occupies without needing to know the volume's total size.
type StorageControlBlock struct {
	StructureLevel uint16
	ClusterSize    uint16 // must match HomeBlock.ClusterSize
	VolumeSize     uint32 // total number of blocks on the volume
	BlockSize      uint32 // bytes per block, normally 512
	Sectors        uint32 // physical sectors per track
	Tracks         uint32 // physical tracks per cylinder
	Cylinders      uint32 // physical cylinders on the device
	Status         uint32
	Status2        uint32
	WriteCount     uint16 // incremented each time the volume is mounted for write
	VolumeLockName string
	MountTime      vmstime.VMSTime
	BackupRevision uint16

	// GenerationNumber is a 64-bit counter, stored on disk as two
	// consecutive 32-bit little-endian words; decoded here as a single
	// little-endian 64-bit value (i.e. NOT using the "swapped longword"
	// convention that RecAttr's Hiblk/Efblk fields use — nothing in the
	// reference implementation suggests this field needs that transform).
	GenerationNumber uint64

	// Checksum is the checksum of the whole block (see Checksum), computed
	// over everything except this field itself.
	Checksum uint16
}

// Byte offsets of each StorageControlBlock field within its 512-byte
// on-disk block.
const (
	scbOffStrucLevel = 0
	scbOffCluster    = 2
	scbOffVolSize    = 4
	scbOffBlkSize    = 8
	scbOffSectors    = 12
	scbOffTracks     = 16
	scbOffCylinders  = 20
	scbOffStatus     = 24
	scbOffStatus2    = 28
	scbOffWriteCount = 32
	scbOffLockName   = 34 // 12 bytes
	scbOffMountTime  = 46 // 8 bytes
	scbOffBackupRev  = 54
	scbOffGenerNum   = 56 // 8 bytes (two 32-bit words)
	// 446 reserved bytes at offset 64
	scbOffChecksum = 510
)

// DecodeStorageControlBlock decodes a StorageControlBlock from its 512-byte
// on-disk representation and validates its checksum.
func DecodeStorageControlBlock(b []byte) (StorageControlBlock, error) {
	if len(b) != BlockSize {
		return StorageControlBlock{}, fmt.Errorf("ondisk: StorageControlBlock requires exactly %d bytes, got %d", BlockSize, len(b))
	}

	s := StorageControlBlock{
		StructureLevel:   binary.LittleEndian.Uint16(b[scbOffStrucLevel:]),
		ClusterSize:      binary.LittleEndian.Uint16(b[scbOffCluster:]),
		VolumeSize:       binary.LittleEndian.Uint32(b[scbOffVolSize:]),
		BlockSize:        binary.LittleEndian.Uint32(b[scbOffBlkSize:]),
		Sectors:          binary.LittleEndian.Uint32(b[scbOffSectors:]),
		Tracks:           binary.LittleEndian.Uint32(b[scbOffTracks:]),
		Cylinders:        binary.LittleEndian.Uint32(b[scbOffCylinders:]),
		Status:           binary.LittleEndian.Uint32(b[scbOffStatus:]),
		Status2:          binary.LittleEndian.Uint32(b[scbOffStatus2:]),
		WriteCount:       binary.LittleEndian.Uint16(b[scbOffWriteCount:]),
		VolumeLockName:   decodePaddedString(b[scbOffLockName : scbOffLockName+12]),
		MountTime:        decodeVMSTime(b[scbOffMountTime:]),
		BackupRevision:   binary.LittleEndian.Uint16(b[scbOffBackupRev:]),
		GenerationNumber: binary.LittleEndian.Uint64(b[scbOffGenerNum:]),
		Checksum:         binary.LittleEndian.Uint16(b[scbOffChecksum:]),
	}

	sum, err := Checksum(b)
	if err != nil {
		// Unreachable given the length check above.
		return s, err
	}
	if sum != s.Checksum {
		return s, fmt.Errorf("ondisk: storage control block checksum mismatch: computed %#04x, stored %#04x", sum, s.Checksum)
	}

	return s, nil
}
