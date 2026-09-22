package filespec

import "testing"

func TestSpecString(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{
			name: "full spec",
			spec: Spec{Device: "DUA0", Dirs: []string{"FOO", "BAR"}, Name: "NAME", Type: "TYP", Version: "5"},
			want: "DUA0:[FOO.BAR]NAME.TYP;5",
		},
		{
			name: "root directory",
			spec: Spec{Device: "DUA0", Name: "NAME", Type: "TYP"},
			want: "DUA0:[000000]NAME.TYP",
		},
		{
			name: "no device",
			spec: Spec{Dirs: []string{"FOO"}, Name: "NAME"},
			want: "[FOO]NAME",
		},
		{
			name: "recursive",
			spec: Spec{Dirs: []string{"FOO"}, Recursive: true, Name: "*", Type: "*"},
			want: "[FOO...]*.*",
		},
		{
			name: "no version",
			spec: Spec{Name: "NAME", Type: "TYP"},
			want: "[000000]NAME.TYP",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.spec.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSpecStringParseRoundTrip(t *testing.T) {
	original := "DUA0:[FOO.BAR]NAME.TYP;5"

	spec, err := Parse(original, Spec{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := spec.String(); got != original {
		t.Errorf("round trip: Parse(%q).String() = %q, want %q", original, got, original)
	}
}
