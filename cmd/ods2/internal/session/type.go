package session

import (
	"fmt"
	"io"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/rms"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{Name: "type", MinAbbrev: 3, MinArgs: 1, MaxArgs: 1, Run: cmdType},
	)
}

// cmdType implements `type file-spec`: writes one file's content to the
// session's output. Unlike `directory` or `copy`, TYPE never accepts
// wildcards — matching the reference implementation, which parses its
// argument with no NAM (wildcard-capable name) block at all.
func cmdType(s *Session, args []string, quals Qualifiers) error {
	spec, err := filespec.Parse(args[0], s.Default)
	if err != nil {
		return fmt.Errorf("type: %w", err)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("type: %w", err)
	}

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("type: %w", err)
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("type: %s.%s not found", spec.Name, spec.Type)
	case 1:
		// exactly one match: proceed
	default:
		return fmt.Errorf("type: %s.%s is ambiguous (%d files match); TYPE does not support wildcards", spec.Name, spec.Type, len(matches))
	}

	f, err := vol.OpenFID(matches[0].Fid)
	if err != nil {
		return fmt.Errorf("type: %w", err)
	}

	return typeFile(s.Stdout, f)
}

// typeFile writes f's entire content to w as text, honoring its record
// format: VFC records have their carriage control expanded (see
// rms.FormatVFCRecord); every other format gets one '\n' appended per
// record, reconstructing ordinary line-oriented text regardless of
// whether the original framing was a length prefix (Fixed/Variable) or a
// stream delimiter that rms.Reader has already stripped off (Stream).
func typeFile(w io.Writer, f *volume.File) error {
	r, err := rms.NewReader(f)
	if err != nil {
		return err
	}

	attr := f.Header.RecordAttributes
	isVFC := attr.Format == ondisk.RecordFormatVFC
	vfcSize := int(attr.VfcSize)

	for {
		rec, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		if isVFC && len(rec) >= vfcSize {
			if _, err := w.Write(rms.FormatVFCRecord(rec[:vfcSize], rec[vfcSize:])); err != nil {
				return err
			}
			continue
		}

		if _, err := w.Write(rec); err != nil {
			return err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
}
