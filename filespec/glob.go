package filespec

import (
	"fmt"
	"strings"

	"github.com/tucats/ods2/ondisk"
	"github.com/tucats/ods2/volume"
)

// Match is one file Glob found: its Fid, the directory path it was found
// in, and its resolved name, type, and version — everything needed to
// build a full VMS file spec string for it, or to open its data directly
// via vol.OpenFID(Match.Fid).
type Match struct {
	Fid     ondisk.Fid
	Dirs    []string
	Name    string
	Type    string
	Version uint16
}

// dirNode pairs an already-open Directory with the path (from the
// volume's master file directory) it was reached by, so results can
// report where they were found.
type dirNode struct {
	path []string
	dir  *volume.Directory
}

// Glob resolves spec against vol, expanding any wildcards in its
// directory path, name, and type, and returns every matching file.
//
// spec.Dirs is resolved one component at a time, starting from the
// volume's master file directory (ondisk.MasterFileDirectoryFid): each
// component narrows the search into a matching subdirectory of the
// previous level, with '*'/'%' wildcards matched against that level's
// subdirectory names exactly as they would be against file names. If
// spec.Recursive is set (VMS's "[dir...]" syntax), matching continues
// into every subdirectory beneath the directories spec.Dirs reaches, to
// any depth.
//
// An empty spec.Name or spec.Type is treated as "*" — matching DCL's own
// behavior when you ask to list a directory without typing a file name at
// all.
func Glob(vol *volume.Volume, spec Spec) ([]Match, error) {
	versionSel, err := parseVersionSelector(spec.Version)
	if err != nil {
		return nil, err
	}

	namePattern := spec.Name
	if namePattern == "" {
		namePattern = "*"
	}

	typePattern := spec.Type
	if typePattern == "" {
		typePattern = "*"
	}

	nodes, err := walkDirs(vol, spec.Dirs)
	if err != nil {
		return nil, err
	}

	if spec.Recursive {
		nodes, err = expandRecursive(vol, nodes)
		if err != nil {
			return nil, err
		}
	}

	var all []Match

	for _, node := range nodes {
		found, err := globInDirectory(node, namePattern, typePattern, versionSel)
		if err != nil {
			return nil, err
		}

		all = append(all, found...)
	}

	return all, nil
}

// walkDirs resolves each component of dirPath against vol in turn,
// starting from the master file directory, expanding wildcards at every
// level, and returns every directory reached at the final depth (there
// may be more than one, if a component's wildcard matched several
// subdirectories at that level).
func walkDirs(vol *volume.Volume, dirPath []string) ([]dirNode, error) {
	root, err := vol.OpenDirectory(ondisk.MasterFileDirectoryFid)
	if err != nil {
		return nil, fmt.Errorf("filespec: opening the master file directory: %w", err)
	}

	current := []dirNode{{dir: root}}

	for _, component := range dirPath {
		var next []dirNode

		for _, node := range current {
			children, err := matchingSubdirectories(vol, node, component)
			if err != nil {
				return nil, err
			}

			next = append(next, children...)
		}

		current = next
	}

	return current, nil
}

// ResolveDirectory resolves a literal directory path (as found in
// Spec.Dirs, e.g. from a parsed destination file spec) to the Directory it
// names, starting from vol's master file directory — the same walk Glob
// itself does internally (walkDirs) to reach the directory a file spec's
// own name/type pattern is matched within, exposed here for callers that
// need the destination *directory itself*, open and ready to write into
// (Directory.Insert), rather than a listing of files inside it.
//
// Unlike Glob, dirs is not expanded as a wildcard pattern: each component
// must name an existing subdirectory (matched case-insensitively, the same
// as any other VMS name comparison in this package), and the result is an
// error unless that resolves to exactly one directory — unambiguous, since
// a write destination has to be.
func ResolveDirectory(vol *volume.Volume, dirs []string) (*volume.Directory, error) {
	nodes, err := walkDirs(vol, dirs)
	if err != nil {
		return nil, err
	}

	switch len(nodes) {
	case 0:
		return nil, fmt.Errorf("filespec: directory %s not found", formatDirPath(dirs))
	case 1:
		return nodes[0].dir, nil
	default:
		return nil, fmt.Errorf("filespec: directory %s is ambiguous (%d matches)", formatDirPath(dirs), len(nodes))
	}
}

// expandRecursive returns nodes plus every directory nested beneath each
// one, to any depth, for spec.Recursive's "and everything below" behavior.
func expandRecursive(vol *volume.Volume, nodes []dirNode) ([]dirNode, error) {
	all := append([]dirNode{}, nodes...)
	queue := append([]dirNode{}, nodes...)

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		children, err := matchingSubdirectories(vol, node, "*")
		if err != nil {
			return nil, err
		}

		all = append(all, children...)
		queue = append(queue, children...)
	}

	return all, nil
}

// matchingSubdirectories lists node's entries, keeps only the ones that
// are directories (name ends in ".DIR"), narrows repeated versions of the
// same subdirectory name down to the highest (an older version of FOO.DIR
// is not a separate directory to search), and opens each one whose name
// matches namePattern.
func matchingSubdirectories(vol *volume.Volume, node dirNode, namePattern string) ([]dirNode, error) {
	entries, err := node.dir.List()
	if err != nil {
		return nil, fmt.Errorf("filespec: listing %s: %w", formatDirPath(node.path), err)
	}

	var dirEntries []ondisk.DirEntry

	for _, e := range entries {
		if strings.HasSuffix(strings.ToUpper(e.Name), ".DIR") {
			dirEntries = append(dirEntries, e)
		}
	}

	dirEntries = selectVersions(dirEntries, versionSelector{kind: versionHighest})

	children := make([]dirNode, 0, len(dirEntries))

	for _, e := range dirEntries {
		name, _ := splitEntryNameType(e.Name)
		if !matchWildcard(namePattern, name) {
			continue
		}

		sub, err := vol.OpenDirectory(e.Fid)
		if err != nil {
			return nil, fmt.Errorf("filespec: opening %s.%s: %w", formatDirPath(node.path), name, err)
		}

		childPath := append(append([]string{}, node.path...), name)
		children = append(children, dirNode{path: childPath, dir: sub})
	}

	return children, nil
}

// globInDirectory matches node's entries against namePattern/typePattern,
// selects versions per versionSel, and returns the results as Matches.
func globInDirectory(node dirNode, namePattern, typePattern string, versionSel versionSelector) ([]Match, error) {
	entries, err := node.dir.List()
	if err != nil {
		return nil, fmt.Errorf("filespec: listing %s: %w", formatDirPath(node.path), err)
	}

	var candidates []ondisk.DirEntry

	for _, e := range entries {
		name, typ := splitEntryNameType(e.Name)
		if matchWildcard(namePattern, name) && matchWildcard(typePattern, typ) {
			candidates = append(candidates, e)
		}
	}

	selected := selectVersions(candidates, versionSel)

	matches := make([]Match, 0, len(selected))

	for _, e := range selected {
		name, typ := splitEntryNameType(e.Name)
		matches = append(matches, Match{
			Fid:     e.Fid,
			Dirs:    node.path,
			Name:    name,
			Type:    typ,
			Version: e.Version,
		})
	}

	return matches, nil
}

// splitEntryNameType splits a directory entry's combined "NAME.TYPE" text
// (see ondisk.DirEntry.Name) into its name and type parts.
func splitEntryNameType(entryName string) (name, typ string) {
	if idx := strings.LastIndexByte(entryName, '.'); idx != -1 {
		return entryName[:idx], entryName[idx+1:]
	}

	return entryName, ""
}

// formatDirPath renders a directory path the way VMS would, e.g.
// "[FOO.BAR]", for use in error messages.
func formatDirPath(path []string) string {
	if len(path) == 0 {
		return "[000000]"
	}

	return "[" + strings.Join(path, ".") + "]"
}
