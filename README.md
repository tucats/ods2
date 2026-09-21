# ods2

A native Go implementation of ODS2, a tool for reading VAX/VMS "Files-11"
(ODS-2) disk volumes and images. This is a from-scratch Go rewrite of the
architecture and file-format knowledge in the C [ods2](https://github.com/DaveShepperd/ods2)
project (itself descended from Paul Nankervis's original, via Hunter
Goatley and crwolff) — not a line-by-line translation.

**Status: feature-complete for read access.** The public library surface —
`vmstime`, `diskimage`, `ondisk`, `volume`, `filespec`, `rms` — and the
`cmd/ods2` CLI (interactive REPL and one-shot subcommands) are both
implemented, unit-tested, and verified end to end against a real compiled
binary: `mount`, `dismount`, `directory`/`dir`, `copy`, `search`, `type`,
`difference`, `set default`, `show`, `help`, `exit`/`quit`. Remaining polish
(golden-file tests against real ODS-2 images, a few of `copy`'s more niche
qualifiers) is tracked below.

## Goals

- Pure Go, no CGO. Standard library plus well-established third-party
  packages only.
- Targets Linux, Windows, and macOS. No VMS-native code, no raw physical
  device or SCSI passthrough support — only disk image/container files
  (plain block dumps and raw optical-media sector dumps) are supported.
- Read-only in this phase: mount, directory listing, copy-off, search, type,
  diff. Write/create support (file creation, deletion, bitmap allocation) is
  explicitly deferred to a future phase with its own design phase.
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
VMS-quadword time semantics.

### Disk image containers

Unlike the C tool, which requires a separate preprocessing step
(`deraw_cdimage.py`) to strip sync/header/ECC bytes from a raw CD-ROM sector
dump before it can be mounted, the Go `diskimage` package detects and
de-frames both supported container kinds transparently:

- **Plain images** — raw ODS-2 volume bytes back-to-back (this also covers
  2048-byte-sector ISO dumps, since 2048 is an exact multiple of the
  512-byte ODS-2 logical block size).
- **Raw CD-ROM sector dumps** — 2352 bytes/sector (12-byte sync + 4-byte
  header + 2048 bytes user data + ECC/EDC), detected by file size and sync
  pattern, de-framed on the fly per read.

## Development plan

See the architecture section above for the package layout.

1. ✅ Scaffolding: `go.mod`, package skeletons, CI.
2. ✅ `diskimage`: plain + raw-CD container support.
3. ✅ `ondisk`: on-disk structure decode + checksum validation.
4. ✅ `volume`: mount, index file bootstrap, file header access, virtual-to-
   logical block mapping, directory list/lookup.
5. ✅ `filespec`: VMS spec parsing + wildcard/recursive glob.
6. ✅ `rms`: record-format reading, VFC decode.
7. ✅ `cmd/ods2`: full command set (`mount`, `dismount`, `directory`/`dir`,
   `copy`, `search`, `type`, `difference`, `set default`, `show`, `help`,
   `exit`/`quit`), REPL (via `github.com/chzyer/readline`) and one-shot CLI
   (via `github.com/spf13/cobra`) sharing the same command implementations.
8. 🔶 Polish (in progress): automated end-to-end CLI tests against a
   synthetic image ✅; `copy`'s `/time` and `/ignore` qualifiers ✅; still
   open: golden-file tests against real (non-synthetic) ODS-2 images,
   explicit cross-platform CI verification, and `copy`'s remaining niche
   qualifiers (`/dirs`, `/stream`, `/vfc`, `/crlf`, `/lf`) — text-mode
   copying already handles the common VFC/Stream/Variable cases correctly
   via package `rms`, just without those specific overrides yet.

Explicitly out of scope for now (future work): write/create support,
ODS-5 support, raw physical device mounting.

## Credits

Paul Nankervis / Hunter Goatley / crwolff — original C implementation and
subsequent forks. See the C reference at
[https://github.com/DaveShepperd/ods2](https://github.com/DaveShepperd/ods2)
which is derived from several predecessor versions of the original forks.
