package filespec

import (
	"reflect"
	"testing"

	"github.com/tucats/ods2/ondisk"
)

func fid(n uint16) ondisk.Fid { return ondisk.Fid{Num: n, Seq: 1} }

func TestParseVersionSelector(t *testing.T) {
	cases := []struct {
		in   string
		want versionSelector
	}{
		{"", versionSelector{kind: versionHighest}},
		{"0", versionSelector{kind: versionHighest}},
		{"*", versionSelector{kind: versionAll}},
		{"5", versionSelector{kind: versionExact, value: 5}},
		{"-1", versionSelector{kind: versionRelative, value: 1}},
		{"-2", versionSelector{kind: versionRelative, value: 2}},
	}
	for _, c := range cases {
		got, err := parseVersionSelector(c.in)
		if err != nil {
			t.Errorf("parseVersionSelector(%q): %v", c.in, err)

			continue
		}

		if got != c.want {
			t.Errorf("parseVersionSelector(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseVersionSelectorRejectsGarbage(t *testing.T) {
	if _, err := parseVersionSelector("not-a-number"); err == nil {
		t.Fatal("parseVersionSelector on garbage: want error, got nil")
	}
}

func TestSelectVersionsHighest(t *testing.T) {
	entries := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 1, Fid: fid(1)},
		{Name: "A.TXT", Version: 3, Fid: fid(3)},
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
	}
	got := selectVersions(entries, versionSelector{kind: versionHighest})
	want := []ondisk.DirEntry{{Name: "A.TXT", Version: 3, Fid: fid(3)}}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectVersions(highest) = %+v, want %+v", got, want)
	}
}

func TestSelectVersionsAll(t *testing.T) {
	entries := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 1, Fid: fid(1)},
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
	}
	got := selectVersions(entries, versionSelector{kind: versionAll})
	want := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
		{Name: "A.TXT", Version: 1, Fid: fid(1)},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectVersions(all) = %+v, want %+v (highest first)", got, want)
	}
}

func TestSelectVersionsExact(t *testing.T) {
	entries := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 1, Fid: fid(1)},
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
	}
	got := selectVersions(entries, versionSelector{kind: versionExact, value: 1})
	want := []ondisk.DirEntry{{Name: "A.TXT", Version: 1, Fid: fid(1)}}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectVersions(exact 1) = %+v, want %+v", got, want)
	}
}

func TestSelectVersionsExactNoMatch(t *testing.T) {
	entries := []ondisk.DirEntry{{Name: "A.TXT", Version: 1, Fid: fid(1)}}
	got := selectVersions(entries, versionSelector{kind: versionExact, value: 99})

	if len(got) != 0 {
		t.Errorf("selectVersions(exact 99) = %+v, want empty", got)
	}
}

func TestSelectVersionsRelative(t *testing.T) {
	entries := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 1, Fid: fid(1)},
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
		{Name: "A.TXT", Version: 3, Fid: fid(3)},
	}
	// -1 (1 back from highest) is the highest itself.
	got := selectVersions(entries, versionSelector{kind: versionRelative, value: 1})
	want := []ondisk.DirEntry{{Name: "A.TXT", Version: 3, Fid: fid(3)}}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectVersions(relative 1) = %+v, want %+v", got, want)
	}

	// -2 (2 back from highest) is the second-highest.
	got = selectVersions(entries, versionSelector{kind: versionRelative, value: 2})
	want = []ondisk.DirEntry{{Name: "A.TXT", Version: 2, Fid: fid(2)}}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectVersions(relative 2) = %+v, want %+v", got, want)
	}
}

func TestSelectVersionsRelativeOutOfRange(t *testing.T) {
	entries := []ondisk.DirEntry{{Name: "A.TXT", Version: 1, Fid: fid(1)}}
	got := selectVersions(entries, versionSelector{kind: versionRelative, value: 5})

	if len(got) != 0 {
		t.Errorf("selectVersions(relative 5) with only 1 version = %+v, want empty", got)
	}
}

func TestSelectVersionsGroupsIndependently(t *testing.T) {
	entries := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 1, Fid: fid(1)},
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
		{Name: "B.TXT", Version: 1, Fid: fid(3)},
	}
	got := selectVersions(entries, versionSelector{kind: versionHighest})
	want := []ondisk.DirEntry{
		{Name: "A.TXT", Version: 2, Fid: fid(2)},
		{Name: "B.TXT", Version: 1, Fid: fid(3)},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectVersions(highest) across two names = %+v, want %+v", got, want)
	}
}
