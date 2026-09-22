package session

import (
	"bufio"
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
				"dirs", "stream", "vfc", "crlf", "lf", "host",
			},
			Run: cmdCopy,
		},
	)
}

// cmdCopy implements `copy source-spec destination` with the reference
// implementation's full qualifier set (see COMMANDS.md for user-facing
// documentation of each one): /QUIET, /VERBOSE, /TEST, /BINARY, /TIME,
// /IGNORE, /DIRS, /STREAM, /VFC, /CRLF, /LF, /HOST. destination is usually
// a host path, as in the reference implementation, but may instead be VMS
// syntax (device:[dir]name.type) naming a location on a volume mounted
// /WRITE — see volumeDestination — in which case copying goes the other
// direction, from this session's already-mounted source volume onto that
// one. Only /QUIET, /VERBOSE, /TEST, and /BINARY carry over to that
// direction: /TIME (no host mtime to preserve), /IGNORE, /DIRS, and the
// line-ending qualifiers are host-file-format concerns with no obvious
// volume-side equivalent (see copyOneFileToVolume/copyRecordsToVolume).
//
// /HOST flips source-spec's own interpretation instead: with it,
// source-spec names a plain host file rather than a file on the mounted
// volume — see cmdCopyFromHost. Without an explicit qualifier for this,
// there would be no reliable way to tell "copy this host file" apart from
// "copy this file already on the volume", since a bare name like
// "go.sum" is a syntactically valid (if possibly nonexistent) VMS file
// spec too.
//
// /VFC is accepted for command-line compatibility but has no effect:
// unlike the reference implementation (where VFC interpretation is
// opt-in), this project's default text-mode copy always expands a VFC
// file's carriage control, the same as `type` does — there is no
// "un-interpreted" text mode to opt out of.
func cmdCopy(s *Session, args []string, quals Qualifiers) error {
	if quals.Has("crlf") && quals.Has("lf") {
		return fmt.Errorf("copy: /crlf and /lf are mutually exclusive")
	}

	if quals.Has("host") {
		return cmdCopyFromHost(s, args, quals)
	}

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

	destVol, destSpec, toVolume, err := volumeDestination(s, dest)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if len(matches) > 1 && !destAcceptsMultiple(dest, destSpec, toVolume) {
		return fmt.Errorf("copy: %s.%s matches %d files; destination must be a directory or contain '*' to copy more than one file", spec.Name, spec.Type, len(matches))
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

	// For a volume destination, the target directory (and its device's
	// bitmap caches) are resolved once, up front, rather than once per
	// matched file — the same directory receives every file this command
	// copies. Skipped entirely under /TEST, which never actually writes.
	var (
		destDir *volume.Directory
		destBm  *volume.Bitmap
		destIb  *volume.IndexBitmap
	)

	if toVolume && !test {
		destDir, destBm, destIb, err = resolveVolumeDest(destVol, destSpec)
		if err != nil {
			return fmt.Errorf("copy: %w", err)
		}
	}

	for _, m := range matches {
		sourceName := fmt.Sprintf("%s.%s%c%d", m.Name, m.Type, s.Delim, m.Version)

		// A directory entry (e.g. SUBDIR.DIR) has no file content of its
		// own to copy. Without /DIRS, skip it entirely, matching the
		// reference implementation's default; with /DIRS, materialize it
		// as a host directory instead of copying "content" — meaningless
		// for a volume destination (there's no "empty directory" concept
		// being asked for on the write side), so it's always skipped
		// there regardless of /DIRS.
		if strings.EqualFold(m.Type, "DIR") {
			if toVolume || !dirs {
				continue
			}
			
			if err := copyDirEntry(s, dest, m, sourceName, test, verbose, quiet); err != nil {
				return err
			}

			continue
		}

		if toVolume {
			name, typ := volumeDestName(destSpec, m)
			destName := (filespec.Spec{Device: destSpec.Device, Dirs: destSpec.Dirs, Name: name, Type: typ}).String()

			if test {
				fmt.Fprintf(s.Stdout, "%%COPY-I-TEST, would copy %s to %s\n", sourceName, destName)

				continue
			}

			if verbose {
				fmt.Fprintf(s.Stdout, "%%COPY-I-COPYING, copying %s to %s\n", sourceName, destName)
			}

			src, err := vol.OpenFID(m.Fid)
			if err != nil {
				return fmt.Errorf("copy: %s: %w", sourceName, err)
			}

			version, err := copyOneFileToVolume(destVol, destDir, destBm, destIb, name, typ, src, opts.binary)
			if err != nil {
				return fmt.Errorf("copy: %s: %w", sourceName, err)
			}

			if !quiet {
				destVersion := (filespec.Spec{Device: destSpec.Device, Dirs: destSpec.Dirs, Name: name, Type: typ, Version: fmt.Sprint(version)}).String()
				fmt.Fprintf(s.Stdout, "%%COPY-S-COPIED, %s copied to %s\n", sourceName, destVersion)
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

// volumeDestination checks whether dest is VMS-syntax destination
// (device:[dir]name.type) on a currently mounted volume, as opposed to an
// ordinary host path — copy's original (and, absent this check, only)
// destination direction. The device name is looked up before dest is
// parsed as a file spec at all, so an ordinary host path with no mounted
// device of that name (the overwhelmingly common case, and every path
// with no ':' at all) is never mistaken for one.
//
// destSpec's Name and Type are deliberately left unresolved from the
// session's current default (only Dirs inherits it, as a base for dest's
// own directory text, which may itself be written relative — see
// filespec.Parse) — an empty destSpec.Name/Type reliably means "dest's
// text named no file of its own", the volume-destination equivalent of
// destIsDirectory for a host path (see destAcceptsMultiple/
// volumeDestName).
func volumeDestination(s *Session, dest string) (vol *volume.Volume, destSpec filespec.Spec, ok bool, err error) {
	colonIdx := strings.IndexByte(dest, ':')
	if colonIdx == -1 {
		return nil, filespec.Spec{}, false, nil
	}

	vol, ok = s.Volumes[strings.ToUpper(dest[:colonIdx])]
	if !ok {
		return nil, filespec.Spec{}, false, nil
	}

	destSpec, err = filespec.Parse(dest, filespec.Spec{Dirs: s.Default.Dirs})
	if err != nil {
		return nil, filespec.Spec{}, false, err
	}

	return vol, destSpec, true, nil
}

// destAcceptsMultiple reports whether dest can receive more than one
// matched file: for a host path, an existing directory or a base name
// containing '*' (copy's original rule); for a volume destination, one
// naming no file of its own (each source file keeps its own name/type) or
// one using VMS's own wildcard-substitution characters ('*'/'%') in its
// name or type, the volume-destination equivalent of a host '*'
// destination.
func destAcceptsMultiple(dest string, destSpec filespec.Spec, toVolume bool) bool {
	if toVolume {
		return (destSpec.Name == "" && destSpec.Type == "") || strings.ContainsAny(destSpec.Name+destSpec.Type, "*%")
	}

	return destIsDirectory(dest) || strings.Contains(filepath.Base(dest), "*")
}

// volumeDestName resolves the actual name/type to create in the
// destination directory for matched source file m: destSpec's own
// name/type, if it gave one (applying VMS's wildcard-substitution
// characters — a literal "*" or "%" component keeps the source's own
// name/type instead of using it literally, matching resolveDestination's
// '*' handling for a host destination), or m's own name/type unchanged if
// destSpec named no file at all (a directory-only destination — see
// volumeDestination).
func volumeDestName(destSpec filespec.Spec, m filespec.Match) (name, typ string) {
	name, typ = m.Name, m.Type
	if destSpec.Name != "" && destSpec.Name != "*" && destSpec.Name != "%" {
		name = destSpec.Name
	}

	if destSpec.Type != "" && destSpec.Type != "*" && destSpec.Type != "%" {
		typ = destSpec.Type
	}

	return name, typ
}

// resolveVolumeDest resolves a volume destination's target Directory and
// its device's bitmap caches, the one-time setup a volume destination
// needs before any file is written into it (shared by cmdCopy's own
// volume branch and cmdCopyFromHost).
func resolveVolumeDest(destVol *volume.Volume, destSpec filespec.Spec) (*volume.Directory, *volume.Bitmap, *volume.IndexBitmap, error) {
	destDir, err := filespec.ResolveDirectory(destVol, destSpec.Dirs)
	if err != nil {
		return nil, nil, nil, err
	}

	destBm, err := destDir.Device.Bitmap()
	if err != nil {
		return nil, nil, nil, err
	}

	destIb, err := destDir.Device.IndexBitmap()
	if err != nil {
		return nil, nil, nil, err
	}

	return destDir, destBm, destIb, nil
}

// cmdCopyFromHost implements copy's /HOST direction: args[0] names a plain
// host file (rather than a file spec on the mounted volume — see cmdCopy's
// own doc comment for why this needs an explicit qualifier at all), copied
// onto destination, which must be VMS syntax naming a location on a volume
// mounted /WRITE (see volumeDestination). Copying a host file onto the
// host filesystem isn't this command's job under /HOST — that's what the
// shell's own file-copy tools are for — so a non-volume destination is
// rejected with a clear error rather than silently falling back to an
// ordinary host-to-host copy.
//
// Only /QUIET, /VERBOSE, /TEST, and /BINARY apply here, the same subset
// cmdCopy's own volume-destination direction supports and for the same
// reasons (see its doc comment): a plain host file has no VMS record
// format, revision date, or subdirectory structure of its own for /TIME,
// /IGNORE, /DIRS, or the line-ending qualifiers to act on.
func cmdCopyFromHost(s *Session, args []string, quals Qualifiers) error {
	hostPath := args[0]

	info, err := os.Stat(hostPath)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if info.IsDir() {
		return fmt.Errorf("copy: %s: is a directory, not a file", hostPath)
	}

	destVol, destSpec, toVolume, err := volumeDestination(s, args[1])
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	if !toVolume {
		return fmt.Errorf("copy: /host requires destination to name a location on a mounted volume (device:[dir]name.type)")
	}

	hostName, hostType := hostBaseNameType(hostPath)
	name, typ := volumeDestName(destSpec, filespec.Match{Name: hostName, Type: hostType})
	destName := (filespec.Spec{Device: destSpec.Device, Dirs: destSpec.Dirs, Name: name, Type: typ}).String()

	test := quals.Has("test")
	if test {
		fmt.Fprintf(s.Stdout, "%%COPY-I-TEST, would copy %s to %s\n", hostPath, destName)

		return nil
	}

	if quals.Has("verbose") {
		fmt.Fprintf(s.Stdout, "%%COPY-I-COPYING, copying %s to %s\n", hostPath, destName)
	}

	destDir, destBm, destIb, err := resolveVolumeDest(destVol, destSpec)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	src, err := os.Open(hostPath)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	defer src.Close()

	version, err := copyHostFileToVolume(destVol, destDir, destBm, destIb, name, typ, src, info.Size(), quals.Has("binary"))
	if err != nil {
		return fmt.Errorf("copy: %s: %w", hostPath, err)
	}

	if !quals.Has("quiet") {
		destVersion := (filespec.Spec{Device: destSpec.Device, Dirs: destSpec.Dirs, Name: name, Type: typ, Version: fmt.Sprint(version)}).String()
		fmt.Fprintf(s.Stdout, "%%COPY-S-COPIED, %s copied to %s\n", hostPath, destVersion)
	}

	return nil
}

// hostBaseNameType splits a host path's base name into a VMS-style
// name/type pair, the same way an ordinary file spec's text is split into
// name and type (see splitNameTypeVersion): on the LAST '.', so a name
// with several dots (e.g. "archive.tar.gz") keeps every earlier one as
// part of the name. A base name with no '.' at all gets an empty type,
// exactly like a VMS file with no type.
//
// The result is always upper-cased, unlike the rest of this project's
// name handling (which never upper-cases text typed directly as VMS
// syntax, since matching is already case-insensitive throughout). A host
// base name is different: it's ordinary host-filesystem text with no VMS
// convention behind its case at all (typically lower- or mixed-case on
// this project's target platforms), so left alone it would produce a
// file that merely *looks* unlike anything real VMS ever wrote. Real
// VMS's own DCL upper-cases unquoted command-line text uniformly for
// exactly this reason; upper-casing just this one host-sourced name is
// the narrow equivalent for /HOST, without changing how any *typed* VMS
// name elsewhere in this project is handled.
func hostBaseNameType(path string) (name, typ string) {
	base := filepath.Base(path)
	if idx := strings.LastIndexByte(base, '.'); idx != -1 {
		return strings.ToUpper(base[:idx]), strings.ToUpper(base[idx+1:])
	}

	return strings.ToUpper(base), ""
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

// copyOneFileToVolume copies src's content into a brand-new file
// name.typ in destDir (using destBm/destIb, destDir's device's bitmap
// caches — see Device.Bitmap/IndexBitmap), auto-assigning the next
// version number the same way CreateFile always does, and returns that
// version for the caller's own confirmation message.
//
// binary selects the same two modes copyOneFile's own read-side direction
// offers: /BINARY creates an Undefined-format file and copies src's exact
// bytes through unchanged (copyRawToVolume); otherwise the destination is
// created Stream_LF and src's records are reframed as plain '\n'-delimited
// text (copyRecordsToVolume), the simplest text convention to target
// without negotiating a full record-format/carriage-control choice on the
// write side — see cmdCopy's own doc comment for why /STREAM, /IGNORE,
// and the line-ending qualifiers don't carry over to this direction.
func copyOneFileToVolume(destVol *volume.Volume, destDir *volume.Directory, destBm *volume.Bitmap, destIb *volume.IndexBitmap, name, typ string, src *volume.File, binary bool) (uint16, error) {
	fullName := name + "." + typ

	recAttr := ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF}
	if binary {
		recAttr = ondisk.RecAttr{Format: ondisk.RecordFormatUndefined, MaxRecordSize: ondisk.BlockSize}
	}

	dst, err := destVol.CreateFile(destDir, fullName, recAttr, destBm, destIb)
	if err != nil {
		return 0, fmt.Errorf("creating %s: %w", fullName, err)
	}

	if binary {
		err = copyRawToVolume(dst, src)
	} else {
		err = copyRecordsToVolume(dst, src)
	}

	if err != nil {
		return 0, fmt.Errorf("writing %s: %w", fullName, err)
	}

	// CreateFile/Directory.Insert already assigned the version actually
	// used (the highest that existed for fullName before this call, plus
	// one); looking it back up via Lookup, rather than threading it back
	// out of CreateFile itself, keeps that bookkeeping entirely inside
	// Directory, where it already lives.
	entry, err := destDir.Lookup(fullName, 0)
	if err != nil {
		return 0, fmt.Errorf("looking up newly created %s: %w", fullName, err)
	}

	return entry.Version, nil
}

// copyHostFileToVolume copies src's content into a brand-new file
// name.typ in destDir — the /HOST-source counterpart of
// copyOneFileToVolume (see its own doc comment for the shared parts of
// this design), reading from a plain host *os.File instead of an
// already-open volume.File. binary selects the same two modes: /BINARY
// creates an Undefined-format file and copies src's exact bytes through
// unchanged (copyHostRawToVolume); otherwise the destination is created
// Stream_LF and src's text is reframed into individual records
// (copyHostRecordsToVolume).
func copyHostFileToVolume(destVol *volume.Volume, destDir *volume.Directory, destBm *volume.Bitmap, destIb *volume.IndexBitmap, name, typ string, src *os.File, size int64, binary bool) (uint16, error) {
	fullName := name + "." + typ

	recAttr := ondisk.RecAttr{Format: ondisk.RecordFormatStreamLF}
	if binary {
		recAttr = ondisk.RecAttr{Format: ondisk.RecordFormatUndefined, MaxRecordSize: ondisk.BlockSize}
	}

	dst, err := destVol.CreateFile(destDir, fullName, recAttr, destBm, destIb)
	if err != nil {
		return 0, fmt.Errorf("creating %s: %w", fullName, err)
	}

	if binary {
		err = copyHostRawToVolume(dst, src, size)
	} else {
		err = copyHostRecordsToVolume(dst, src)
	}

	if err != nil {
		return 0, fmt.Errorf("writing %s: %w", fullName, err)
	}

	entry, err := destDir.Lookup(fullName, 0)
	if err != nil {
		return 0, fmt.Errorf("looking up newly created %s: %w", fullName, err)
	}

	return entry.Version, nil
}

// copyHostRawToVolume copies src's exact bytes to dst block by block (the
// /BINARY case for a /HOST source), zero-padding the final partial block
// the same way a whole-block write always must, then records dst's true
// byte length via CloseWithFinalByte — the /HOST-source counterpart of
// copyRawToVolume, reading from a plain io.Reader with a known size
// instead of a volume.File whose valid length comes from
// rms.FileByteLength.
func copyHostRawToVolume(dst *volume.File, src io.Reader, size int64) error {
	buf := make([]byte, ondisk.BlockSize)
	remaining := size

	for vbn := uint32(1); remaining > 0; vbn++ {
		n, err := io.ReadFull(src, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}

		for i := n; i < len(buf); i++ {
			buf[i] = 0
		}

		if err := dst.WriteBlock(vbn, buf); err != nil {
			return err
		}

		remaining -= int64(n)
	}

	return dst.CloseWithFinalByte(uint16(size % ondisk.BlockSize))
}

// copyHostRecordsToVolume reframes src's plain host text as a Stream_LF
// file on dst (the default, non-/BINARY case for a /HOST source): split on
// '\n', trimming a trailing '\r' so CRLF-terminated host text works the
// same as LF-terminated text, and each line becomes one Stream_LF record
// via rms.Writer. Unlike copyRecordsToVolume (a volume source, whose
// original record format/carriage-control needs writeRecords' full
// per-format logic to interpret), a plain host file has no VMS record
// structure of its own to reframe — it's already linear text.
func copyHostRecordsToVolume(dst *volume.File, src io.Reader) error {
	w, err := rms.NewWriter(dst)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(src)

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if err := w.Put([]byte(line)); err != nil {
				return err
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}

			return readErr
		}
	}

	return w.Close()
}

// copyRawToVolume copies src's exact valid bytes (see rms.FileByteLength)
// to dst block by block, matching copyBinary's own read-side convention,
// then records dst's true byte length — which may end partway through its
// last block — via CloseWithFinalByte rather than Close's own
// whole-block-only rounding.
func copyRawToVolume(dst, src *volume.File) error {
	total := rms.FileByteLength(src.Header.RecordAttributes)
	remaining := total

	buf := make([]byte, ondisk.BlockSize)
	for vbn := uint32(1); remaining > 0; vbn++ {
		if err := src.ReadBlock(vbn, buf); err != nil {
			return err
		}

		if err := dst.WriteBlock(vbn, buf); err != nil {
			return err
		}

		remaining -= int64(len(buf))
	}

	return dst.CloseWithFinalByte(uint16(total % ondisk.BlockSize))
}

// copyRecordsToVolume reframes src's records as a Stream_LF file on dst.
// It reuses writeRecords' own per-format logic (VFC carriage-control
// expansion, Fixed/Variable/Stream all normalized to plain '\n'-terminated
// text) — the same code that already builds a host text file's content —
// via lineSplitWriter, which re-splits that text back into individual
// records for rms.Writer.Put to apply Stream_LF's own framing to.
func copyRecordsToVolume(dst, src *volume.File) error {
	w, err := rms.NewWriter(dst)
	if err != nil {
		return err
	}

	lw := &lineSplitWriter{put: w.Put}
	if err := writeRecords(lw, src, lfLineEnding); err != nil {
		return err
	}

	if len(lw.buf) > 0 {
		// A trailing "line" with no terminating '\n' at all — possible
		// when src's own last record has no natural terminator (a VFC
		// record using a carriage-control that suppresses the trailing
		// line feed, most notably). Flushed as a final record rather than
		// silently dropped.
		if err := w.Put(lw.buf); err != nil {
			return err
		}
	}

	return w.Close()
}

// lineSplitWriter is an io.Writer adapter that calls put once per
// '\n'-terminated line written to it (excluding the '\n' itself),
// buffering only whatever partial line hasn't seen its terminator yet —
// the bridge between writeRecords, which writes a stream of already
// line-terminated text, and rms.Writer, whose Put wants each record
// handed to it individually so it can apply its own destination format's
// framing.
type lineSplitWriter struct {
	buf []byte
	put func([]byte) error
}

func (l *lineSplitWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != '\n' {
			l.buf = append(l.buf, b)

			continue
		}

		if err := l.put(l.buf); err != nil {
			return 0, err
		}

		l.buf = l.buf[:0]
	}

	return len(p), nil
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
