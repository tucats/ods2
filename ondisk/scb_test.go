package ondisk

import (
	"encoding/binary"
	"testing"
)

func validStorageControlBlockBytes(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, BlockSize)

	binary.LittleEndian.PutUint16(b[scbOffStrucLevel:], 0x0102)
	binary.LittleEndian.PutUint16(b[scbOffCluster:], 4)
	binary.LittleEndian.PutUint32(b[scbOffVolSize:], 1_000_000)
	binary.LittleEndian.PutUint32(b[scbOffBlkSize:], 512)
	binary.LittleEndian.PutUint32(b[scbOffSectors:], 63)
	binary.LittleEndian.PutUint32(b[scbOffTracks:], 255)
	binary.LittleEndian.PutUint32(b[scbOffCylinders:], 1024)
	binary.LittleEndian.PutUint16(b[scbOffWriteCount:], 42)
	copy(b[scbOffLockName:scbOffLockName+12], "MYVOLUME    ")
	binary.LittleEndian.PutUint64(b[scbOffGenerNum:], 0x0102030405060708)

	sum, err := Checksum(b)
	if err != nil {
		t.Fatalf("computing test fixture checksum: %v", err)
	}
	binary.LittleEndian.PutUint16(b[scbOffChecksum:], sum)

	return b
}

func TestDecodeStorageControlBlockValid(t *testing.T) {
	b := validStorageControlBlockBytes(t)

	s, err := DecodeStorageControlBlock(b)
	if err != nil {
		t.Fatalf("DecodeStorageControlBlock: %v", err)
	}

	if s.VolumeSize != 1_000_000 {
		t.Errorf("VolumeSize = %d, want 1000000", s.VolumeSize)
	}
	if s.ClusterSize != 4 {
		t.Errorf("ClusterSize = %d, want 4", s.ClusterSize)
	}
	if s.VolumeLockName != "MYVOLUME" {
		t.Errorf("VolumeLockName = %q, want %q", s.VolumeLockName, "MYVOLUME")
	}
	if s.GenerationNumber != 0x0102030405060708 {
		t.Errorf("GenerationNumber = %#x, want 0x0102030405060708", s.GenerationNumber)
	}
}

func TestDecodeStorageControlBlockBadChecksum(t *testing.T) {
	b := validStorageControlBlockBytes(t)
	b[scbOffVolSize] ^= 0xFF

	if _, err := DecodeStorageControlBlock(b); err == nil {
		t.Fatal("DecodeStorageControlBlock with corrupted data: want error, got nil")
	}
}

func TestDecodeStorageControlBlockWrongSize(t *testing.T) {
	if _, err := DecodeStorageControlBlock(make([]byte, BlockSize-1)); err == nil {
		t.Fatal("DecodeStorageControlBlock with wrong-size buffer: want error, got nil")
	}
}
