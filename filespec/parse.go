package filespec

import (
	"fmt"
	"strings"
)

// Parse parses a VMS file specification, filling in any component the
// input text leaves unspecified from def — typically the caller's current
// default directory, the same role VMS's own "current default device and
// directory" plays when you type a partial file spec like "FOO.TXT" at
// the DCL prompt.
//
// Directory components are the one part of a file spec VMS lets you write
// relative to your default directory, rather than always spelling out in
// full:
//
//   - "[FOO.BAR]" (no leading '.' or '-') is always an ABSOLUTE directory
//     path from the volume's master file directory, replacing def's
//     directory entirely.
//   - "[.BAR]" descends from def's directory into a subdirectory BAR.
//   - "[-]" moves up one level from def's directory.
//   - "[-.BAR]" moves up one level, then descends into BAR.
//   - "[--.BAR]" moves up two levels, then descends into BAR — and so on:
//     each additional leading '-' means one more level up.
//   - "[000000]" and "[]" both mean the master file directory itself.
//
// Moving up past the master file directory is an error.
func Parse(raw string, def Spec) (Spec, error) {
	device, rest, err := splitDevice(raw)
	if err != nil {
		return Spec{}, err
	}

	dirText, hadBrackets, rest, err := splitDirectory(rest)
	if err != nil {
		return Spec{}, err
	}

	name, typ, version := splitNameTypeVersion(rest)

	spec := Spec{
		Device:  device,
		Name:    name,
		Type:    typ,
		Version: version,
	}

	if spec.Device == "" {
		spec.Device = def.Device
	}

	if spec.Name == "" {
		spec.Name = def.Name
	}

	if spec.Type == "" {
		spec.Type = def.Type
	}

	if spec.Version == "" {
		spec.Version = def.Version
	}

	if !hadBrackets {
		// No "[...]" at all in the input text: inherit the default
		// directory completely, the same way an unwritten Name or Type
		// does above.
		spec.Dirs = def.Dirs
	} else {
		dirs, recursive, err := resolveDirectory(dirText, def.Dirs)
		if err != nil {
			return Spec{}, err
		}

		spec.Dirs = dirs
		spec.Recursive = recursive
	}

	return spec, nil
}

// splitDevice splits a leading "device:" off raw, if present. A device
// spec, when present, is always terminated by a ':' that appears before
// any directory bracket — a ':' found only after a "[...]" isn't a device
// delimiter (nothing legitimately follows a directory spec that contains
// one), so that's reported as a syntax error rather than silently
// misparsed.
func splitDevice(raw string) (device, rest string, err error) {
	colonIdx := strings.IndexByte(raw, ':')
	if colonIdx == -1 {
		return "", raw, nil
	}

	bracketIdx := strings.IndexAny(raw, "[<")
	if bracketIdx != -1 && colonIdx > bracketIdx {
		return "", "", fmt.Errorf("filespec: unexpected ':' in %q", raw)
	}

	return raw[:colonIdx], raw[colonIdx+1:], nil
}

// splitDirectory splits a leading "[...]" or "<...>" off rest, if present.
// hadBrackets distinguishes "no directory was written at all" (inherit the
// default entirely) from "an empty directory was written" ("[]", treated
// as the master file directory — see resolveDirectory).
func splitDirectory(rest string) (dirText string, hadBrackets bool, remainder string, err error) {
	if rest == "" || (rest[0] != '[' && rest[0] != '<') {
		return "", false, rest, nil
	}

	closeChar := byte(']')
	if rest[0] == '<' {
		closeChar = '>'
	}

	end := strings.IndexByte(rest, closeChar)
	if end == -1 {
		return "", false, "", fmt.Errorf("filespec: unterminated directory bracket in %q", rest)
	}

	return rest[1:end], true, rest[end+1:], nil
}

// splitNameTypeVersion splits rest (whatever follows the device and
// directory) into name, type, and version, using the LAST ';' and the
// LAST '.' as delimiters — VMS file names themselves may not contain
// either character, so the last occurrence of each is always the real
// delimiter.
func splitNameTypeVersion(rest string) (name, typ, version string) {
	nameTypePart := rest
	if idx := strings.LastIndexByte(rest, ';'); idx != -1 {
		nameTypePart = rest[:idx]
		version = rest[idx+1:]
	}

	if idx := strings.LastIndexByte(nameTypePart, '.'); idx != -1 {
		return nameTypePart[:idx], nameTypePart[idx+1:], version
	}

	return nameTypePart, "", version
}

// resolveDirectory interprets a directory spec's text (the part between
// the brackets) against a default directory path, applying VMS's
// relative-directory rules (see Parse's documentation) when dirText
// starts with '.' or '-', and recognizing a trailing "..." (VMS's
// recursive-descent wildcard, e.g. "[FOO...]" or "[-.SYS*...]") regardless
// of which other form the rest of the text takes.
func resolveDirectory(dirText string, defDirs []string) (dirs []string, recursive bool, err error) {
	recursive = strings.HasSuffix(dirText, "...")
	if recursive {
		dirText = dirText[:len(dirText)-3]
	}

	if dirText == "" {
		if recursive {
			// "[...]" on its own means "the default directory and
			// everything beneath it", not "the root and everything
			// beneath it" — so, unlike a truly empty/unwritten
			// directory spec, this inherits def's directory rather than
			// resetting to the master file directory.
			return defDirs, true, nil
		}

		return nil, false, nil
	}

	if dirText == "000000" {
		return nil, recursive, nil
	}

	if dirText[0] != '.' && dirText[0] != '-' {
		// An absolute path: replaces the default directory entirely.
		return strings.Split(dirText, "."), recursive, nil
	}

	ups := 0

	i := 0
	for i < len(dirText) && dirText[i] == '-' {
		ups++
		i++
	}

	if ups > len(defDirs) {
		return nil, false, fmt.Errorf("filespec: directory spec %q goes above the master file directory", dirText)
	}

	base := append([]string{}, defDirs[:len(defDirs)-ups]...)
	remainder := dirText[i:]
	
	switch {
	case remainder == "":
		// Just "-", "--", etc.: move up and stop there.
		return base, recursive, nil
	case remainder[0] == '.' && len(remainder) > 1:
		// "-.BAR" or ".BAR": move up (0 or more levels), then descend.
		return append(base, strings.Split(remainder[1:], ".")...), recursive, nil
	default:
		return nil, false, fmt.Errorf("filespec: invalid directory spec %q", dirText)
	}
}
