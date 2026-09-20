package volume

import "testing"

func TestMountSingleDevice(t *testing.T) {
	c := newMemContainer(10)
	c.putBlock(1, buildHomeBlockBytes(t, homeBlockFixture{
		homeLBN:     1,
		rvn:         1,
		clusterSize: 4,
	}))

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	if len(vol.Devices) != 1 {
		t.Fatalf("len(Devices) = %d, want 1", len(vol.Devices))
	}
	if vol.Devices[0].Rvn != 1 {
		t.Errorf("Devices[0].Rvn = %d, want 1", vol.Devices[0].Rvn)
	}
	if vol.Devices[0].Home.ClusterSize != 4 {
		t.Errorf("Devices[0].Home.ClusterSize = %d, want 4", vol.Devices[0].Home.ClusterSize)
	}
}

func TestMountHomeBlockNotAtFirstBlock(t *testing.T) {
	// The home block doesn't have to be at LBN 1 — Mount must keep
	// scanning until it finds one that is both well-formed and
	// self-consistent (HomeLBN equals the LBN it was read from).
	c := newMemContainer(10)
	c.putBlock(5, buildHomeBlockBytes(t, homeBlockFixture{homeLBN: 5, rvn: 1}))

	vol, err := Mount(c)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if vol.Devices[0].Home.HomeLBN != 5 {
		t.Errorf("Home.HomeLBN = %d, want 5", vol.Devices[0].Home.HomeLBN)
	}
}

func TestMountRejectsSelfInconsistentHomeBlock(t *testing.T) {
	// A block that otherwise looks like a valid home block (right format,
	// right checksum) but whose HomeLBN field doesn't match where it was
	// actually read from is not self-consistent, and must be rejected —
	// otherwise a stray copy of a home block left over at the wrong
	// location (e.g. from a previous, different volume) could be
	// mistaken for the real one.
	c := newMemContainer(10)
	// Written at LBN 3, but claims to be the home block for LBN 7.
	c.putBlock(3, buildHomeBlockBytes(t, homeBlockFixture{homeLBN: 7, rvn: 1}))

	if _, err := Mount(c); err == nil {
		t.Fatal("Mount with a self-inconsistent home block: want error, got nil")
	}
}

func TestMountNoHomeBlockFound(t *testing.T) {
	c := newMemContainer(10) // every block left zeroed: no valid home block anywhere
	if _, err := Mount(c); err == nil {
		t.Fatal("Mount with no home block present: want error, got nil")
	}
}

func TestMountRequiresAtLeastOneContainer(t *testing.T) {
	if _, err := Mount(); err == nil {
		t.Fatal("Mount with no containers: want error, got nil")
	}
}

func TestMountVolumeSet(t *testing.T) {
	dev1 := newMemContainer(10)
	dev1.putBlock(1, buildHomeBlockBytes(t, homeBlockFixture{homeLBN: 1, rvn: 1}))

	dev2 := newMemContainer(10)
	dev2.putBlock(1, buildHomeBlockBytes(t, homeBlockFixture{homeLBN: 1, rvn: 2}))

	vol, err := Mount(dev1, dev2)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if len(vol.Devices) != 2 {
		t.Fatalf("len(Devices) = %d, want 2", len(vol.Devices))
	}
	if vol.Devices[0].Rvn != 1 || vol.Devices[1].Rvn != 2 {
		t.Errorf("Devices Rvns = [%d, %d], want [1, 2]", vol.Devices[0].Rvn, vol.Devices[1].Rvn)
	}
}

func TestMountRejectsVolumeSetOutOfOrder(t *testing.T) {
	// Two devices whose home blocks both claim relative volume number 1
	// — as if the caller passed the volume set's second member first, or
	// mixed up two unrelated single-disk volumes — should be rejected
	// rather than silently mounted with an inconsistent Rvn assignment.
	dev1 := newMemContainer(10)
	dev1.putBlock(1, buildHomeBlockBytes(t, homeBlockFixture{homeLBN: 1, rvn: 1}))

	dev2 := newMemContainer(10)
	dev2.putBlock(1, buildHomeBlockBytes(t, homeBlockFixture{homeLBN: 1, rvn: 1}))

	if _, err := Mount(dev1, dev2); err == nil {
		t.Fatal("Mount with an out-of-order volume set: want error, got nil")
	}
}
