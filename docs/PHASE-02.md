# Phase 2 — write support

This is the design and progress-tracking document for Phase 2: adding write
support to the Go ODS-2 implementation. [PHASE-01.md](PHASE-01.md) covered
the initial build-out (disk format, volume mount, *read-only* file/directory
access, and the `cmd/ods2` CLI) and is now feature-complete for that scope.

**Unlike PHASE-01.md, this document is a living record, not just a
retrospective checklist.** It's written *before* implementation starts, and
each subtask below is updated in place — status, design decisions actually
made, deviations from the plan, and a link to the commit(s) that closed it —
as work on it lands. Treat the "Subtasks" section as the source of truth for
"what's left" at any point during Phase 2; the top status table is a quick
summary of the same information.

## Goals

- Open an existing file for write access, or create a new file (with
  automatic filename version numbering — creating `FOO.TXT` when `FOO.TXT;3`
  already exists produces `FOO.TXT;4`), and write data to it.
- A `BITMAP.SYS` implementation: decode/encode the volume's storage
  (free-space) bitmap, and an allocator that can find and reserve free
  space from it.
- An in-memory cache for that bitmap (and the analogous INDEXF.SYS header
  bitmap — see [Two bitmaps, not one](#two-bitmaps-not-one)), so repeated
  allocations during one mount session don't each require a fresh disk
  read, with a well-defined flush point rather than the reference
  implementation's implicit, best-effort one (see
  [What we're deliberately not porting](#what-were-deliberately-not-porting-from-the-c-reference)).
- An `INITIALIZE` command/API that can build a fresh, minimal ODS-2 volume
  from scratch (root directory, INDEXF.SYS, BITMAP.SYS, and the rest of the
  reserved file set) on a container that doesn't have one yet — by "brute
  force" where that's simplest, matching how VMS's own INIT breaks the same
  chicken-and-egg bootstrapping problem Phase 1's `bootstrapIndexFile`
  already had to solve for *reading*.
- Dismounting a volume flushes the in-memory bitmap cache back to
  `BITMAP.SYS` before closing the container.
- An `ANALYZE/DISK` command that can review a mounted, writable volume for
  bitmap/allocation consistency and, given `/REPAIR`, regenerate a correct
  `BITMAP.SYS` from the volume's actual file allocations.

Because Phase 1 shipped with no external consumers of this module yet,
**breaking changes to any exported API are acceptable** where they produce a
cleaner design — notably `diskimage.Container` and `ondisk.FileHeader`/
`HomeBlock`, both of which need new capabilities Phase 1 never needed to
expose.

## Non-goals (explicitly out of scope for Phase 2)

- **File/directory deletion.** The reference implementation's own delete
  path is explicitly author-acknowledged as broken (`deallocfile()`,
  `update.c:451-453`: *"This routine has bugs and does NOT work properly
  yet!!!!"*), and this project has no correct design to fall back on yet.
  Phase 2 only *creates* file-header and directory-entry state; freeing it
  is a future phase with its own design pass.
- **Raw CD-ROM (`FormatRawCD`) write support.** These are 2352-byte sector
  dumps with sync/header/ECC framing around each logical block; write
  support means recomputing that framing, which isn't needed for this
  project's actual use case (working with plain disk images). Mounting a
  raw-CD container `/WRITE` fails with a clear error.
- **Volume sets (multi-disk volumes), for writing.** Phase 1 reads across a
  mounted volume set fine; Phase 2's allocator, `INITIALIZE`, and
  `ANALYZE/DISK` all target a single-device volume. The reference
  implementation's cross-device "whichever device has the most free space"
  heuristic (`update_addhead()`, `update.c:307-315`) is a crude load
  balancer, not something worth reproducing — multi-disk write is future
  work with its own design.
- **ODS-5 support, indexed/relative RMS files** — same scope boundary
  Phase 1 drew; unchanged.
- **A general-purpose block/object cache** (the reference implementation's
  AVL-tree + LRU `cache.c`). See
  [What we're deliberately not porting](#what-were-deliberately-not-porting-from-the-c-reference).

## Status

| # | Subtask | Status |
|---|---|---|
| 0 | Phase 1 bugfix: `Directory.List()` on partially-allocated directories | Done |
| 1 | `diskimage`: writable containers | Done |
| 2 | `ondisk`: fixed-layout encoders (HomeBlock, FileHeader, Fid, Uic, Ident, RecAttr) | Done |
| 3 | `ondisk`: retrieval-pointer encoder | Not started |
| 4 | `ondisk`: storage-bitmap bit-packing + SCB encoder | Not started |
| 5 | `ondisk`: directory-block encoder | Not started |
| 6 | `volume`: storage-bitmap cache & allocator (BITMAP.SYS) | Not started |
| 7 | `volume`: index-file header-slot cache & allocator (INDEXF.SYS) | Not started |
| 8 | `volume`: file-header writer (new headers, extension segments, HighWaterMark) | Not started |
| 9 | `volume`: directory mutation (insert + auto-extend + version assignment) | Not started |
| 10 | `volume`: file write API (open-for-write, CreateFile, WriteBlock) | Not started |
| 11 | `volume`: Dismount flush | Not started |
| 12 | `volume`+`cmd`: `INITIALIZE` | Not started |
| 13 | `rms`: record writer | Not started |
| 14 | `cmd/ods2`: `MOUNT /WRITE` + `DISMOUNT` wiring | Not started |
| 15 | `cmd/ods2`: `ANALYZE/DISK` | Not started |
| 16 | `cmd/ods2`: `COPY` host → volume direction (stretch goal) | Not started |

Legend: **Not started** / **In progress** / **Done** (commit `abc1234`) /
**Deferred** (with a reason).

---

## Design overview

### Two bitmaps, not one

A recurring source of confusion worth stating up front, because Phase 2
touches both and they're easy to conflate: an ODS-2 volume has **two
independent free-space bitmaps**, serving different purposes, at different
locations, allocated in different units:

1. **The index-file bitmap** (`HomeBlock.IndexBitmapVBN`/
   `IndexBitmapLBN`/`IndexBitmapSize`, already decoded in Phase 1) — one bit
   per **file-header slot** in `INDEXF.SYS` (i.e. per potential file
   number, up to `HomeBlock.MaxFiles`), recording which header slots are
   currently in use. It lives *inside* `INDEXF.SYS` itself, immediately
   before the header area (`volume.readFileHeaderViaIndex` already computes
   `vbn := fid.Number() - 1 + IndexBitmapVBN + IndexBitmapSize` to skip
   past it). Allocating a new file means finding a clear bit here.
2. **The storage bitmap, `BITMAP.SYS`** (file number 2) — one bit per
   **allocation cluster** (`HomeBlock.ClusterSize` blocks), recording which
   of the volume's data blocks are free. Its first block is the
   `StorageControlBlock` (`ondisk/scb.go`, decode-only today); the actual
   bitmap bits follow in subsequent blocks. Allocating space for file
   *data* (or for a new file-header extension segment, which also consumes
   a data extent) means finding a run of clear bits here.

Both are bitmaps, both need an encode/decode pair, an allocator, and a
cache — but they're separate structures at separate locations, and Phase 2
needs both. Where the plan below says "the bitmap cache" without
qualification for a design point that applies equally to either, both are
meant; subtasks 6 and 7 build one of each.

### Where the new code lives

Mirroring Phase 1's existing package boundaries:

- **`ondisk`** — byte-exact encode/decode of on-disk structures, no I/O, no
  allocation policy. Phase 1 left this package decode-only on purpose
  (`internal/odstest/odstest.go`'s doc comment explains why: *"package
  ondisk deliberately exposes no encoder — this project only ever needs to
  read ODS-2 volumes"*). Phase 2 reverses that: every `Decode*` function
  gets a symmetric `Encode*`, living in the same file, reusing the same
  private byte-offset constants. `internal/odstest`'s hand-rolled
  encoders (`BuildHomeBlockBytes`, `BuildFileHeaderBytes`,
  `EncodeExtentFormat2`, `putFidAt`, `putSwappedLongword`,
  `BuildDirRecordBytes`/`BuildDirBlock`) were exactly this, done ad hoc and
  test-only, because nothing else needed it yet; subtasks 2-5 promote that
  logic into real `ondisk` API and retire the duplicates once `ondisk`'s
  own encoders can produce identical bytes (a good place for round-trip
  `Decode(Encode(x)) == x` tests to live).
- **`volume`** — allocation policy, caching, and mutation semantics built
  on top of `ondisk` + `diskimage`: which cluster/header-slot to pick,
  when to flush, how a file gets extended, how a directory insert works.
  This is genuinely new code, not a promotion of existing test helpers.
- **`rms`** — record-format-aware writing (Fixed/Variable/VFC/Stream),
  symmetric to the existing `Reader`.
- **`diskimage`** — gets write I/O. `Container` itself is left unchanged
  (every existing implementation keeps compiling as-is); a new
  `WritableContainer` interface (`Container` plus `WriteBlock`) is
  implemented only by `plainImage`, matching how the stdlib separates
  `io.Reader`/`io.Writer`. Code that needs to write type-asserts for it and
  fails clearly ("raw CD-ROM images are read-only") rather than forcing
  `rawCDImage` to implement a `WriteBlock` that could never sensibly work.
- **`cmd/ods2/internal/session`** — new commands (`initialize.go`,
  `analyze.go`), following the exact `Command{...}` + `init()`-registration
  pattern every existing command already uses (see `mount.go` for the
  simplest complete example, `copy.go` for a larger one).

### Caching strategy

Phase 2 needs *some* in-memory state to avoid re-reading/re-writing bitmap
blocks on every single allocation within one mount session, but the
reference implementation's approach — a generic AVL-tree + LRU object pool
(`cache.c`) caching everything (file headers, directory blocks, bitmap
blocks alike) at a uniform 4-block chunk granularity, with write-back
deferred until either LRU pressure evicts an object or the volume is
dismounted — is overkill for what this project actually needs, and its
deferred-flush design is a real correctness gap (see next section). It
exists in the reference to support VMS RMS's multi-handle, long-lived-
process, memory-pressure-bounded model; this project is a single-process,
single-writer-at-a-time CLI tool.

Phase 2's cache is narrower and simpler on purpose: **just the two bitmaps**
(subtasks 6 and 7), loaded once at mount (or at the point a volume is first
opened `/WRITE`), mutated in memory as allocations happen, and flushed
through explicit, well-defined points rather than an implicit eviction
policy:

- Every command that finishes a mutating operation (`CREATE`, a `COPY`
  destined for the volume, `INITIALIZE`) flushes its own dirty bitmap
  writes before returning — no operation leaves dirty state hanging around
  hoping a future operation (or dismount) will pick it up.
- `DISMOUNT` flushes as a final safety net (subtask 11), matching the
  user-facing requirement this phase was asked to deliver, but is not the
  *only* flush point the way it is in the reference implementation.

File headers and directory blocks are **not** cached at all in Phase 2 —
they're written through immediately when modified (a header write is one
512-byte block; a directory-block rewrite is at most a handful). This
trades a small amount of avoidable I/O for a much simpler correctness
story: there's no dirty-tracking to get wrong for those structures, because
nothing is ever left dirty in memory.

### What we're deliberately not porting from the C reference

Per instruction: nothing below is ported as-is. Each item is either a bug,
a hardcoded shortcut that happens to work for whatever the author tested
against, or dead/disabled code. The Go design instead implements the same
*functionality* correctly from the on-disk format's actual rules (the
fields already exist in `ondisk.HomeBlock`/`FileHeader` for exactly this
reason).

| Reference code | Problem | Go design instead |
|---|---|---|
| `bitmap_search()` (`update.c:143-216`) | Not true best-fit despite the name; starts from a caller-supplied hint and stops at the first run "big enough," with **no wraparound** — a caller near EOF can spuriously fail to find free space that exists earlier on the volume. | A single deterministic full-bitmap scan (first-fit) every time. No hint plumbing, no partial-scan failure mode. Simpler code, and with no concurrent writers to protect a hint's locality benefit from, there's nothing to lose. |
| `update_findhead()`/`headmap_clear()` (`update.c:226-227,250`) | Compute the header bitmap's own location from `home.hm2$w_cluster*4 + 1` — a formula that happens to match the tested volume's layout — instead of the home block fields that exist for exactly this (`hm2$w_ibmapvbn`/`hm2$w_ibmapsize`), which the code *does* use correctly elsewhere (`accesshead()`, `access.c:106-107`). | Always derive the header-bitmap's location from `HomeBlock.IndexBitmapVBN`/`IndexBitmapSize`, consistently, everywhere. |
| `headmap_clear()` (`update.c:227`) | Hardcodes `if (head_no < 10) return 0` to protect reserved file headers from being freed, even though `HomeBlock.ReservedFiles` (`hm2$w_resfiles`) exists in the struct specifically for this and is never read anywhere in the C source. | Always read `HomeBlock.ReservedFiles` for this check. |
| `bitmap_modify()`'s `WORK_UNIT` packing (`update.c:22-29`) | Bits are packed into native-word-sized (`int`, 32 bits normally, but a `char` on some big-endian builds) units — an endianness- and word-size-dependent hack. | A fixed, explicit byte-oriented bit layout (defined once in `ondisk`, independent of host architecture), verified against a real volume's actual `BITMAP.SYS` bytes if a real writable test image becomes available (see [Testing strategy](#testing-strategy)). |
| `deallocfile()` (`update.c:455-530`) | Author-acknowledged broken; not safe to call (*"So DON'T use mount/write!!!"*). | Out of scope for Phase 2 entirely (see [Non-goals](#non-goals-explicitly-out-of-scope-for-phase-2)); a correct design is future work. |
| `#ifdef EXTEND` block (`update.c:552-679`) | Dead code, never compiled (macro never defined), and contains actual syntax errors. | Ignored entirely; not a source of truth for anything. |
| `sys_create()` (`rms.c:1843-1915`) | A test stub: ignores the parsed filename and directory FID it just computed correctly, hardcoding `"TEST.FILE;1"` and parent FID `11`. | Wire the real parsed name and directory Fid through to file/header creation (subtasks 9-10) — the parsing/searching machinery upstream of this stub in the C reference is fine and informs Phase 1's already-working `filespec`/`volume.Directory.Lookup`. |
| `bitmap_search()` result always encoded as retrieval-pointer **format 3** (`update.c:432-438`), regardless of whether the count/LBN would fit a smaller format. | Wastes header space; not incorrect, just needlessly wide. | The Go retrieval-pointer encoder (subtask 3) picks the smallest format that fits each extent, symmetric with the decoder already handling all four. |
| Cache write-back only on LRU eviction or dismount; the one explicit flush routine, `cache_flush()`, is dead code (never called; its one call site is commented out in `ods2.c:1821`). | A crash or abnormal exit mid-session can silently lose dirty blocks that are still "hot." | Deterministic flush points, not implicit ones — see [Caching strategy](#caching-strategy). |
| Directory block splitting (`insert_ent()`, `direct.c:293-385`) | Calls `exit(0)` — crashing the whole program — if inserting a new entry would require extending the directory *file* past its currently allocated blocks (comment: *"I can't extend a directory yet!!"*). | Subtask 9's directory insert calls the same file-extend path (subtask 8) any other file write would use; running out of room in a directory is an ordinary, handled case, not a fatal one. |

---

## Subtasks

Each subtask is sized to be one commit (per this repo's convention: commit
each complete, testable task on its own). "Depends on" lists other Phase 2
subtasks that must land first; Phase 1 is assumed complete throughout.

### 0. Phase 1 bugfix: `Directory.List()` on partially-allocated directories

**Depends on:** nothing — a Phase 1 correctness fix, not new Phase 2
functionality, but worth landing first: it blocks reliable use of
`testdata/rq0-ra92.dsk` (see
[Testing strategy](#testing-strategy)) for the rest of this phase, and
subtask 9's directory-insert path calls the same `List()` this bug is in.

Discovered by smoke-testing `testdata/rq0-ra92.dsk` (a real, writable
OpenVMS volume newly available locally — see
[Testing strategy](#testing-strategy)) against the existing `DIRECTORY`
command: `dir [000000]*.*` fails with `volume: decoding directory block 2:
ondisk: directory record size is inconsistent with its name length`.

Root cause: this volume's root directory header has
`RecordAttributes.HighestBlock` (allocated) = 3 but
`EndOfFileBlock`/`HighWaterMark` = 2 — one block of allocated-but-never-
written trailing space, which is completely normal (VMS pre-extends
directories by `HomeBlock.DefaultExtendSize` blocks at a time; this
volume's is 5). `Directory.List()` (`volume/directory.go`) walks every
block from `1` to `d.Blocks()`, where `Blocks()` returns the header's
*allocated* size, not its *used* size. Reading block 2 correctly returns
an all-zero buffer (`File.ReadBlock` honors `HighWaterMark`, per its
documented guarantee), but `ondisk.DecodeDirectoryBlock` doesn't recognize
an all-zero block as "no more data" — its end-of-data check only looks for
the `0xFFFF` size-field sentinel, and a `0x0000` size field instead gets
parsed as a valid-looking but internally-inconsistent record.

Every fixture Phase 1 was tested against so far (synthetic
`internal/odstest` fixtures, and the real OpenVMS install-CD image) never
exercised a directory with this shape — install-CD directories are
typically packed tightly with no slack, and hand-built test fixtures
naturally only ever wrote blocks they meant to be read. A real, actively-
used OpenVMS volume does have this shape, which is exactly the kind of
real-world edge case Phase 1's own real-image validation exists to catch.

Fix: `Directory.List()` should stop at the directory's logical end
(`EndOfFileBlock`), not its physical allocation (`HighestBlock`) — matching
the purpose of the high-water-mark guarantee (blocks at/beyond it are
allocated-but-meaningless, not "more directory data"). Exact shape TBD when
this lands (bound the loop by `EndOfFileBlock` directly, or treat any
block at/beyond the high-water mark as an early, non-error stop) — worth
confirming against this fixture's actual bytes whether a directory's last
used block can ever contain a partial/trailing record before picking the
exact boundary condition.

**Tests:** a regression test reproducing this exact shape (a directory
header with `HighestBlock > EndOfFileBlock`) against a synthetic fixture,
confirming `List()` succeeds and returns exactly the entries within the
used range; existing `directory_test.go`/`realimage_test.go` coverage
continues to pass unchanged; `dir [000000]*.*` against
`testdata/rq0-ra92.dsk` succeeds.

**Shipped, in two passes** — the first (bounding by `HighWaterMark`) turned
out to be necessary but not sufficient, caught by a second real-world case
before landing:

1. First pass: a shared `(*File) isUnwritten(vbn) bool` helper
   (`volume/file.go`) factored the high-water-mark check out of
   `ReadBlock` and into `Directory.List`, stopping at the first unwritten
   block instead of trying to decode it. Fixed `[000000]` on
   `testdata/rq0-ra92.dsk`.
2. But `[VMS$COMMON.SYSLIB]` still failed the same way, on a *different*
   block. Root-caused to `HighWaterMark` and `EndOfFileBlock` being looser
   and tighter bounds respectively, not interchangeable: `SYSLIB.DIR` has
   `HighWaterMark` 25 (blocks below it are safe to read — either real data
   or, with VMS's high-water marking, physically pre-zeroed slack) but
   `EndOfFileBlock`/`FirstFreeByte` 17/0 (real *content* ends at the close
   of block 16) — blocks 17-24 are genuinely zero on disk, not simulated,
   yet still outside the directory's logical content, and bounding by
   `HighWaterMark` alone (too loose) let `List` attempt to decode them
   anyway. Added `(*File) UsedBlocks() uint32` (`volume/file.go`),
   deriving the true content boundary from `EndOfFileBlock`/
   `FirstFreeByte` using the same arithmetic `rms.FileByteLength` already
   used for a file's exact byte length, and made `List` bound its walk by
   `UsedBlocks()`, keeping the `isUnwritten` check as a secondary,
   defensive guard (normally unreachable, given a well-formed header) in
   case a corrupt header's `EndOfFileBlock` ever claims more than its own
   `HighWaterMark` backs up.

That second pass broke essentially every synthetic directory fixture in
the test suite — none of them, across `cmd/ods2`, `filespec`, and
`volume`, had ever needed to set `EndOfFileBlock` before, so it defaulted
to 0 ("no data"), which `UsedBlocks` (correctly, now) takes literally.
Patching each call site individually wasn't practical; instead,
`internal/odstest.BuildFileHeaderBytes` now defaults a *directory*
fixture's `EndOfFileBlock` from `HighestBlock` when left unset (treating
all allocated blocks as real content — what every existing fixture already
implicitly assumed), scoped to `FchDirectory` specifically so it can't
interact with `rms`'s legitimate deliberately-empty-file test (a plain
file fixture with `HighestBlock` > 0 but `EndOfFileBlock` 0 left alone).

Regression test `TestDirectoryListSkipsUnwrittenTrailingBlocks`
(`volume/directory_test.go`) reproduces the `[000000]` shape exactly
(`HighestBlock` 3, `HighWaterMark`/`EndOfFileBlock` 2), with the two
trailing blocks filled with non-zero garbage rather than left zeroed —
catching a fix that only *happens* to work because unwritten blocks
default to zero, as distinct from one that genuinely stops at the right
boundary. Confirmed against the real fixture directly: `dir [000000]*.*`
and `dir [VMS$COMMON.SYSLIB]` both now list cleanly on
`testdata/rq0-ra92.dsk` (13 and 198 entries respectively). That listing is
also a useful preview for subtask 12's reserved-file table: this real,
actively-used volume's file 10 is `SECURITY.SYS` (not an unused
placeholder as guessed below), and it has no file 11 at all — worth
folding into that table's own ground-truth pass when subtask 12 starts,
not changed here to keep this commit scoped to the bugfix.

### 1. `diskimage`: writable containers

**Depends on:** nothing (first subtask).

Add a `WritableContainer` interface (`Container` plus
`WriteBlock(lbn uint32, buf []byte) error`), implemented by `plainImage`
via `f.WriteAt` (symmetric with its existing `ReadBlock`'s `f.ReadAt`).
`rawCDImage` does not implement it. Add `diskimage.Create(path string,
blocks uint32) (WritableContainer, error)` that creates (or truncates) a
host file of exactly `blocks * BlockSize` bytes, zero-filled, and returns
it opened for read-write — this is what `INITIALIZE` (subtask 12) builds a
fresh volume on top of, and what write-path unit tests build synthetic
fixtures on top of instead of hand-rolling an in-memory container per test.

**Tests:** round-trip write/read-back at various LBNs including the first
and last block; `Create` produces exactly the requested size, zero-filled;
attempting to obtain a `WritableContainer` from a raw-CD-opened container
fails cleanly (type assertion, or an explicit `OpenWritable`/error path —
exact shape TBD when this subtask starts).

**Shipped.** Went with the explicit-function shape rather than a type
assertion on a `Container` from the existing `Open`: `OpenWritable`/
`OpenFormatWritable` open the file `O_RDWR` themselves and reject a
detected raw-CD image with a clear error before ever handing back a
container, rather than letting a caller successfully type-assert a
read-only-backed `*rawCDImage`/`*plainImage` and then fail confusingly on
the first `WriteBlock`. The size/raw-CD-sync-pattern detection logic that
`OpenFormat` already had was factored out into a private `detectFormat`
helper shared by both the read-only and read-write open paths, so the two
can't drift on what counts as "looks like a raw CD image." `Create` uses
`os.Truncate` to size the new file, which is zero-filled ("sparse") by the
OS on every platform this project targets — no explicit zero-byte writes
needed.

### 2. `ondisk`: fixed-layout encoders

**Depends on:** 1 (tests want a container to round-trip through, though the
encoders themselves are pure byte manipulation with no I/O).

Add `EncodeHomeBlock`, `EncodeFileHeader`, `EncodeFid`, `EncodeUic`,
`EncodeIdent`, `EncodeRecAttr` — each the exact inverse of its existing
`Decode*`, reusing the same private offset constants, and computing/writing
the checksum via the existing `Checksum()` function (already suitable for
this unchanged — it's a pure function of the first 510 bytes). Also add the
reserved-file `Fid` constants Phase 1 never needed:
`BitmapFileFid = {Num: 2, Seq: 2}`, `BadBlockFileFid = {Num: 3, Seq: 3}`,
and (pending confirmation — see subtask 12) constants for file numbers
5-11.

`FileHeader`'s variable-position IDENT/map/ACL areas are the main
complexity here: encoding a header means *choosing* non-overlapping
`IdentOffset`/`MapOffset`/`AclOffset`/`EndOffset` values for whatever
content is being written (not just replaying values that were already on
disk, the way `raw` does for decode), typically by laying the areas out
contiguously in a fixed, conventional order (IDENT first, as the reference
implementation's `update_addhead()` does at `fh2$b_idoffset=40`).

**Tests:** `Decode(Encode(x)) == x` round-trips for representative values
of each type, including edge cases (a header with a zero-length IDENT area,
a `Fid` with a non-zero `Nmx`, etc.); checksum of an encoded block validates
via the existing decoder.

**Shipped.** `EncodeFid`/`EncodeUic`/`EncodeRecAttr` are direct,
no-error inverses of their `Decode*` counterparts (RecAttr's
`HighestBlock`/`EndOfFileBlock` round-trip through a new
`encodeSwappedLongword`, the inverse of the existing
`decodeSwappedLongword`). `EncodeHomeBlock` and `EncodeIdent` can fail —
their space/NUL-padded text fields (`VolumeName`, `Ident.Filename`, etc.)
have a fixed on-disk width a caller's string might not fit — so they
return `(..., error)`, via new `encodePaddedString`/`EncodeIdent` bounds
checks rather than silently truncating.

`FileHeader` needed a different shape entirely, for the reason the
subtask description called out: `IdentOffset`/`MapOffset`/`AclOffset`/
`EndOffset`/`MapWordsInUse` aren't independent inputs the way the rest of
a header's fields are — they're wholly *determined* by what variable
content is being written and in what order, so treating them as settable
`FileHeader` fields on the encode side would let a caller construct a
self-contradictory header. `EncodeFileHeader(h FileHeader, areas
FileHeaderAreas) ([]byte, error)` splits the two apart: `h` carries every
field that really is just replayed onto disk (`Fid`, `RecordAttributes`,
`HighWaterMark`, ...), while `FileHeaderAreas{Ident *Ident, MapBytes,
AclBytes []byte}` carries the variable content, and the function itself
computes and fills in all five derived fields — laying IDENT, then map,
then ACL out contiguously starting right after the fixed portion of the
header (word offset 54, byte 108, matching the existing
`fhOff*`-constants' own layout), erroring if the combined content
overflows the 402 bytes available before the checksum field. A nil
`Ident` produces a genuine zero-length IDENT area (`IdentOffset ==
MapOffset`) rather than requiring one, covering the "zero-length IDENT
area" edge case the subtask's test plan called out. `MapBytes` exists
today only as an opaque pre-encoded `[]byte` a caller supplies directly
(subtask 3 hasn't landed the extent-list encoder yet); `AclBytes` is
always `nil` in practice, since this project never constructs ACLs, but
the field exists so the layout math treats all three areas uniformly.

Because `EncodeFileHeader` *computes* its own `IdentOffset`/`MapOffset`/
etc. rather than replaying whatever an input `FileHeader` happened to
carry there, a literal `Decode(Encode(x)) == x` isn't quite the right
round-trip test for this one type (unlike Fid/Uic/RecAttr, where it is,
and HomeBlock, where encoding a `HomeBlock` decoded straight from a
hand-built fixture reproduces the original bytes exactly). Its test
instead builds a `FileHeader` + `FileHeaderAreas`, encodes, decodes, and
checks: every field that came from `h` matches directly; the IDENT and
map-area content match via the *existing*, already-validated decode-side
`(*FileHeader).Ident()`/`RetrievalPointers()` methods rather than a
hand-rolled byte comparison — a stronger check, and a preview of the
"dogfood Phase 1's read path" strategy this document's own [Testing
strategy](#testing-strategy) section calls out for later subtasks.

`internal/odstest`'s hand-rolled duplicates were retired where directly
replaceable without disturbing any consuming test's behavior: `putFidAt`
now delegates to `ondisk.EncodeFid`, `putSwappedLongword` is gone entirely
(`BuildFileHeaderBytes` now builds an `ondisk.RecAttr` and calls
`ondisk.EncodeRecAttr`), and `BuildHomeBlockBytes` is now a thin wrapper
around `ondisk.EncodeHomeBlock`. `BuildFileHeaderBytes` itself keeps its
own hand-placed offset logic rather than switching to `EncodeFileHeader`
— its `FileHeaderFixture.IdentOffset` is deliberately settable to
arbitrary values, including 0 to disable `volume.File.ReadBlock`'s
high-water-mark check, a fixture-only affordance `EncodeFileHeader`'s
always-computed layout has no way to express and Phase 1's own read-path
tests across several packages already depend on; forcing convergence here
would have meant changing test behavior well outside this subtask's
scope, not just retiring a duplicate.

### 3. `ondisk`: retrieval-pointer encoder

**Depends on:** 2 (shares the file header's map-area machinery).

Add an encoder that takes `[]Extent` and produces the packed on-disk map
bytes, picking the smallest of the four formats (see `retrieval.go`'s
existing decoder for the exact per-format bit layout — this is real
on-disk format, worth reproducing precisely, not a reference-implementation
artifact) that fits each extent's count and LBN, and reporting how many map
words the result consumes (needed by subtask 8 to decide whether a header's
map area has run out of room).

**Tests:** `RetrievalPointers(Encode(extents)) == extents` round-trips
across boundary values for each format (max count/LBN that still fits
format 1, one more than that forcing format 2, and so on).

### 4. `ondisk`: storage-bitmap bit-packing + SCB encoder

**Depends on:** 2 (reuses `Checksum`).

Add `EncodeStorageControlBlock` (inverse of the existing, currently-unused
`DecodeStorageControlBlock`). Add bit-level helpers for the free-space
bitmap's actual bits — a fixed, explicit byte-oriented packing (see
[What we're deliberately not porting](#what-were-deliberately-not-porting-from-the-c-reference)
for why this isn't just the reference implementation's word-packing
scheme): something like `BitmapTest(bits []byte, cluster uint32) bool`,
`BitmapSet`/`BitmapClear`, operating on whatever buffer holds the decoded
bitmap blocks. One bit per cluster; bit set = free, matching the
reference's convention (`update_freecount()` counts set bits as free
space) — this is the one piece of the bitmap design taken from the
reference, since "which polarity means free" is a real on-disk convention,
not an implementation artifact.

**Tests:** set/clear/test round-trip across byte boundaries; SCB
encode/decode round-trip.

### 5. `ondisk`: directory-block encoder

**Depends on:** 2 (reuses `Fid` encoding).

Add `EncodeDirectoryBlock(entries []DirEntry) ([]byte, error)`, the inverse
of `DecodeDirectoryBlock`: groups entries by name, sorts each name's
versions descending (matching on-disk convention and this project's own
`Directory.Lookup` "version 0 means highest" logic), packs them into a
512-byte block with the `0xFFFF` end-of-data sentinel, and errors if the
entries don't fit in one block. Deliberately a whole-block "encode this set
of entries" function, not an in-place byte-splicing API like the reference
implementation's `insert_ent()` — the reference's block-splitting logic
being intertwined with in-place mutation is exactly what makes its
directory-extension case (see table above) hard to get right; keeping
encode pure and stateless pushes "which entries go in which block, and
what to do when they don't fit" to `volume` (subtask 9), where it belongs
alongside the rest of the allocation policy.

**Tests:** `DecodeDirectoryBlock(EncodeDirectoryBlock(entries))` round-trips
for various entry counts/name lengths; encoding entries that don't fit
returns an error rather than silently truncating.

### 6. `volume`: storage-bitmap cache & allocator (BITMAP.SYS)

**Depends on:** 1, 4.

A new type (name TBD when this subtask starts — something like
`volume.Bitmap`) that, given a `Device` open for write, reads
`BITMAP.SYS`'s SCB and bitmap blocks into memory once, and exposes:

- `FindFree(clusters uint32) (Extent, error)` — first-fit scan (see
  [table above](#what-were-deliberately-not-porting-from-the-c-reference)
  for why not the reference's hint/best-run approach), returning one
  contiguous extent of the requested length or an error if the volume has
  no run that long. (A caller needing more space than the largest free run
  offers is future work — Phase 2's file-extend path, subtask 8, is free to
  loop calling `FindFree` with a smaller request and accept fragmentation,
  rather than requiring this method to solve multi-extent allocation
  itself.)
- `MarkAllocated(e Extent)` / `MarkFree(e Extent)` — mutate the in-memory
  bitmap and mark it dirty.
- `Flush() error` — write every dirty bitmap block back via
  `WritableContainer.WriteBlock`, update the SCB's free-cluster-derived
  state if it tracks one, clear the dirty flag.

**Tests:** allocate/free round-trips against a synthetic
`diskimage.Create`d volume; `FindFree` picks the expected extent for a
hand-constructed bitmap with known free runs; `Flush` followed by
re-reading the bitmap from the container reflects the mutations; an
unflushed `Bitmap` doesn't affect the on-disk bytes at all (confirms the
cache is genuinely deferred, not accidentally write-through).

### 7. `volume`: index-file header-slot cache & allocator (INDEXF.SYS)

**Depends on:** 1, 4 (shares the same bit-packing helpers as subtask 6,
applied to the index-file bitmap instead of `BITMAP.SYS` — see
[Two bitmaps, not one](#two-bitmaps-not-one)).

The header-slot analog of subtask 6: reads the index-file bitmap region
(`HomeBlock.IndexBitmapVBN`/`Size`, within `INDEXF.SYS`) into memory,
exposes `FindFreeSlot() (fileNumber uint32, error)` (skipping the first
`HomeBlock.ReservedFiles` slots — always via that field, never a hardcoded
constant, per the table above) and `MarkAllocated`/`MarkFree`, with the
same deferred-flush `Flush()` shape as subtask 6. Matching the reference
implementation's (correct, worth keeping) extra safety check in
`update_findhead()`: before trusting a clear bit, read that header slot's
actual block and confirm it really looks unused (zero checksum, zero file
number) before handing it out — cheap, and catches a bitmap/reality
mismatch before it causes silent corruption, exactly the kind of
inconsistency `ANALYZE/DISK` (subtask 15) is also designed to catch at a
whole-volume level.

**Tests:** analogous to subtask 6's, against the index-file bitmap region
instead.

### 8. `volume`: file-header writer

**Depends on:** 2, 3, 6, 7.

New file, `volume/writeheader.go` (or similar): given a directory Fid, a
name, and initial `RecAttr`, allocates a header slot (subtask 7), builds a
`FileHeader` (owner/protection defaulted from the volume's own home block
fields — `HomeBlock.VolumeOwner`/`FileProtection` — rather than a
hardcoded UIC the way the reference implementation's `update_addhead()`
does), encodes it (subtask 2), and writes it to disk immediately (no
deferred flush for headers — see
[Caching strategy](#caching-strategy)). A companion `Extend(f *File,
additionalBlocks uint32) error`: allocates space via subtask 6, encodes the
new extent into the primary header's map area if there's room (checking
against `MapOffset`/`AclOffset` — the reference implementation's own
"is the map area nearly full" check, `update.c:412-413`, is a reasonable
model for the comparison itself, just not for the header-location formula
elsewhere in the same file), or allocates and links a new extension-header
segment (subtask 7 again, for the new segment's own slot) when it doesn't,
and updates `HighWaterMark` correctly on the primary header afterward —
this field is exactly what Phase 1's own `File.ReadBlock` relies on to
avoid exposing stale data from a block's previous occupant, so getting it
right here is what keeps Phase 1's read path correct against Phase 2's
writes.

**Tests:** create a header, confirm it round-trips through Phase 1's own
`ondisk.DecodeFileHeader`/`volume.OpenFID` unchanged (dogfooding the
existing read path as the correctness check); extend a file enough to
require a second header segment, confirm `RetrievalPointers()` chases the
chain correctly; confirm a freshly-extended-but-unwritten block reads back
as zero (via `HighWaterMark`), per Phase 1's existing guarantee.

### 9. `volume`: directory mutation

**Depends on:** 5, 8.

`(*Directory) Insert(name string, version uint16, fid ondisk.Fid) error`
and a `NextVersion(name string) (uint16, error)` helper (returns
`Lookup(name, 0).Version + 1`, or `1` if the name doesn't exist yet — the
"highest existing version + 1" rule the reference implementation's
`search_ent()` also implements, `direct.c:630-636`). Insert re-reads all of
the directory's current entries (`List()`, already exists), adds the new
one, and re-encodes affected block(s) via subtask 5's
`EncodeDirectoryBlock` — when the result doesn't fit in the existing
allocation, extends the directory file via subtask 8's `Extend` (an
ordinary, handled case here, unlike the reference implementation's
`exit(0)` for the same situation — see the table above) and writes the
extra block(s).

**Tests:** insert into an empty directory; insert a second version of an
existing name and confirm `NextVersion` predicted it correctly; insert
enough entries to force a directory-block split/extension and confirm
`List()` afterward sees every entry, correctly grouped and versioned.

### 10. `volume`: file write API

**Depends on:** 6, 8, 9.

`(*File) WriteBlock(vbn uint32, data []byte) error` — extends the file
(subtask 8) if `vbn` is beyond its current allocation, then writes through
the resolved extent immediately (no write-back caching for file data
either, matching the "only the two bitmaps are cached" design). `(*Volume)
CreateFile(dir *Directory, name string, recAttr ondisk.RecAttr) (*File,
error)` — resolves the next version number (subtask 9), allocates a header
(subtask 8), inserts the directory entry (subtask 9), and returns an open,
writable `File`. `(*File) Close() error` — writes back any header fields
that changed since it was opened/created (`RecordAttributes.EndOfFileBlock`
etc.) — needed because, unlike VMS RMS, nothing here holds a header
"pinned" in a cache that a close could just mark dirty; Phase 2's design
writes the header eagerly at points that change it, so `Close` is mostly
a well-defined place to finalize EOF-related bookkeeping once writing is
done, plus a place to fail loudly if a caller forgets to call it (a `File`
opened for write that's never `Close`d should be caught by a `go vet`-style
discipline or a test helper, not silently accepted).

**Tests:** create a file, write several blocks (including out of order,
to exercise the auto-extend path), close it, and confirm Phase 1's own
`File.ReadBlock` reads every byte back correctly, including a not-yet
directly-written block within the file's extended range reading back as
zero pre-write; open an *existing* file (created by a previous test step)
for write and confirm overwriting a block in place works.

### 11. `volume`: Dismount flush

**Depends on:** 6, 7.

Add `(*Volume) Dismount() error` that flushes every device's bitmap caches
(subtasks 6 and 7, if this mount session created any) before closing
containers, and switch `cmdDismount` (subtask 14) to call it instead of
looping `dev.Container.Close()` directly. A volume that was only ever
mounted read-only (no `Bitmap`/header-slot-cache instances created) is a
no-op flush, so this is safe to call unconditionally regardless of how the
volume was mounted.

**Tests:** mount, allocate (without an intervening flush, to confirm this
is genuinely exercising the deferred cache rather than something already
flushed elsewhere), `Dismount()`, re-open the same container fresh, and
confirm the allocation is visible.

### 12. `volume` + `cmd`: `INITIALIZE`

**Depends on:** 1, 2, 4, 5, 6, 7.

The most novel subtask — nothing in Phase 1 or the reference implementation
does this (see the C-reference report: no `INITIALIZE`/`mkfs`-equivalent
exists there at all; it assumes a volume was already formatted by real
VMS). Given a `WritableContainer` (from `diskimage.Create`, subtask 1) and
a small set of parameters (volume label, owner UIC, cluster size — with
defaults), builds a minimal but valid volume:

1. Compute layout: home block at LBN 1, `MaxFiles`/`ReservedFiles`/
   `IndexBitmapSize` sized for a small volume (exact numbers TBD — start
   from whatever the reference-implementation-informed field relationships
   require to stay internally consistent, refine against real-volume
   values if `ODS2_TEST_IMAGE` is available during implementation).
2. Write `INDEXF.SYS`'s own header directly at the fixed, computable LBN
   (mirroring `bootstrapIndexFile`'s read-side logic exactly, in reverse)
   — this is the brute-force step that avoids the chicken-and-egg problem
   the user's prompt called out.
3. Allocate and write header slots + minimal data for the rest of the
   reserved file set. The C reference gives no guidance here (files 3,
   5-11 are never referenced by name anywhere in it); the working table
   below is derived from published ODS-2 documentation and needs
   confirming against a real volume before this subtask is considered
   done. Two real fixtures help here in different ways: the existing
   `cmd/ods2/realimage_test.go` fixture already asserts `INDEXF.SYS`,
   `BITMAP.SYS`, `000000.DIR`, and `BADBLK.SYS`'s names/Fids against a real
   OpenVMS V5.5-2H4 *install CD*; `testdata/rq0-ra92.dsk` (see
   [Testing strategy](#testing-strategy)) is a real, actively-*used*
   OpenVMS volume rather than an install image, so it's the better source
   for what files 5-11 actually contain in practice (an install CD may
   never have touched them) — inspect its master file directory and each
   reserved file's header directly (once subtask 0 makes `DIRECTORY`
   against its root reliable) to fill in this table for real before
   treating it as final:

   | # | Name | Role |
   |---|---|---|
   | 1 | INDEXF.SYS | index file (this volume's own file-header table) |
   | 2 | BITMAP.SYS | storage bitmap |
   | 3 | BADBLK.SYS | bad-block list (empty on a fresh volume) |
   | 4 | 000000.DIR | master file directory (root) |
   | 5 | CORIMG.SYS | core image file (historical; empty) |
   | 6 | VOLSET.SYS | volume-set list (empty for a single-disk volume) |
   | 7 | CONTIN.SYS | continuation file (historical; empty) |
   | 8 | BACKUP.SYS | backup journal (empty) |
   | 9 | BADLOG.SYS | bad-block log (empty) |
   | 10 | (reserved) | unused placeholder header |
   | 11 | (reserved) | unused placeholder header |

4. Build the root directory (`000000.DIR`)'s single data block, listing
   every reserved file above by name via subtask 5's encoder (matching
   what a real volume's MFD actually contains, per the existing real-image
   test).
5. Mark the reserved regions (home block, `INDEXF.SYS` itself, its header
   bitmap, `BITMAP.SYS` itself) allocated in the storage bitmap (subtask 6)
   before flushing it, and every reserved header slot allocated in the
   index-file bitmap (subtask 7).
6. Write the home block (and, at minimum, its checksum) last, once
   everything it points at is valid.

Exposed as `volume.Initialize(c diskimage.WritableContainer, opts
InitializeOptions) error` in `volume`, and as a new `INITIALIZE` REPL/
one-shot command in `cmd/ods2/internal/session/initialize.go` wrapping it
(parsing size/label/cluster-size arguments, creating the host file via
`diskimage.Create`).

**Tests:** the primary acceptance test is dogfooding — `volume.Initialize`
a fresh container, then `volume.Mount` it and confirm every Phase-1 read
operation works against it unmodified: the reserved files' headers decode,
`000000.DIR` lists them with the right Fids (same shape of assertion as
`realimage_test.go`'s real-image check, but against a synthetic freshly-
initialized volume, runnable in CI unlike the real-image test), and a file
subsequently created on it (subtask 10) and dismounted (subtask 11) can be
re-mounted and read back correctly.

### 13. `rms`: record writer

**Depends on:** 10.

A `rms.Writer` symmetric to the existing `Reader`: `Put(record []byte)
error` per format (Fixed/Variable/VFC/Stream*, dispatching on
`RecAttr.Format` the same way `Reader.Next()` does today), and `Close()
error` finalizing `EndOfFileBlock`/`FirstFreeByte` on the underlying
`File`'s header. This is what a future record-aware "copy a host file onto
the volume" path (subtask 16) would use instead of writing raw fixed-size
blocks directly via `File.WriteBlock`.

**Tests:** write records in each format, read them back via the existing
`rms.Reader`, confirm round-trip fidelity — including VFC's carriage-
control byte handling, matching the level of care Phase 1's `TYPE`/`COPY`
already put into reading it correctly.

### 14. `cmd/ods2`: `MOUNT /WRITE` + `DISMOUNT` wiring

**Depends on:** 1, 11.

`mount.go`'s `/write` qualifier currently is accepted but does nothing
(`"this project is read-only... there is no write mode to enable"`). Wire
it for real: obtain a `WritableContainer` (subtask 1) instead of a
read-only one when `/WRITE` is given, failing clearly for a raw-CD image;
store on the session whichever mutating commands need to check "is this
volume writable" before attempting subtasks 9/10's operations. Switch
`cmdDismount` to call `Volume.Dismount()` (subtask 11) instead of looping
`Close()` directly.

**Tests:** `MOUNT ... /WRITE` against a plain image succeeds and a
subsequent write-path operation works; against a raw-CD image, fails with
a clear message; `MOUNT` without `/WRITE` still works exactly as it does
today (read-only, unchanged behavior) — a regression test against Phase
1's existing `mount_test.go` coverage.

### 15. `cmd/ods2`: `ANALYZE/DISK`

**Depends on:** 6, 14.

New command, `analyze.go`. The read/diagnose half needs almost nothing
from this phase beyond subtask 4's bitmap-bit-decode helpers: walk file
numbers `1..MaxFiles`, decode every in-use header (checksum-valid, non-
zero file number) via Phase 1's existing `ondisk.DecodeFileHeader`, collect
every extent it (and its directory's own extents, and any extension
segments) claims, and build the bitmap those allocations *should* produce.
Compare bit-for-bit against `BITMAP.SYS`'s actual on-disk bits (decoded,
also via subtask 4) and report every discrepancy: a block the real bitmap
marks free but some file actually uses (corruption risk — a future
allocation could silently overwrite live data), and a block marked
allocated that no file's extents actually claim (reclaimable, but not
dangerous). Given `/REPAIR`, rewrite `BITMAP.SYS`'s bit data (via subtask
6's `Bitmap` — construct one from the computed-correct state rather than
the on-disk one, mark it entirely dirty, `Flush()`) to match the computed
truth, requiring the volume be mounted `/WRITE` (subtask 14).

**Tests:** against a known-good `INITIALIZE`d + written-to volume (subtask
12/10), reports zero discrepancies; against a volume whose `BITMAP.SYS` was
hand-corrupted in a test fixture (a bit flipped to "free" under a block a
real file uses, and separately a bit left "allocated" under space nothing
uses), reports exactly those two discrepancies; with `/REPAIR`, produces a
`BITMAP.SYS` that a follow-up `ANALYZE/DISK` (no `/REPAIR`) reports clean.

### 16. `cmd/ods2`: `COPY` host → volume direction (stretch goal)

**Depends on:** 13, 14, 15 optional.

Not required to consider Phase 2 "done" against the goals this document
opened with, but the most natural way to exercise the entire write stack
end to end through a real user-facing command, and a good place to
retire "write support" from being purely a library-level capability with
no CLI surface. Extends `copy.go`'s existing destination-resolution logic
to recognize a VMS-syntax destination (device mounted `/WRITE`) and drive
`volume.CreateFile` + `rms.Writer` (subtasks 10/13) instead of writing to
the host filesystem, reusing the qualifier set already designed for the
existing (volume → host) direction where it still makes sense
(`/VERBOSE`, `/TEST`, `/BINARY`; `/TIME`, `/IGNORE`, `/DIRS`, the line-
ending qualifiers are host-file-format concerns that don't obviously carry
over and would need their own design pass if pursued).

**Tests:** copy a host file onto a writable volume, confirm it reads back
identically via `TYPE`/`COPY` off the volume; copying to a name that
already exists on the volume creates the next version rather than
overwriting, matching this document's version-numbering goal directly.

---

## Testing strategy

Every subtask above needs its own unit tests per this repo's standing
convention (tests land in the same commit as the functional code they
cover), but two points apply across the whole phase and are worth stating
once:

- **The install-CD real-image fixture (`ODS2_TEST_IMAGE`) cannot be used to
  test writes.** It's read-only by design (a real OpenVMS install CD dump,
  not checked into the repo, used only for optional local validation via
  an environment variable), and even where it's a plain (non-raw-CD)
  format in principle writable, it's the wrong tool for regression
  testing: any accidental corruption would need re-fetching an external
  asset outside this repo's control, and intentional writes to it would
  make the fixture no longer represent "known-good real VMS output" for
  future read-side regression checks. Write-path tests build their own
  synthetic volumes instead, via `diskimage.Create` + `volume.Initialize`
  (subtask 12) — once that subtask lands, it becomes the standard
  fixture-construction path for every later write-path test in this
  phase, superseding one-off manual byte construction the way
  `internal/odstest` did for Phase 1's decode tests.
- **A second real fixture, `testdata/rq0-ra92.dsk`, is available locally
  and *can* be used for write testing.** It's a real, writable OpenVMS
  volume (label `OPENVMS071`), placed directly under `testdata/` — `*.dsk`
  is already `.gitignore`d, so it's a stable local path that simply won't
  exist in CI or on a fresh checkout, rather than something reached
  through an environment variable the way `ODS2_TEST_IMAGE` is. Tests that
  use it should check for its presence (`os.Stat`, skipping if absent,
  the same pattern `realimage_test.go` already uses) rather than assume
  it's there. Unlike `ODS2_TEST_IMAGE`, writing to it is fine *in
  principle* — but no test (or manual exploration) ever mutates the
  checked-in copy directly; make a working copy first (a plain host-level
  file copy to a scratch path is enough) and operate on that. It's also
  useful beyond write testing: being a real, actively-used volume rather
  than an install CD, it's the better source of ground truth for subtask
  12's reserved-file-layout table (files 5-11 specifically, which the
  install-CD fixture's own assertions don't cover) — see subtask 12. It
  also already surfaced one genuine Phase 1 bug (subtask 0), which is
  exactly the kind of value a second, differently-shaped real volume is
  expected to keep providing through the rest of this phase.
- **Dogfooding Phase 1's read path is the strongest correctness signal
  available.** Nearly every subtask's test plan above includes "write
  something, then confirm Phase 1's existing, already-validated read code
  reads it back correctly" rather than only asserting against hand-computed
  expected bytes. Both matter (a round-trip test can hide a decoder and
  encoder sharing the same wrong assumption), but the read-path check in
  particular is only possible because Phase 1 was validated against a real
  VMS volume — leaning on it here is a genuine cross-check, not a
  tautology.

## Related reading

- [PHASE-01.md](PHASE-01.md) — Phase 1's plan/retrospective; current
  package architecture and design rationale.
- [COMMANDS.md](COMMANDS.md) — full CLI command reference; `INITIALIZE` and
  `ANALYZE/DISK` get their own entries here once implemented.
- `testdata/rq0-ra92.dsk` — a real, writable OpenVMS volume available
  locally for this phase's development and testing (gitignored, not
  checked into the repo); see [Testing strategy](#testing-strategy) for
  how it's used and the ground rule (copies only, never mutate the
  original).
- `/Users/tom/Projects/ods2` — the C reference implementation consulted for
  this plan (Paul Nankervis's ODS2, via Hunter Goatley/crwolff/Dave
  Shepperd forks) — a from-scratch design informed by it, not a port of it;
  see
  [What we're deliberately not porting](#what-were-deliberately-not-porting-from-the-c-reference)
  above for specifics.
