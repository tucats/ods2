package ondisk

import "testing"

func TestParseProtection(t *testing.T) {
	tests := []struct {
		text string
		base uint16
		want uint16
	}{
		{"(S:RWED,O:RWED,G:RE,W)", 0, 0xFA00},
		{"(S:RWE,O:RWE,G:RE,W:E)", 0, 0xBA88},
		{"(SYSTEM:RWED,OWNER:RWED,GROUP:RE,WORLD)", 0, 0xFA00},
		{"(sy=rwed, ow=rwed, gr=re, wo)", 0, 0xFA00},
		{"(S,O,G,W)", 0, 0xFFFF},
		{"W:RE", 0xFA00, 0xAA00},
		{"(G:R)", 0xBA88, 0xBE88},
		{"(W:DERW)", 0xFFFF, 0x0FFF},
	}

	for _, tt := range tests {
		got, err := ParseProtection(tt.text, tt.base)
		if err != nil {
			t.Errorf("ParseProtection(%q): %v", tt.text, err)
			continue
		}

		if got != tt.want {
			t.Errorf("ParseProtection(%q, %#x) = %#x, want %#x", tt.text, tt.base, got, tt.want)
		}
	}
}

func TestParseProtectionErrors(t *testing.T) {
	for _, text := range []string{"", "()", "(X:RWED)", "(S:RWEX)", "(S:R,SYSTEM:W)", "(S:R,,W)"} {
		if _, err := ParseProtection(text, 0); err == nil {
			t.Errorf("ParseProtection(%q): want an error, got none", text)
		}
	}
}

func TestFormatProtection(t *testing.T) {
	for mask, want := range map[uint16]string{
		0xFA00: "(S:RWED,O:RWED,G:RE,W)",
		0xBA88: "(S:RWE,O:RWE,G:RE,W:E)",
		0x0000: "(S:RWED,O:RWED,G:RWED,W:RWED)",
		0xFFFF: "(S,O,G,W)",
	} {
		if got := FormatProtection(mask); got != want {
			t.Errorf("FormatProtection(%#x) = %q, want %q", mask, got, want)
		}

		if back, err := ParseProtection(want, 0x1234); err != nil || back != mask {
			t.Errorf("ParseProtection(FormatProtection(%#x)) = %#x, %v", mask, back, err)
		}
	}
}
