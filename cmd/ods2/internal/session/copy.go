package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tucats/ods2/filespec"
	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/rms"
	"github.com/tucats/ods2/volume"
)

func init() {
	Table = append(Table,
		Command{
			Name:       "copy",
			MinAbbrev:  4,
			MinArgs:    2,
			MaxArgs:    2,
			Qualifiers: []string{"quiet", "verbose", "test", "binary", "time", "ignore"},
			Run:        cmdCopy,
		},
	)
}

// cmdCopy implements `copy source-spec destination [/quiet] [/verbose]
// [/test] [/binary] [/time] [/ignore]`, copying one or more files off a
// mounted volume onto the host filesystem. destination is always host
// (not VMS) path syntax — this project, like the reference
// implementation, never writes back to an ODS-2 volume.
//
// A subset of the reference implementation's full qualifier set: /dirs
// (mirror source subdirectories as host directories while also
// materializing .DIR entries) and /stream/vfc/crlf/lf (format-specific
// copy tweaks) are not implemented yet; text-mode copying already
// handles the common VFC/Stream/Variable cases correctly via package rms,
// just without those specific overrides.
func cmdCopy(s *Session, args []string, quals Qualifiers) error {
	spec, err := filespec.Parse(args[0], s.Default)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	vol, err := s.volumeFor(spec)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	matches, err := filespec.Glob(vol, spec)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("copy: %s.%s not found", spec.Name, spec.Type)
	}

	dest := args[1]
	if len(matches) > 1 && !destIsDirectory(dest) && !strings.Contains(filepath.Base(dest), "*") {
		return fmt.Errorf("copy: %s.%s matches %d files; destination must be a directory or contain '*' to copy more than one file", spec.Name, spec.Type, len(matches))
	}

	quiet := quals.Has("quiet")
	verbose := quals.Has("verbose")
	test := quals.Has("test")
	binary := quals.Has("binary")
	preserveTime := quals.Has("time")
	ignore := quals.Has("ignore")

	for _, m := range matches {
		outPath := resolveDestination(dest, m, s.Delim)
		sourceName := fmt.Sprintf("%s.%s%c%d", m.Name, m.Type, s.Delim, m.Version)

		if test {
			fmt.Fprintf(s.Stdout, "%%COPY-I-TEST, would copy %s to %s\n", sourceName, outPath)
			continue
		}

		if verbose {
			fmt.Fprintf(s.Stdout, "%%COPY-I-COPYING, copying %s to %s\n", sourceName, outPath)
		}

		f, err := copyOneFile(vol, m, outPath, binary, ignore)
		if err != nil {
			return fmt.Errorf("copy: %s: %w", sourceName, err)
		}

		if preserveTime {
			if err := preserveFileTime(outPath, f); err != nil && verbose {
				fmt.Fprintf(s.Stdout, "%%COPY-W-NOTIME, could not preserve date on %s: %v\n", outPath, err)
			}
		}

		if !quiet {
			fmt.Fprintf(s.Stdout, "%%COPY-S-COPIED, %s copied to %s\n", sourceName, outPath)
		}
	}

	return nil
}

// preserveFileTime sets outPath's modification (and access) time to f's
// own revision date, for /time.
func preserveFileTime(outPath string, f *volume.File) error {
	ident, err := f.Header.Ident()
	if err != nil {
		return err
	}
	t := ident.RevisionDate.Time()
	return os.Chtimes(outPath, t, t)
}

// destIsDirectory reports whether dest already names an existing host
// directory.
func destIsDirectory(dest string) bool {
	info, err := os.Stat(dest)
	return err == nil && info.IsDir()
}

// resolveDestination builds the host output path for one matched file.
//
//   - If dest already names an existing directory, the file is written
//     there under its own name.type;version.
//   - If dest's base name contains '*', that wildcard is filled in from
//     the matched file's own name and/or type — VMS's classic
//     wildcard-copy substitution (e.g. copying "*.TXT" to "*.OLD" renames
//     each file's type while keeping its own name).
//   - Otherwise dest is used exactly as given, as a literal output path
//     (only sensible when exactly one file is being copied — cmdCopy
//     checks that before calling this).
func resolveDestination(dest string, m filespec.Match, delim byte) string {
	if destIsDirectory(dest) {
		return filepath.Join(dest, fmt.Sprintf("%s.%s%c%d", m.Name, m.Type, delim, m.Version))
	}

	base := filepath.Base(dest)
	if !strings.Contains(base, "*") {
		return dest
	}

	ext := filepath.Ext(base)
	namePart := strings.TrimSuffix(base, ext)
	typePart := strings.TrimPrefix(ext, ".")

	if namePart == "*" {
		namePart = m.Name
	}
	if typePart == "*" || typePart == "" {
		typePart = m.Type
	}

	return filepath.Join(filepath.Dir(dest), namePart+"."+typePart)
}

// copyOneFile copies one matched file's content to outPath: raw bytes in
// /binary mode, or the same text-mode rendering `type` produces
// otherwise (see typeFile). It returns the opened volume.File (so the
// caller can use its header for /time) even when an error occurs, since
// the header itself was successfully read regardless of what happened
// afterward.
//
// If ignore is set and text-mode copying hits a corrupt record
// (rms.ErrCorruptRecord), copyOneFile restarts the destination file from
// scratch in raw/binary mode rather than leaving a partially-written
// text-mode file behind. This is a simplified stand-in for the reference
// implementation's own /IGNORE behavior (which switches to raw mode and
// resumes from exactly where the corruption was found): this project's
// rms.Reader doesn't expose how many bytes it had already consumed at
// the point of failure, so "restart the whole file in raw mode" is what's
// implemented instead — still recovers the file's bytes losslessly, just
// without preserving whatever partial record-oriented formatting the
// good portion of the file would otherwise have gotten.
func copyOneFile(vol *volume.Volume, m filespec.Match, outPath string, binary, ignore bool) (*volume.File, error) {
	f, err := vol.OpenFID(m.Fid)
	if err != nil {
		return nil, err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return f, err
	}
	defer out.Close()

	if binary {
		return f, copyBinary(out, f)
	}

	err = typeFile(out, f)
	if err != nil && ignore && errors.Is(err, rms.ErrCorruptRecord) {
		if _, seekErr := out.Seek(0, io.SeekStart); seekErr != nil {
			return f, seekErr
		}
		if truncErr := out.Truncate(0); truncErr != nil {
			return f, truncErr
		}
		return f, copyBinary(out, f)
	}
	return f, err
}

// copyBinary copies a file's exact valid bytes (see rms.FileByteLength)
// with no record interpretation at all — the file's raw on-disk content,
// block by block.
func copyBinary(out *os.File, f *volume.File) error {
	remaining := rms.FileByteLength(f.Header.RecordAttributes)

	buf := make([]byte, ondisk.BlockSize)
	for vbn := uint32(1); remaining > 0; vbn++ {
		if err := f.ReadBlock(vbn, buf); err != nil {
			return err
		}

		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		if _, err := out.Write(buf[:n]); err != nil {
			return err
		}
		remaining -= n
	}

	return nil
}
