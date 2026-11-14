package ondisk

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// vmsACL turns a hex dump into bytes; the dumps below are ACLs VMS V7.1's
// $DISPLAY returned for files its SET SECURITY/ACL had changed (govax's
// Phase 93 probe), each with the ACL text it was given.
func vmsACL(t *testing.T, dump string) []byte {
	t.Helper()

	b, err := hex.DecodeString(strings.ReplaceAll(dump, " ", ""))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// The identifiers the probe's ACLs name.
var (
	idA        = UicIdentifier(Uic{Group: 0o200, Member: 1}) // PROT93A
	idB        = UicIdentifier(Uic{Group: 0o200, Member: 2}) // PROT93B
	idTeam     = Identifier(0x80010001)                      // PROT93_TEAM
	idInteract = Identifier(0x80000003)                      // INTERACTIVE
)

func TestACEsAsVMS(t *testing.T) {
	const rwedc = ACEAccessRead | ACEAccessWrite | ACEAccessExecute | ACEAccessDelete | ACEAccessControl

	for _, c := range []struct {
		text string
		dump string
		aces []ACE
	}{
		{"(IDENTIFIER=PROT93A,ACCESS=READ)", "0C010000 01000000 01008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, ACEAccessRead, 0)}},
		{"(IDENTIFIER=PROT93A,ACCESS=READ+WRITE+EXECUTE+DELETE+CONTROL)", "0C010000 1F000000 01008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, rwedc, 0)}},
		{"(IDENTIFIER=[200,*],ACCESS=READ)", "0C010000 01000000 FFFF8000",
			[]ACE{NewIdentifierACE([]Identifier{UicIdentifier(Uic{Group: 0o200, Member: WildMember})}, ACEAccessRead, 0)}},
		{"(IDENTIFIER=[*,*],ACCESS=READ)", "0C010000 01000000 FFFFFF3F",
			[]ACE{NewIdentifierACE([]Identifier{UicIdentifier(Uic{Group: WildGroup, Member: WildMember})}, ACEAccessRead, 0)}},
		{"(IDENTIFIER=PROT93_TEAM,ACCESS=NONE)", "0C010000 00000000 01000180",
			[]ACE{NewIdentifierACE([]Identifier{idTeam}, 0, 0)}},
		{"(IDENTIFIER=PROT93A+PROT93_TEAM,ACCESS=READ)", "10010000 01000000 01008000 01000180",
			[]ACE{NewIdentifierACE([]Identifier{idA, idTeam}, ACEAccessRead, 0)}},
		{"(IDENTIFIER=INTERACTIVE,ACCESS=READ)", "0C010000 01000000 03000080",
			[]ACE{NewIdentifierACE([]Identifier{idInteract}, ACEAccessRead, 0)}},
		{"(IDENTIFIER=PROT93A,OPTIONS=PROTECTED,ACCESS=READ)", "0C010002 01000000 01008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, ACEAccessRead, ACEOptionProtected)}},
		{"(IDENTIFIER=PROT93A,OPTIONS=NOPROPAGATE,ACCESS=READ)", "0C010008 01000000 01008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, ACEAccessRead, ACEOptionNoPropagate)}},
		{"(IDENTIFIER=PROT93A,OPTIONS=DEFAULT,ACCESS=READ)", "0C010001 01000000 01008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, ACEAccessRead, ACEOptionDefault)}},
		{"(IDENTIFIER=PROT93A,OPTIONS=PROTECTED+NOPROPAGATE,ACCESS=READ)", "0C01000A 01000000 01008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, ACEAccessRead, ACEOptionProtected|ACEOptionNoPropagate)}},
		{"(DEFAULT_PROTECTION,S:RWED,O:RWED,G:RE,W:)", "18090000 00000000 10000000 10000000 1A000000 1F000000",
			[]ACE{NewDefaultProtectionACE(0xFA00, 0)}},
		{"(DEFAULT_PROTECTION,OPTIONS=PROTECTED,S:RWED,O:RWED,G:,W:)", "18090002 00000000 10000000 10000000 1F000000 1F000000",
			[]ACE{NewDefaultProtectionACE(0xFF00, ACEOptionProtected)}},
		{"(CREATOR,ACCESS=READ+WRITE)", "080D0000 03000000",
			[]ACE{{Type: ACETypeCreator, Access: ACEAccessRead | ACEAccessWrite}}},
		{"(ALARM_JOURNAL=SECURITY,ACCESS=READ+WRITE+SUCCESS+FAILURE)", "10060300 03000000 53454355 52495459",
			[]ACE{{Type: ACETypeAlarm, Flags: ACEAlarmSuccess | ACEAlarmFailure, Access: ACEAccessRead | ACEAccessWrite, Journal: "SECURITY"}}},
		{"(AUDIT=SECURITY,ACCESS=DELETE+FAILURE)", "10050200 08000000 53454355 52495459",
			[]ACE{{Type: ACETypeAudit, Flags: ACEAlarmFailure, Access: ACEAccessDelete, Journal: "SECURITY"}}},
		{"(IDENTIFIER=PROT93A,ACCESS=READ),(IDENTIFIER=PROT93B,ACCESS=WRITE)", "0C010000 01000000 01008000 0C010000 02000000 02008000",
			[]ACE{NewIdentifierACE([]Identifier{idA}, ACEAccessRead, 0), NewIdentifierACE([]Identifier{idB}, ACEAccessWrite, 0)}},
	} {
		raw := vmsACL(t, c.dump)

		if got := EncodeACL(c.aces); !bytes.Equal(got, raw) {
			t.Errorf("%s: encoded % X, VMS % X", c.text, got, raw)
		}

		decoded, err := DecodeACL(raw)
		if err != nil || len(decoded) != len(c.aces) {
			t.Errorf("%s: decoded %+v, %v", c.text, decoded, err)

			continue
		}

		for i, d := range decoded {
			w := c.aces[i]
			if d.Type != w.Type || d.Flags != w.Flags || d.Access != w.Access || d.DefaultProtection != w.DefaultProtection ||
				d.Journal != w.Journal || !equalIdentifiers(d.Identifiers, w.Identifiers) {
				t.Errorf("%s ACE %d: decoded %+v, want %+v", c.text, i, d, w)
			}
		}

		if again := EncodeACL(decoded); !bytes.Equal(again, raw) {
			t.Errorf("%s: decoded and encoded again, % X", c.text, again)
		}
	}
}

// An ACE of a type this package doesn't know is kept as it is.
func TestACEUnknownTypeKept(t *testing.T) {
	raw := []byte{10, 42, 0, 1, 7, 0, 0, 0, 0xAA, 0xBB}

	aces, err := DecodeACL(raw)
	if err != nil || len(aces) != 1 || aces[0].Type != 42 || aces[0].Flags != ACEOptionDefault || aces[0].Access != 7 {
		t.Fatalf("%+v, %v", aces, err)
	}

	if got := EncodeACL(aces); !bytes.Equal(got, raw) {
		t.Errorf("% X, want % X", got, raw)
	}
}

func TestDecodeACLErrors(t *testing.T) {
	for _, raw := range [][]byte{
		{7, 1, 0, 0, 0, 0, 0},                // shorter than a header
		{12, 1, 0, 0, 1, 0, 0, 0, 1, 0},      // runs past the end
		{10, 1, 0, 0, 1, 0, 0, 0, 1, 0},      // an identifier ACE of half an identifier
		{8, 1, 0, 0, 1, 0, 0, 0},             // an identifier ACE of none
		{12, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // a short default protection ACE
	} {
		if aces, err := DecodeACL(raw); err == nil {
			t.Errorf("% X: %+v, want an error", raw, aces)
		}
	}

	// A zero length byte ends the list, as a header's padding does.
	raw := append(vmsACL(t, "080D0000 03000000"), 0, 0)
	if aces, err := DecodeACL(raw); err != nil || len(aces) != 1 {
		t.Errorf("padded: %+v, %v", aces, err)
	}
}

func TestFileHeaderACEs(t *testing.T) {
	acl := vmsACL(t, "0C010000 01000000 01008000")

	b, err := EncodeFileHeader(FileHeader{}, FileHeaderAreas{Ident: &Ident{Filename: "A.DAT;1"}, AclBytes: ACLArea(acl)})
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeFileHeader(b)
	if err != nil {
		t.Fatal(err)
	}

	aces, err := h.ACEs()
	if err != nil || len(aces) != 1 || aces[0].Identifiers[0] != idA {
		t.Errorf("%+v, %v", aces, err)
	}
}

// The inheritance rules on [PROT93]DP.DIR's ACL as VMS V7.1 had it: a
// default protection ACE, a DEFAULT ACE for PROT93_TEAM, a DEFAULT and
// NOPROPAGATE one for PROT93B, and a plain one for PROT93A.
func TestACLInheritance(t *testing.T) {
	dp := vmsACL(t, "18090000 00000000 10000000 10000000 1E000000 1B000000"+
		"0C010001 01000000 01000180"+
		"0C010009 03000000 02008000"+
		"0C010000 01000000 01008000")

	types := func(acl []byte) []ACE {
		aces, err := DecodeACL(acl)
		if err != nil {
			t.Fatal(err)
		}

		return aces
	}

	// A file made in it: the two DEFAULT ACEs, DEFAULT cleared and
	// NOPROPAGATE kept.
	files, err := ACLFromDirectoryDefaults(dp)
	if err != nil {
		t.Fatal(err)
	}

	want := EncodeACL([]ACE{
		NewIdentifierACE([]Identifier{idTeam}, ACEAccessRead, 0),
		NewIdentifierACE([]Identifier{idB}, ACEAccessRead|ACEAccessWrite, ACEOptionNoPropagate),
	})
	if !bytes.Equal(files, want) {
		t.Errorf("a file's ACL: %+v", types(files))
	}

	// A subdirectory: everything but the NOPROPAGATE ACE, DEFAULT kept.
	sub, err := ACLForSubdirectory(dp)
	if err != nil {
		t.Fatal(err)
	}

	if got := types(sub); len(got) != 3 || got[0].Type != ACETypeDefaultProtection || !got[1].Has(ACEOptionDefault) || got[2].Identifiers[0] != idA {
		t.Errorf("a subdirectory's ACL: %+v", got)
	}

	// A new version of the file made in it: the NOPROPAGATE ACE goes.
	next, err := ACLForNewVersion(files)
	if err != nil {
		t.Fatal(err)
	}

	if got := types(next); len(got) != 1 || got[0].Identifiers[0] != idTeam {
		t.Errorf("a new version's ACL: %+v", got)
	}

	// Nothing kept is no ACL.
	if none, err := ACLFromDirectoryDefaults(vmsACL(t, "0C010000 01000000 01008000")); err != nil || none != nil {
		t.Errorf("no DEFAULT ACEs: % X, %v", none, err)
	}
}

func TestIdentifier(t *testing.T) {
	for _, c := range []struct {
		id   Identifier
		uic  bool
		text string
	}{
		{idA, true, "[200,1]"},
		{0x0080FFFF, true, "[200,*]"},
		{0x3FFFFFFF, true, "[*,*]"},
		{idInteract, false, "%X80000003"},
	} {
		if c.id.IsUic() != c.uic || c.id.String() != c.text {
			t.Errorf("%#x: IsUic %v, %q; want %v, %q", uint32(c.id), c.id.IsUic(), c.id.String(), c.uic, c.text)
		}

		if c.uic && UicIdentifier(c.id.Uic()) != c.id {
			t.Errorf("%#x: doesn't come back from its UIC", uint32(c.id))
		}
	}
}

// equalIdentifiers compares two lists of identifiers.
func equalIdentifiers(a, b []Identifier) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
