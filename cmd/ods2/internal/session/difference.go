package session

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/rms"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{Name: "difference", MinAbbrev: 4, MinArgs: 2, MaxArgs: 2, Run: cmdDifference},
	)
}

// cmdDifference implements `difference file-spec local-file`: a simple
// line-by-line comparison between one file on a mounted volume and a
// plain file on the host filesystem, matching the reference
// implementation's own admittedly basic diff (no attempt at finding a
// minimal edit script or realigning after an inserted/deleted line — just
// "line N differs" for each line number where the two files disagree).
func cmdDifference(s *Session, args []string, quals Qualifiers) error {
	spec, err := filespec.Parse(args[0], s.Default)
	if err != nil {
		return fmt.Errorf("difference: %w", err)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("difference: %w", err)
	}

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("difference: %w", err)
	}

	if len(matches) != 1 {
		return fmt.Errorf("difference: %s.%s must match exactly one file, matched %d", spec.Name, spec.Type, len(matches))
	}

	f, err := vol.OpenFID(matches[0].Fid)
	if err != nil {
		return fmt.Errorf("difference: %w", err)
	}

	local, err := os.Open(args[1])
	if err != nil {
		return fmt.Errorf("difference: %w", err)
	}

	defer local.Close()

	return diffFiles(s.Stdout, f, local)
}

// diffFiles compares f's records against local's lines, one to one by
// position, and reports how many positions disagree.
func diffFiles(w io.Writer, f *volume.File, local io.Reader) error {
	r, err := rms.NewReader(f)
	if err != nil {
		return err
	}

	attr := f.Header.RecordAttributes
	isVFC := attr.Format == ondisk.RecordFormatVFC
	vfcSize := int(attr.VfcSize)

	scanner := bufio.NewScanner(local)
	lineNum := 0
	diffs := 0

	for {
		rec, vmsErr := r.Next()
		haveVMS := vmsErr == nil

		if vmsErr != nil && vmsErr != io.EOF {
			return vmsErr
		}

		haveLocal := scanner.Scan()

		if !haveVMS && !haveLocal {
			break
		}

		lineNum++

		var vmsLine string

		if haveVMS {
			text := rec
			if isVFC && len(rec) >= vfcSize {
				text = rec[vfcSize:]
			}

			vmsLine = string(text)
		}

		if haveVMS != haveLocal || vmsLine != scanner.Text() {
			diffs++

			fmt.Fprintf(w, "%%DIFF-I-DIFF, line %d differs\n", lineNum)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if diffs == 0 {
		fmt.Fprintln(w, "%DIFF-I-EQUAL, files are identical")
	} else {
		fmt.Fprintf(w, "%%DIFF-I-COUNT, %d line(s) differ\n", diffs)
	}
	
	return nil
}
