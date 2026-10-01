# Phase 4 — directories as VMS's CREATE/DIRECTORY makes them

**Status:** done (2026-10-01), with govax's Phase 34.

This phase is planned and tracked in govax's
[`docs/PHASE-34.md`](https://github.com/tucats/govax/blob/main/docs/PHASE-34.md),
which adds VMS's `CREATE/DIRECTORY` to govax and checks the result against
VMS 7.3. This document only logs the work that phase does in this
repository.

## What changes here

- **`volume`**: `CreateDirectory` takes `DirectoryOptions` (version limit,
  owner, protection, allocation) and lays a directory out as VMS 7.3 does:
  directory and contiguous, variable-length no-span records of at most 512
  bytes, born with its allocation and an empty first block, IDENT revision
  0, and always version 1. `NewFileHeader` gains `Owner` and `Protection`.
- **`filespec`**: `CreateDirectoryPath` makes each missing level of a
  directory path, with options worked out against each level's parent.
- **The CLI**: `CREATE DIRECTORY` makes several levels at once and takes
  `/OWNER`, `/PROTECTION`, and `/ALLOCATION`.

## Progress log

- 2026-10-01: `volume` (govax Phase 34, subtask 2). `CreateDirectory` with
  `DirectoryOptions` (`createdir.go`), `NewFileHeader.Owner`/`Protection`,
  and a directory's IDENT revision starting at 0. A directory that already
  exists, in any version, is now `ErrExists` rather than a new version. A
  failure part way gives back the header and space. The layout was read
  from directories VMS 7.3 made on govax's Phase 33 oracle volume.
- 2026-10-01: `ondisk.ParseUic`, `ParseProtection`, `FormatProtection`,
  and `ProtectionAccess`, so ods2's CLI and govax read `/OWNER_UIC` and
  `/PROTECTION` the same way.
- 2026-10-01: `filespec.CreateDirectoryPath` (`createdir.go`) makes every
  missing level, each with options from a callback given its own parent;
  levels made before a failure stay made and are reported.
  `volume.InheritedDirectoryOptions` gives VMS's defaults (the parent's
  limit, its protection less delete). The CLI's `CREATE DIRECTORY` uses
  them, makes several levels, reports an existing directory (the MFD too)
  as `%CREATE-I-EXISTS`, and gains `/OWNER`, `/PROTECTION`, and
  `/ALLOCATION`.
- 2026-10-01: Reconciled with VMS 7.3's run of govax's CREATE/DIRECTORY
  oracle. A new directory's entry in its parent has no version limit,
  whatever the parent's default (`Directory.insert`). A directory's new
  space is zeroed, every block, and its high-water mark set past it
  (VMS's `/ALLOCATION=4` directory reads HWM 5), and adding or removing
  entries never lowers the mark. `CreateDirectoryPath` refuses a ninth
  level, and a name over 39 characters, before making anything
  (`volume.ErrDirectoryName`, VMS's RMS$_DIR).
