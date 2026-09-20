package ondisk

import "testing"

func TestChecksum(t *testing.T) {
	block := make([]byte, BlockSize)
	// Put a distinct, easy-to-hand-verify value in the first three words
	// and leave the rest zero. Expected sum = 1 + 2 + 3 = 6.
	block[0], block[1] = 0x01, 0x00 // word 0 = 1
	block[2], block[3] = 0x02, 0x00 // word 1 = 2
	block[4], block[5] = 0x03, 0x00 // word 2 = 3

	got, err := Checksum(block)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if got != 6 {
		t.Errorf("Checksum = %d, want 6", got)
	}
}

func TestChecksumIgnoresLastWord(t *testing.T) {
	// The last 16-bit word of the block (bytes 510-511) is where the
	// checksum value itself is stored, so it must never be included in the
	// sum — otherwise a valid checksum could never be computed at all
	// (the field would depend on itself).
	block := make([]byte, BlockSize)
	block[0], block[1] = 0x01, 0x00 // word 0 = 1, included

	withLastWordZero, err := Checksum(block)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	block[510], block[511] = 0xFF, 0xFF // set the last word to something huge
	withLastWordSet, err := Checksum(block)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}

	if withLastWordZero != withLastWordSet {
		t.Errorf("Checksum changed when only the last word changed: %d vs %d", withLastWordZero, withLastWordSet)
	}
}

func TestChecksumOverflowsWithoutWraparound(t *testing.T) {
	// Fill every one of the 255 summed words with 0xFFFF. The true sum is
	// 255*0xFFFF = 0xFEFF01, which is larger than 16 bits — Checksum
	// should just keep the low 16 bits of that (0xFF01), not clamp or
	// saturate the value.
	block := make([]byte, BlockSize)
	for i := 0; i < 255; i++ {
		block[i*2], block[i*2+1] = 0xFF, 0xFF
	}

	got, err := Checksum(block)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if want := uint16(0xFF01); got != want {
		t.Errorf("Checksum = %#x, want %#x", got, want)
	}
}

func TestChecksumWrongSize(t *testing.T) {
	if _, err := Checksum(make([]byte, BlockSize-1)); err == nil {
		t.Fatal("Checksum with wrong-size buffer: want error, got nil")
	}
	if _, err := Checksum(make([]byte, BlockSize+1)); err == nil {
		t.Fatal("Checksum with wrong-size buffer: want error, got nil")
	}
}
