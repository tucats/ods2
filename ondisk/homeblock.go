package ondisk

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/tucats/ods2/vmstime"
)

// HomeBlockFormatID is the fixed identifier every valid ODS-2 ("Files-11
// On-Disk Structure Level 2") volume must carry in its home block's Format
// field. Checking for it is how mounting code confirms a container really
// holds an ODS-2 volume, rather than, say, an ODS-1 volume, an unrelated
// filesystem, or unrelated/corrupted data. On disk the field is actually
// 12 bytes wide and space-padded ("DECFILE11B  "); DecodeHomeBlock trims
// the trailing spaces before comparing, so this constant is written
// without them.
const HomeBlockFormatID = "DECFILE11B"

// HomeBlock is the volume home block: a single 512-byte block, present on
// every ODS-2 volume, that describes the volume as a whole — its name,
// owner, size limits, and, critically, where to find the volume's index
// file (INDEXF.SYS), the file that in turn describes every other file on
// the volume. Mounting a volume starts by locating and validating its home
// block; see package volume.
//
// Most fields here directly mirror the on-disk layout (and its original
// field names, for anyone cross-referencing the reference C source or
// other ODS-2 documentation); a few purely-reserved byte ranges from the
// on-disk structure are intentionally not represented here, since they
// carry no defined meaning.
type HomeBlock struct {
	// HomeLBN is the logical block number of THIS home block. A
	// legitimate home block's HomeLBN must equal the LBN it was actually
	// read from — otherwise it's either the wrong block or corrupted data.
	// (DecodeHomeBlock does not check this itself, since it doesn't know
	// what LBN its input came from; package volume performs that check
	// while searching for the home block.)
	HomeLBN uint32

	// AlternateHomeLBN and AlternateIndexLBN point to backup copies of the
	// home block and the index file's first header, for recovery if the
	// primary copies are damaged. The reference implementation this
	// project is based on never actually reads these alternates, and
	// neither does this port (yet) — they're decoded here for
	// completeness, since a library consumer might want to inspect them.
	AlternateHomeLBN  uint32
	AlternateIndexLBN uint32

	// StructureLevel is the on-disk structure level/version, split into two
	// bytes the same way FileHeaderStructureLevel is: the high byte is the
	// structure level (2, for "ODS-2"), the low byte the version within it
	// -- so a volume's home block conventionally carries the same value,
	// 0x0201 (513 decimal), that every one of its file headers does.
	// Confirmed directly against testdata/rq0-ra92.dsk (a real OpenVMS
	// volume): its home block's raw bytes at this field are `01 02`, which
	// decode little-endian to 0x0201, not the reversed 0x0102 an earlier
	// pass at this comment mistakenly claimed.
	StructureLevel uint16
	ClusterSize    uint16 // disk allocation unit, in blocks: space is always allocated in groups of this many blocks

	HomeVBN           uint16 // this home block's own position (virtual block number) within INDEXF.SYS
	AlternateHomeVBN  uint16
	AlternateIndexVBN uint16

	IndexBitmapVBN  uint16 // where the storage (free-space) bitmap starts, as a VBN within INDEXF.SYS
	IndexBitmapLBN  uint32 // where the storage bitmap starts, as an absolute LBN on the device
	MaxFiles        uint32 // maximum number of files the volume can hold (= number of header slots in INDEXF.SYS)
	IndexBitmapSize uint16 // size of the storage bitmap, in blocks
	ReservedFiles   uint16 // number of file header slots reserved for the volume's own bookkeeping files

	DeviceType            uint16
	RelativeVolumeNumber  uint16 // this disk's position within a multi-disk volume set (1-based; 0 or 1 for a single-disk volume)
	VolumeSetCount        uint16 // total number of disks in the volume set
	VolumeCharacteristics uint16
	VolumeOwner           Uic

	Protection     uint16
	FileProtection uint16 // default protection mask applied to newly-created files
	Checksum1      uint16 // a legacy secondary checksum; not validated by this package or its reference implementation

	CreationDate vmstime.VMSTime

	WindowSize              uint8 // suggested retrieval-pointer window size (a performance hint; not needed by this project's simpler mapping approach)
	DirectoryPreAccessLimit uint8
	DefaultExtendSize       uint16

	RetentionMin vmstime.VMSTime
	RetentionMax vmstime.VMSTime
	RevisionDate vmstime.VMSTime

	MinSecurityClass [20]byte
	MaxSecurityClass [20]byte

	SerialNumber uint32

	StructureName string // e.g. "DECFILE11B" restated as a human-readable label
	VolumeName    string // the volume label, e.g. what a MOUNT command displays
	OwnerName     string
	Format        string // should equal HomeBlockFormatID for a valid ODS-2 volume

	// Checksum2 is the checksum of the whole block (see Checksum),
	// computed over everything except this field itself.
	Checksum2 uint16
}

// Byte offsets of each HomeBlock field within its 512-byte on-disk block.
// These are private: nothing outside DecodeHomeBlock needs to know a
// field's raw byte position, only its decoded Go value.
const (
	homeOffHomeLBN       = 0
	homeOffAlHomeLBN     = 4
	homeOffAltIdxLBN     = 8
	homeOffStrucLevel    = 12
	homeOffClusterSize   = 14
	homeOffHomeVBN       = 16
	homeOffAlHomeVBN     = 18
	homeOffAltIdxVBN     = 20
	homeOffIdxBitmapVBN  = 22
	homeOffIdxBitmapLBN  = 24
	homeOffMaxFiles      = 28
	homeOffIdxBitmapSize = 32
	homeOffReservedFiles = 34
	homeOffDeviceType    = 36
	homeOffRvn           = 38
	homeOffSetCount      = 40
	homeOffVolChar       = 42
	homeOffVolOwner      = 44 // 4 bytes (Uic)
	// 4 reserved bytes at offset 48
	homeOffProtection     = 52
	homeOffFileProtection = 54
	// 2 reserved bytes at offset 56
	homeOffChecksum1    = 58
	homeOffCreationDate = 60 // 8 bytes
	homeOffWindow       = 68
	homeOffLruLimit     = 69
	homeOffExtend       = 70
	homeOffRetainMin    = 72  // 8 bytes
	homeOffRetainMax    = 80  // 8 bytes
	homeOffRevDate      = 88  // 8 bytes
	homeOffMinClass     = 96  // 20 bytes
	homeOffMaxClass     = 116 // 20 bytes
	// 320 reserved bytes at offset 136
	homeOffSerialNumber = 456
	homeOffStrucName    = 460 // 12 bytes
	homeOffVolName      = 472 // 12 bytes
	homeOffOwnerName    = 484 // 12 bytes
	homeOffFormat       = 496 // 12 bytes
	// 2 reserved bytes at offset 508
	homeOffChecksum2 = 510
)

// decodeVMSTime decodes an 8-byte VMS quadword timestamp. On disk it's a
// signed 64-bit integer stored little-endian, same as every other
// multi-byte field in this format.
func decodeVMSTime(b []byte) vmstime.VMSTime {
	return vmstime.VMSTime(int64(binary.LittleEndian.Uint64(b)))
}

// encodeVMSTime writes an 8-byte VMS quadword timestamp into b (which must
// be at least 8 bytes), the exact inverse of decodeVMSTime.
func encodeVMSTime(b []byte, t vmstime.VMSTime) {
	binary.LittleEndian.PutUint64(b, uint64(int64(t)))
}

// decodePaddedString decodes a fixed-width, space-padded ASCII text field
// (VMS pads names and labels with trailing spaces to fill their allotted
// width) and trims the padding, since Go code almost always wants the
// trimmed form.
func decodePaddedString(b []byte) string {
	return strings.TrimRight(string(b), " ")
}

// encodePaddedString writes s into dst (its full fixed on-disk width),
// space-padding whatever's left, the exact inverse of decodePaddedString.
// It's an error for s to be longer than dst, since there's no defined way
// to truncate a name or label without silently losing data the caller
// almost certainly cares about.
func encodePaddedString(dst []byte, s string) error {
	if len(s) > len(dst) {
		return fmt.Errorf("value %q is %d bytes, want at most %d", s, len(s), len(dst))
	}
	n := copy(dst, s)
	for ; n < len(dst); n++ {
		dst[n] = ' '
	}
	return nil
}

// DecodeHomeBlock decodes a HomeBlock from its 512-byte on-disk
// representation and validates it: the Format field must read
// HomeBlockFormatID, and the block's checksum must be self-consistent.
// Both checks together confirm this is genuinely an intact ODS-2 home
// block, though not necessarily the RIGHT one — DecodeHomeBlock has no way
// to know what LBN b was read from, so it cannot check HomeLBN
// self-consistency (that HomeLBN equals the block's own location); package
// volume performs that check as part of searching for a volume's home
// block.
//
// The decoded HomeBlock is returned even when validation fails, in case a
// caller wants to inspect it anyway (for diagnostics, say); check the
// returned error to know whether it's trustworthy.
func DecodeHomeBlock(b []byte) (HomeBlock, error) {
	if len(b) != BlockSize {
		return HomeBlock{}, fmt.Errorf("ondisk: HomeBlock requires exactly %d bytes, got %d", BlockSize, len(b))
	}

	owner, err := DecodeUic(b[homeOffVolOwner:])
	if err != nil {
		return HomeBlock{}, fmt.Errorf("ondisk: decoding HomeBlock volume owner: %w", err)
	}

	h := HomeBlock{
		HomeLBN:                 binary.LittleEndian.Uint32(b[homeOffHomeLBN:]),
		AlternateHomeLBN:        binary.LittleEndian.Uint32(b[homeOffAlHomeLBN:]),
		AlternateIndexLBN:       binary.LittleEndian.Uint32(b[homeOffAltIdxLBN:]),
		StructureLevel:          binary.LittleEndian.Uint16(b[homeOffStrucLevel:]),
		ClusterSize:             binary.LittleEndian.Uint16(b[homeOffClusterSize:]),
		HomeVBN:                 binary.LittleEndian.Uint16(b[homeOffHomeVBN:]),
		AlternateHomeVBN:        binary.LittleEndian.Uint16(b[homeOffAlHomeVBN:]),
		AlternateIndexVBN:       binary.LittleEndian.Uint16(b[homeOffAltIdxVBN:]),
		IndexBitmapVBN:          binary.LittleEndian.Uint16(b[homeOffIdxBitmapVBN:]),
		IndexBitmapLBN:          binary.LittleEndian.Uint32(b[homeOffIdxBitmapLBN:]),
		MaxFiles:                binary.LittleEndian.Uint32(b[homeOffMaxFiles:]),
		IndexBitmapSize:         binary.LittleEndian.Uint16(b[homeOffIdxBitmapSize:]),
		ReservedFiles:           binary.LittleEndian.Uint16(b[homeOffReservedFiles:]),
		DeviceType:              binary.LittleEndian.Uint16(b[homeOffDeviceType:]),
		RelativeVolumeNumber:    binary.LittleEndian.Uint16(b[homeOffRvn:]),
		VolumeSetCount:          binary.LittleEndian.Uint16(b[homeOffSetCount:]),
		VolumeCharacteristics:   binary.LittleEndian.Uint16(b[homeOffVolChar:]),
		VolumeOwner:             owner,
		Protection:              binary.LittleEndian.Uint16(b[homeOffProtection:]),
		FileProtection:          binary.LittleEndian.Uint16(b[homeOffFileProtection:]),
		Checksum1:               binary.LittleEndian.Uint16(b[homeOffChecksum1:]),
		CreationDate:            decodeVMSTime(b[homeOffCreationDate:]),
		WindowSize:              b[homeOffWindow],
		DirectoryPreAccessLimit: b[homeOffLruLimit],
		DefaultExtendSize:       binary.LittleEndian.Uint16(b[homeOffExtend:]),
		RetentionMin:            decodeVMSTime(b[homeOffRetainMin:]),
		RetentionMax:            decodeVMSTime(b[homeOffRetainMax:]),
		RevisionDate:            decodeVMSTime(b[homeOffRevDate:]),
		SerialNumber:            binary.LittleEndian.Uint32(b[homeOffSerialNumber:]),
		StructureName:           decodePaddedString(b[homeOffStrucName : homeOffStrucName+12]),
		VolumeName:              decodePaddedString(b[homeOffVolName : homeOffVolName+12]),
		OwnerName:               decodePaddedString(b[homeOffOwnerName : homeOffOwnerName+12]),
		Format:                  decodePaddedString(b[homeOffFormat : homeOffFormat+12]),
		Checksum2:               binary.LittleEndian.Uint16(b[homeOffChecksum2:]),
	}
	copy(h.MinSecurityClass[:], b[homeOffMinClass:homeOffMinClass+20])
	copy(h.MaxSecurityClass[:], b[homeOffMaxClass:homeOffMaxClass+20])

	if h.Format != HomeBlockFormatID {
		return h, fmt.Errorf("ondisk: not an ODS-2 home block: format identifier is %q, want %q", h.Format, HomeBlockFormatID)
	}

	sum, err := Checksum(b)
	if err != nil {
		// Unreachable given the length check above, but handled instead
		// of ignored so a future refactor can't silently swallow it.
		return h, err
	}
	if sum != h.Checksum2 {
		return h, fmt.Errorf("ondisk: home block checksum mismatch: computed %#04x, stored %#04x", sum, h.Checksum2)
	}

	return h, nil
}

// EncodeHomeBlock encodes h into its 512-byte on-disk representation,
// computing Checksum2 (see Checksum) from the rest of the block —
// whatever value h.Checksum2 itself holds is ignored, since a checksum
// only makes sense as the OUTPUT of encoding the rest of the block, never
// an input to it.
//
// The only way this can fail is a text field (StructureName, VolumeName,
// OwnerName, or Format) too long for its fixed 12-byte on-disk width;
// every other field is a fixed-width numeric value with no validity
// constraint this package enforces.
func EncodeHomeBlock(h HomeBlock) ([]byte, error) {
	b := make([]byte, BlockSize)

	binary.LittleEndian.PutUint32(b[homeOffHomeLBN:], h.HomeLBN)
	binary.LittleEndian.PutUint32(b[homeOffAlHomeLBN:], h.AlternateHomeLBN)
	binary.LittleEndian.PutUint32(b[homeOffAltIdxLBN:], h.AlternateIndexLBN)
	binary.LittleEndian.PutUint16(b[homeOffStrucLevel:], h.StructureLevel)
	binary.LittleEndian.PutUint16(b[homeOffClusterSize:], h.ClusterSize)
	binary.LittleEndian.PutUint16(b[homeOffHomeVBN:], h.HomeVBN)
	binary.LittleEndian.PutUint16(b[homeOffAlHomeVBN:], h.AlternateHomeVBN)
	binary.LittleEndian.PutUint16(b[homeOffAltIdxVBN:], h.AlternateIndexVBN)
	binary.LittleEndian.PutUint16(b[homeOffIdxBitmapVBN:], h.IndexBitmapVBN)
	binary.LittleEndian.PutUint32(b[homeOffIdxBitmapLBN:], h.IndexBitmapLBN)
	binary.LittleEndian.PutUint32(b[homeOffMaxFiles:], h.MaxFiles)
	binary.LittleEndian.PutUint16(b[homeOffIdxBitmapSize:], h.IndexBitmapSize)
	binary.LittleEndian.PutUint16(b[homeOffReservedFiles:], h.ReservedFiles)
	binary.LittleEndian.PutUint16(b[homeOffDeviceType:], h.DeviceType)
	binary.LittleEndian.PutUint16(b[homeOffRvn:], h.RelativeVolumeNumber)
	binary.LittleEndian.PutUint16(b[homeOffSetCount:], h.VolumeSetCount)
	binary.LittleEndian.PutUint16(b[homeOffVolChar:], h.VolumeCharacteristics)
	copy(b[homeOffVolOwner:homeOffVolOwner+UicSize], EncodeUic(h.VolumeOwner))
	binary.LittleEndian.PutUint16(b[homeOffProtection:], h.Protection)
	binary.LittleEndian.PutUint16(b[homeOffFileProtection:], h.FileProtection)
	binary.LittleEndian.PutUint16(b[homeOffChecksum1:], h.Checksum1)
	encodeVMSTime(b[homeOffCreationDate:], h.CreationDate)
	b[homeOffWindow] = h.WindowSize
	b[homeOffLruLimit] = h.DirectoryPreAccessLimit
	binary.LittleEndian.PutUint16(b[homeOffExtend:], h.DefaultExtendSize)
	encodeVMSTime(b[homeOffRetainMin:], h.RetentionMin)
	encodeVMSTime(b[homeOffRetainMax:], h.RetentionMax)
	encodeVMSTime(b[homeOffRevDate:], h.RevisionDate)
	copy(b[homeOffMinClass:homeOffMinClass+20], h.MinSecurityClass[:])
	copy(b[homeOffMaxClass:homeOffMaxClass+20], h.MaxSecurityClass[:])
	binary.LittleEndian.PutUint32(b[homeOffSerialNumber:], h.SerialNumber)

	for _, f := range []struct {
		name string
		dst  []byte
		val  string
	}{
		{"StructureName", b[homeOffStrucName : homeOffStrucName+12], h.StructureName},
		{"VolumeName", b[homeOffVolName : homeOffVolName+12], h.VolumeName},
		{"OwnerName", b[homeOffOwnerName : homeOffOwnerName+12], h.OwnerName},
		{"Format", b[homeOffFormat : homeOffFormat+12], h.Format},
	} {
		if err := encodePaddedString(f.dst, f.val); err != nil {
			return nil, fmt.Errorf("ondisk: encoding HomeBlock.%s: %w", f.name, err)
		}
	}

	sum, err := Checksum(b)
	if err != nil {
		// Unreachable given b's fixed length above.
		return nil, err
	}
	binary.LittleEndian.PutUint16(b[homeOffChecksum2:], sum)

	return b, nil
}
