# ods2

A native Go implementation of ODS2, a tool for reading VAX/VMS "Files-11"
(ODS-2) disk volumes and images. This is a from-scratch Go rewrite of the
architecture and file-format knowledge in the C [ods2](https://github.com/DaveShepperd/ods2)
project (itself descended from Paul Nankervis's original, via Hunter
Goatley and crwolff) — not a line-by-line translation.

## Goals

- Pure Go, no CGO. Standard library plus well-established third-party
  packages only.
- Targets Linux, Windows, and macOS. No VMS-native code, no raw physical
  device or SCSI passthrough support — only disk image/container files
  (plain block dumps and raw optical-media sector dumps) are supported.
- **Library-first.** Everything except the `cmd/ods2` CLI is a clean,
  importable Go library, intended for reuse from other projects.

## Architecture

```text
github.com/tucats/ods2/
  vmstime/            VMS quadword time <-> time.Time
  diskimage/          container abstraction: plain image / raw-CD sector image,
                      exposed as 512-byte logical block access
  ondisk/             byte-exact ODS-2 on-disk structures + decoders
  volume/             mount + file/directory access built on ondisk + diskimage
  filespec/           VMS file-spec parsing + wildcard matching/glob
  rms/                record-format read layer (Fixed/VAR/VFC/Stream*)
  cmd/ods2/           the CLI: REPL + one-shot subcommands
    internal/session/ interactive session state + command table/handlers
    internal/repl/    line-editing REPL loop
  testdata/           fixtures for automated tests
```

`vmstime`, `diskimage`, `ondisk`, `volume`, `filespec`, and `rms` are the
public library surface, free of any CLI/REPL concerns. `cmd/ods2` (and its
internal `session`/`repl` packages) is the only CLI-specific code.

### Why not a line-by-line port

The reference C code's structure is heavily shaped by things Go doesn't need
to replicate:

- A generic AVL-tree + LRU object cache (`cache.c`) that mainly exists to
  provide write-back buffering and object de-duplication for the (buggy)
  write path — for read-only access this collapses to plain maps, or no
  cache at all.
- Three parallel physical-I/O backends (Unix, Windows/ASPI-SCSI, VMS-native)
  full of raw-device opens, CD-ROM geometry ioctls, and SCSI passthrough —
  all droppable once the only supported input is a container/image file.
  Go's `os.File` plus `io.ReaderAt`/`io.WriterAt` natively handles 64-bit
  offsets on every target platform, no `_LARGEFILE64_SOURCE`-style workaround
  needed.
- VMS "swapped longword" and optional big-endian byte-swap macros — the Go
  port always decodes explicitly via `encoding/binary.LittleEndian`, so host
  endianness is a non-issue. The one on-disk quirk that must still be
  preserved is the RMS-inherited 16-bit-half word-swap on two specific
  fields (`fat$l_hiblk`, `fat$l_efblk`).
- VMS string descriptors and the VMS odd/even success/failure status
  convention — replaced by plain Go `string`/`error`.
- A resumable, pointer-linked wildcard-search cursor, needed because VMS
  RMS's `$SEARCH` is a call-based iteration API — replaced by ordinary Go
  recursion/slices.

What must be preserved faithfully, because it's the actual ODS-2 file
format rather than an artifact of C or VMS RMS: the home block / index file
/ file header / directory record byte layouts, the four-format
retrieval-pointer (extent) encoding, VAR/VFC record framing, and
VMS-quadword time semantics. These are publically available data structure;
the C reference version was consulted for comfiration but not copied
directly.

### Feature Set

The Go packages in this project (and the CLI that uses them) have the following features

- Initialize a new empty ODS-2 volume as a container file.
- Mount a container file. By default, mounted readonly, but can be mounted
  for read/write.
- Manager a "default directory" path.
- Get a directory listing of files
- Type the contents of a file
- Copy files within the container
- Copy container files to the host system, and host system files into
  the container.
- Delete files in the container.
- Set verion limits for files and directories.
- Purge older versions of files in the container.
- Analyze the container disk and perform repairs caused by abends
  that terminated execution without unmounting a volume (which
  ensures the BITMAP.SYS file in the container is up-to-date).
- Ability to read and write ODS-2 disk containers, and read CD-ROM containers
  and raw CD-ROM image files.
