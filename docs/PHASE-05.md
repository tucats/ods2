# Phase 5 — files shared by several users

**Status:** in progress (2026-10-08), with govax's Phase 47.

This phase is planned and tracked in govax's
[`docs/PHASE-47 - RMS and processes.md`](https://github.com/tucats/govax/blob/main/docs/PHASE-47%20-%20RMS%20and%20processes.md),
which lets several emulated VAX processes use one file at once, with VMS's
sharing rules. This document only logs the work that phase does in this
repository.

## What changes here

- **`volume`: accessing a file** (`access.go`). `Volume.Access(fid,
  AccessMode)` opens a file for one accessor, who says whether it will
  write the file and whether it lets others read it (`NoRead` clear) or
  write it (`NoWrite` clear); a conflicting access fails with
  `ErrAccessConflict` (VMS's SS$_ACCONFLICT). `Access.Deaccess` ends it.
  `AccessFile` is the same for a `File` in hand (the one `CreateFile` just
  made). The rule is the file system's: every accessor reads, so an
  access fails if someone denies reading, if it writes and someone denies
  writing, if it denies reading and anyone has the file, or if it denies
  writing and someone writes.
- **One `File` per open file.** While a file is accessed, every `Access`
  of it and every `OpenFID` of its ID return the same `*File`, VMS's file
  control block: an extension, an end of file, or a header change made
  through one is what all see. The last `Deaccess` writes the header back
  (`WriteAttributes`). A file no one accesses gets a `File` of its own
  from `OpenFID`, read from the disk, as before. A `Close` through another
  path doesn't disarm a `File` that accessors are writing.
- **The end of file** as a value of the shared `File`: `SetEndOfFile(ebk,
  ffb)` (moving the high-water mark past it) and `WriteAttributes`.
- **Deleting an open file.** `DeleteFile` and `DeleteHeader` on an
  accessed file remove the directory entry (for `DeleteFile`) at once and
  mark the file for delete; its header and blocks are freed by its last
  `Deaccess`, and meanwhile it can't be accessed again
  (`ErrMarkedForDelete`). A version limit's purge, going through
  `DeleteFile`, is covered too.
- **`rms`**: `NewAppender` starts a `Writer` at the file's end of file
  (the partly used last block read back in). `Writer.SetShared(true)`
  makes each `Put` go at the file's end of file as it is then, which
  another writer may have moved, and write the record through, moving the
  end of file, before returning; `Flush` does the same for an unshared
  `Writer` without closing it, and a shared `Writer`'s `Close` is a
  `Flush`. A `Reader` asks the file for its end of file each time it
  reaches it, and reads a partly used block again from where its data
  ended, so it sees records appended while it reads.

## Progress log

- 2026-10-08: All of the above (govax Phase 47, subtasks 3 and 4).
