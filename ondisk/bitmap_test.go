package ondisk

import "testing"

// TestBitmapAllClearInitially confirms a freshly zeroed buffer reads as
// "every cluster allocated" -- bit 0 means allocated, so an all-zero
// buffer (as a freshly allocated Go byte slice always starts) has no free
// clusters until BitmapSet marks some.
func TestBitmapAllClearInitially(t *testing.T) {
	bits := make([]byte, 4)
	for cluster := uint32(0); cluster < 32; cluster++ {
		if BitmapTest(bits, cluster) {
			t.Errorf("BitmapTest(cluster %d) on a zeroed buffer = true, want false", cluster)
		}
	}
}

// TestBitmapSetClearRoundTrip exercises set/clear/test across byte
// boundaries (clusters 7/8/9 straddle the first two bytes) and confirms
// each bit is independent of its neighbors.
func TestBitmapSetClearRoundTrip(t *testing.T) {
	bits := make([]byte, 4)

	for _, cluster := range []uint32{0, 1, 7, 8, 9, 15, 16, 31} {
		BitmapSet(bits, cluster)
		if !BitmapTest(bits, cluster) {
			t.Errorf("BitmapTest(cluster %d) after BitmapSet = false, want true", cluster)
		}
	}

	// Every other cluster should be untouched by those sets.
	set := map[uint32]bool{0: true, 1: true, 7: true, 8: true, 9: true, 15: true, 16: true, 31: true}
	for cluster := uint32(0); cluster < 32; cluster++ {
		want := set[cluster]
		if got := BitmapTest(bits, cluster); got != want {
			t.Errorf("BitmapTest(cluster %d) = %v, want %v", cluster, got, want)
		}
	}

	// Clearing one bit doesn't disturb its neighbors, including the one
	// sharing its byte.
	BitmapClear(bits, 8)
	if BitmapTest(bits, 8) {
		t.Error("BitmapTest(cluster 8) after BitmapClear = true, want false")
	}
	if !BitmapTest(bits, 7) || !BitmapTest(bits, 9) {
		t.Error("BitmapClear(cluster 8) disturbed a neighboring cluster's bit")
	}
}

// TestBitmapSetIdempotent confirms setting an already-set bit (or clearing
// an already-clear one) is a harmless no-op, not an error or a toggle.
func TestBitmapSetIdempotent(t *testing.T) {
	bits := make([]byte, 1)

	BitmapSet(bits, 3)
	BitmapSet(bits, 3)
	if !BitmapTest(bits, 3) {
		t.Error("BitmapTest(cluster 3) after two BitmapSet calls = false, want true")
	}

	BitmapClear(bits, 5)
	BitmapClear(bits, 5)
	if BitmapTest(bits, 5) {
		t.Error("BitmapTest(cluster 5) after two BitmapClear calls = true, want false")
	}
}
