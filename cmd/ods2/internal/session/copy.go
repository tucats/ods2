package session

import (
	"fmt"
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
			Qualifiers: []string{"quiet", "verbose", "test", "binary"},
			Run:        cmdCopy,
		},
	)
}

// cmdCopy implements `copy source-spec destination [/quiet] [/verbose]
// [/test] [/binary]`, copying one or more files off a mounted volume onto
// the host filesystem. destination is always host (not VMS) path syntax —
// this project, like the reference implementation, never writes back to
// an ODS-2 volume.
//
// A subset of the reference implementation's full qualifier set: /dirs
// (mirror source subdirectories as host directories while also
// materializing .DIR entries), /stream and /vfc (format-specific raw-copy
// tweaks), /ignore (fall back to raw bytes on a corrupt record instead of
// stopping), /time (preserve the source file's date on the copy), and
// /crlf/lf (force a specific line-ending convention) are not implemented
// yet; text-mode copying already handles the common VFC/Stream/Variable
// cases correctly via package rms, just without those specific
// overrides.
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

		if err := copyOneFile(vol, m, outPath, binary); err != nil {
			return fmt.Errorf("copy: %s: %w", sourceName, err)
		}

		if !quiet {
			fmt.Fprintf(s.Stdout, "%%COPY-S-COPIED, %s copied to %s\n", sourceName, outPath)
		}
	}

	return nil
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
// otherwise (see typeFile).
func copyOneFile(vol *volume.Volume, m filespec.Match, outPath string, binary bool) error {
	f, err := vol.OpenFID(m.Fid)
	if err != nil {
		return err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if binary {
		return copyBinary(out, f)
	}
	return typeFile(out, f)
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
