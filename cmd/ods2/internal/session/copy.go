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
			Name:      "copy",
			MinAbbrev: 4,
			MinArgs:   2,
			MaxArgs:   2,
			Qualifiers: []string{
				"quiet", "verbose", "test", "binary", "time", "ignore",
				"dirs", "stream", "vfc", "crlf", "lf",
			},
			Run: cmdCopy,
		},
	)
}

// cmdCopy implements `copy source-spec destination` with the reference
// implementation's full qualifier set (see COMMANDS.md for user-facing
// documentation of each one): /QUIET, /VERBOSE, /TEST, /BINARY, /TIME,
// /IGNORE, /DIRS, /STREAM, /VFC, /CRLF, /LF. destination is always host
// (not VMS) path syntax — this project, like the reference
// implementation, never writes back to an ODS-2 volume.
//
// /VFC is accepted for command-line compatibility but has no effect:
// unlike the reference implementation (where VFC interpretation is
// opt-in), this project's default text-mode copy always expands a VFC
// file's carriage control, the same as `type` does — there is no
// "un-interpreted" text mode to opt out of.
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

	if quals.Has("crlf") && quals.Has("lf") {
		return fmt.Errorf("copy: /crlf and /lf are mutually exclusive")
	}

	opts := copyOptions{
		binary:     quals.Has("binary"),
		ignore:     quals.Has("ignore"),
		stream:     quals.Has("stream"),
		lineEnding: lfLineEnding,
	}
	if quals.Has("crlf") {
		opts.lineEnding = crlfLineEnding
	}

	quiet := quals.Has("quiet")
	verbose := quals.Has("verbose")
	test := quals.Has("test")
	preserveTime := quals.Has("time")
	dirs := quals.Has("dirs")

	for _, m := range matches {
		sourceName := fmt.Sprintf("%s.%s%c%d", m.Name, m.Type, s.Delim, m.Version)

		// A directory entry (e.g. SUBDIR.DIR) has no file content of its
		// own to copy. Without /DIRS, skip it entirely, matching the
		// reference implementation's default; with /DIRS, materialize it
		// as a host directory instead of copying "content".
		if strings.EqualFold(m.Type, "DIR") {
			if !dirs {
				continue
			}
			if err := copyDirEntry(s, dest, m, sourceName, test, verbose, quiet); err != nil {
				return err
			}
			continue
		}

		outPath := resolveDestination(dest, m, s.Delim, dirs)

		if test {
			fmt.Fprintf(s.Stdout, "%%COPY-I-TEST, would copy %s to %s\n", sourceName, outPath)
			continue
		}

		if verbose {
			fmt.Fprintf(s.Stdout, "%%COPY-I-COPYING, copying %s to %s\n", sourceName, outPath)
		}

		f, err := copyOneFile(vol, m, outPath, opts)
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

// copyDirEntry handles one matched directory entry under /DIRS: it
// creates the corresponding host directory (mirroring m.Dirs, plus the
// directory's own name, under dest) rather than copying any content.
func copyDirEntry(s *Session, dest string, m filespec.Match, sourceName string, test, verbose, quiet bool) error {
	dirPath := hostDirPath(dest, m)

	if test {
		fmt.Fprintf(s.Stdout, "%%COPY-I-TEST, would create directory for %s at %s\n", sourceName, dirPath)
		return nil
	}
	if verbose {
		fmt.Fprintf(s.Stdout, "%%COPY-I-COPYING, creating directory %s for %s\n", dirPath, sourceName)
	}
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("copy: creating directory for %s: %w", sourceName, err)
	}
	if !quiet {
		fmt.Fprintf(s.Stdout, "%%COPY-S-COPIED, %s materialized as directory %s\n", sourceName, dirPath)
	}
	return nil
}

// hostDirPath builds the host directory path representing matched
// directory entry m: dest, followed by m's directory path (m.Dirs), followed
// by the directory's own name (m.Name) — i.e. the full path TO and
// INCLUDING this directory itself.
func hostDirPath(dest string, m filespec.Match) string {
	parts := make([]string, 0, len(m.Dirs)+2)
	parts = append(parts, dest)
	parts = append(parts, m.Dirs...)
	parts = append(parts, m.Name)
	return filepath.Join(parts...)
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
//     there under its own name.type;version — under a mirrored copy of
//     its source subdirectory path (m.Dirs) too, if preserveDirs (/DIRS)
//     is set and the match came from a subdirectory.
//   - If dest's base name contains '*', that wildcard is filled in from
//     the matched file's own name and/or type — VMS's classic
//     wildcard-copy substitution (e.g. copying "*.TXT" to "*.OLD" renames
//     each file's type while keeping its own name).
//   - Otherwise dest is used exactly as given, as a literal output path
//     (only sensible when exactly one file is being copied — cmdCopy
//     checks that before calling this).
func resolveDestination(dest string, m filespec.Match, delim byte, preserveDirs bool) string {
	filename := fmt.Sprintf("%s.%s%c%d", m.Name, m.Type, delim, m.Version)

	if destIsDirectory(dest) {
		if preserveDirs && len(m.Dirs) > 0 {
			parts := make([]string, 0, len(m.Dirs)+2)
			parts = append(parts, dest)
			parts = append(parts, m.Dirs...)
			parts = append(parts, filename)
			return filepath.Join(parts...)
		}
		return filepath.Join(dest, filename)
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

// copyOptions collects copy's format-affecting qualifiers, gathered into
// one value rather than a long, easy-to-transpose list of bool
// parameters.
type copyOptions struct {
	binary     bool
	ignore     bool
	stream     bool // force raw-byte copying for Stream-format source files (/STREAM)
	lineEnding []byte
}

// copyOneFile copies one matched file's content to outPath, creating any
// missing parent directories along the way (relevant when /DIRS produced
// a nested outPath). It returns the opened volume.File (so the caller can
// use its header for /time) even when an error occurs afterward, since
// the header itself was successfully read regardless of what happened
// next.
//
// If opts.ignore is set and text-mode copying hits a corrupt record
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
func copyOneFile(vol *volume.Volume, m filespec.Match, outPath string, opts copyOptions) (*volume.File, error) {
	f, err := vol.OpenFID(m.Fid)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return f, err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return f, err
	}
	defer out.Close()

	if opts.binary {
		return f, copyBinary(out, f)
	}

	if opts.stream && isStreamFormat(f.Header.RecordAttributes.Format) {
		// /STREAM: copy a stream-format file's exact bytes rather than
		// scanning for delimiters and re-writing a (possibly different)
		// line ending — "don't look for a line end character and don't
		// insert anything in output", per the reference implementation.
		return f, copyBinary(out, f)
	}

	err = writeRecords(out, f, opts.lineEnding)
	if err != nil && opts.ignore && errors.Is(err, rms.ErrCorruptRecord) {
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

// isStreamFormat reports whether format is one of the three Stream record
// formats (as opposed to Fixed, Variable, VFC, or Undefined).
func isStreamFormat(format ondisk.RecordFormat) bool {
	switch format {
	case ondisk.RecordFormatStreamCRLF, ondisk.RecordFormatStreamLF, ondisk.RecordFormatStreamCR:
		return true
	default:
		return false
	}
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
