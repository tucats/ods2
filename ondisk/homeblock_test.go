package ondisk

import (
	"encoding/binary"
	"testing"
)

// validHomeBlockBytes builds a syntactically valid, correctly-checksummed
// 512-byte home block, with every field set to a distinct, recognizable
// value so tests can verify each one was decoded from the right offset.
// The returned block is ready to hand to DecodeHomeBlock as-is.
func validHomeBlockBytes(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, BlockSize)

	binary.LittleEndian.PutUint32(b[homeOffHomeLBN:], 1)
	binary.LittleEndian.PutUint32(b[homeOffAlHomeLBN:], 2)
	binary.LittleEndian.PutUint32(b[homeOffAltIdxLBN:], 3)
	binary.LittleEndian.PutUint16(b[homeOffStrucLevel:], 0x0102)
	binary.LittleEndian.PutUint16(b[homeOffClusterSize:], 4)
	binary.LittleEndian.PutUint16(b[homeOffHomeVBN:], 1)
	binary.LittleEndian.PutUint16(b[homeOffAlHomeVBN:], 2)
	binary.LittleEndian.PutUint16(b[homeOffAltIdxVBN:], 3)
	binary.LittleEndian.PutUint16(b[homeOffIdxBitmapVBN:], 4)
	binary.LittleEndian.PutUint32(b[homeOffIdxBitmapLBN:], 5)
	binary.LittleEndian.PutUint32(b[homeOffMaxFiles:], 1000)
	binary.LittleEndian.PutUint16(b[homeOffIdxBitmapSize:], 6)
	binary.LittleEndian.PutUint16(b[homeOffReservedFiles:], 9)
	binary.LittleEndian.PutUint16(b[homeOffDeviceType:], 0)
	binary.LittleEndian.PutUint16(b[homeOffRvn:], 1)
	binary.LittleEndian.PutUint16(b[homeOffSetCount:], 1)
	binary.LittleEndian.PutUint16(b[homeOffVolChar:], 0)
	binary.LittleEndian.PutUint16(b[homeOffVolOwner:], 4)   // Uic.Member
	binary.LittleEndian.PutUint16(b[homeOffVolOwner+2:], 1) // Uic.Group
	binary.LittleEndian.PutUint16(b[homeOffProtection:], 0xFF00)
	binary.LittleEndian.PutUint16(b[homeOffFileProtection:], 0xFF00)
	binary.LittleEndian.PutUint16(b[homeOffChecksum1:], 0) // never validated; value is irrelevant

	binary.LittleEndian.PutUint64(b[homeOffCreationDate:], 0) // VMS epoch, 17-NOV-1858

	b[homeOffWindow] = 7
	b[homeOffLruLimit] = 8
	binary.LittleEndian.PutUint16(b[homeOffExtend:], 5)

	binary.LittleEndian.PutUint32(b[homeOffSerialNumber:], 0x12345678)
	copy(b[homeOffStrucName:homeOffStrucName+12], "DECFILE11B  ")
	copy(b[homeOffVolName:homeOffVolName+12], "MYVOLUME    ")
	copy(b[homeOffOwnerName:homeOffOwnerName+12], "PAULNANK    ")
	copy(b[homeOffFormat:homeOffFormat+12], "DECFILE11B  ")

	sum, err := Checksum(b)
	if err != nil {
		t.Fatalf("computing test fixture checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[homeOffChecksum2:], sum)

	return b
}

func TestDecodeHomeBlockValid(t *testing.T) {
	b := validHomeBlockBytes(t)

	h, err := DecodeHomeBlock(b)
	if err != nil {
		t.Fatalf("DecodeHomeBlock: %v", err)
	}

	if h.HomeLBN != 1 {
		t.Errorf("HomeLBN = %d, want 1", h.HomeLBN)
	}
	if h.ClusterSize != 4 {
		t.Errorf("ClusterSize = %d, want 4", h.ClusterSize)
	}
	if h.MaxFiles != 1000 {
		t.Errorf("MaxFiles = %d, want 1000", h.MaxFiles)
	}
	if h.VolumeOwner.Member != 4 || h.VolumeOwner.Group != 1 {
		t.Errorf("VolumeOwner = %+v, want {Member:4 Group:1}", h.VolumeOwner)
	}
	if h.VolumeName != "MYVOLUME" {
		t.Errorf("VolumeName = %q, want %q (trailing spaces should be trimmed)", h.VolumeName, "MYVOLUME")
	}
	if h.OwnerName != "PAULNANK" {
		t.Errorf("OwnerName = %q, want %q", h.OwnerName, "PAULNANK")
	}
	if h.Format != HomeBlockFormatID {
		t.Errorf("Format = %q, want %q", h.Format, HomeBlockFormatID)
	}
	if h.SerialNumber != 0x12345678 {
		t.Errorf("SerialNumber = %#x, want 0x12345678", h.SerialNumber)
	}
}

func TestDecodeHomeBlockBadFormat(t *testing.T) {
	b := validHomeBlockBytes(t)
	copy(b[homeOffFormat:homeOffFormat+12], "GARBAGE     ")
	// The checksum would now be wrong too, but the format check should be
	// what's reported, since it's checked first and is the more specific,
	// more useful diagnostic ("this isn't an ODS-2 volume at all" versus
	// "this block failed its checksum").
	sum, err := Checksum(b)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[homeOffChecksum2:], sum)

	if _, err := DecodeHomeBlock(b); err == nil {
		t.Fatal("DecodeHomeBlock with bad format identifier: want error, got nil")
	}
}

func TestDecodeHomeBlockBadChecksum(t *testing.T) {
	b := validHomeBlockBytes(t)
	// Corrupt one byte that isn't part of the checksum field itself.
	b[homeOffVolName] ^= 0xFF

	if _, err := DecodeHomeBlock(b); err == nil {
		t.Fatal("DecodeHomeBlock with corrupted data (checksum now wrong): want error, got nil")
	}
}

func TestDecodeHomeBlockWrongSize(t *testing.T) {
	if _, err := DecodeHomeBlock(make([]byte, BlockSize-1)); err == nil {
		t.Fatal("DecodeHomeBlock with wrong-size buffer: want error, got nil")
	}
}
