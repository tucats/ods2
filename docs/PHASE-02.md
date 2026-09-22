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
| --- | --- | --- |
| 0 | Phase 1 bugfix: `Directory.List()` on partially-allocated directories | Done |
| 1 | `diskimage`: writable containers | Done |
| 2 | `ondisk`: fixed-layout encoders (HomeBlock, FileHeader, Fid, Uic, Ident, RecAttr) | Done |
| 3 | `ondisk`: retrieval-pointer encoder | Done |
| 4 | `ondisk`: storage-bitmap bit-packing + SCB encoder | Done |
| 5 | `ondisk`: directory-block encoder | Done |
| 6 | `volume`: storage-bitmap cache & allocator (BITMAP.SYS) | Done |
| 7 | `volume`: index-file header-slot cache & allocator (INDEXF.SYS) | Done |
| 8 | `volume`: file-header writer (new headers, extension segments, HighWaterMark) | Done |
| 9 | `volume`: directory mutation (insert + auto-extend + version assignment) | Done |
| 10 | `volume`: file write API (open-for-write, CreateFile, WriteBlock) | Done |
| 11 | `volume`: Dismount flush | Done |
| 12 | `volume`+`cmd`: `INITIALIZE` | Done |
| 13 | `rms`: record writer | Done |
| 14 | `cmd/ods2`: `MOUNT /WRITE` + `DISMOUNT` wiring | Done |
| 15 | `cmd/ods2`: `ANALYZE/DISK` | Done |
| 16 | `cmd/ods2`: `COPY` host → volume direction (stretch goal) | Done |

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
| --- | --- | --- |
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
5-11 — turned out, once subtask 12 actually checked, to be 5-9: a
volume's reserved set is nine files, not eleven (see subtask 12's own
write-up).

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

**Shipped.** `EncodeRetrievalPointers(extents []Extent) ([]byte, error)`
encodes a whole extent list, delegating each extent to a private
`encodeExtent`, which picks the narrowest of formats 1-3 that fits (format
1 needs both `Count <= 256` and `StartLBN <= 0x3FFFFF`; format 2 only
needs `Count <= 16384`, since its LBN field is already a full 32 bits;
format 3 covers everything else up to `Count <= 0x40000000`, the largest
count the on-disk 30-bit field can represent). A `Count` of 0 or above that
ceiling is rejected outright rather than silently wrapping or producing a
corrupt entry — no caller has a legitimate reason to construct either.
Format 0 (the placeholder/filler word `RetrievalPointers` skips on decode)
is never an encode target, since no `Extent` value means "no extent."

The result plugs directly into `FileHeaderAreas.MapBytes` from subtask 2 —
`EncodeFileHeader` already derives `MapWordsInUse` from its length, so no
separate word-count return value was needed. Tests cover the boundary
values the subtask's test plan called for (narrowest-format selection
verified directly via encoded length, not just via a round trip, since a
round trip alone can't distinguish "correct bytes" from "correct bytes in
a wider-than-necessary format"), plus a full round trip through
`EncodeFileHeader`/`DecodeFileHeader`/`RetrievalPointers` across a mixed
extent list spanning every format boundary in one map area.

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

**Shipped.** `EncodeStorageControlBlock` is a direct inverse of
`DecodeStorageControlBlock`, following the same pattern as
`EncodeHomeBlock`/`EncodeFileHeader`: every field is replayed onto disk
except `Checksum`, which is always computed fresh, and `VolumeLockName`
(the SCB's one fixed-width text field) goes through the existing
`encodePaddedString`, so an oversized name is rejected rather than
truncated.

The bit-packing helpers landed in a new file, `ondisk/bitmap.go`, rather
than in `scb.go` — they're conceptually part of `BITMAP.SYS` but operate
on the bitmap *bits* (the blocks that follow the SCB), not the SCB
structure itself, and neither had a natural home in an existing file.
`BitmapTest`/`BitmapSet`/`BitmapClear` take the caller-owned, already
correctly-sized decoded-bitmap buffer directly (`bits []byte`) plus a
0-based cluster number, packing one bit per cluster, LSB-first within each
byte (`bits[cluster/8]`, bit `cluster%8`) — free/allocated polarity (1 =
free) is kept from the reference implementation as a genuine on-disk
convention, but the word-sized, host-endianness-dependent packing scheme
around it is not (see [What we're deliberately not
porting](#what-were-deliberately-not-porting-from-the-c-reference)). None
of the three functions bounds-check `cluster` against `len(bits)`
themselves — an out-of-range call panics via an ordinary slice-index
panic, treated as a caller bug (subtask 6 owns sizing the buffer to cover
every valid cluster) rather than something worth an `error` return for
every call site to check. This byte-oriented layout is still unconfirmed
against a real volume's actual `BITMAP.SYS` bytes, per the caveat already
called out in the table above — worth revisiting if `testdata/rq0-ra92.dsk`
(or another writable real volume) turns out to have a live bitmap file
worth decoding for comparison when subtask 6 lands.

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

**Shipped.** `EncodeDirectoryBlock` groups the input `[]DirEntry` by Name
into a `map[string][]DirEntry`, orders the distinct names ascending
(`sort.Strings`) and each name's versions descending
(`sort.Slice`), then lays out one name record per group exactly as
`DecodeDirectoryBlock` expects to find it: 6-byte header (size, then a
version-limit word and a flags byte this project's own decoder never
reads, left zero), name text padded to even length, then one 8-byte
version entry per version. Ordering names ascending isn't required for
correctness — `DecodeDirectoryBlock` has no ordering expectation of its
own — but matches the convention `volume.Directory.List`'s doc comment
already describes real on-disk directories as following, so a block this
function produces is indistinguishable from one a real VMS system would
have written for the same entries. A name longer than 255 bytes (the
on-disk name-length field is a single byte) or an entry set that doesn't
fit in one 512-byte block is rejected with an error rather than silently
truncated; when the encoded records fill the block exactly, the trailing
`0xFFFF` sentinel is skipped rather than written past the buffer's end,
relying on (and exercised by a test against) `DecodeDirectoryBlock`'s scan
loop already stopping on its own once there's no room left for another
record header.

`internal/odstest`'s `BuildDirRecordBytes`/`BuildDirBlock` were
deliberately **not** retired in favor of this — unlike subtask 2's
`putFidAt`/`BuildHomeBlockBytes`, which really were pure duplicates, these
two let a test hand-place records in an arbitrary, unsorted order and
build deliberately malformed blocks (oversized declared sizes, truncated
entry areas, and so on — see `directory_test.go`'s own corruption tests),
which is exactly the decode-side robustness testing `EncodeDirectoryBlock`
itself doesn't need or want to support. They're consumed across
`cmd/ods2`, `filespec`, and `volume`'s own test suites, several of which
assert a specific on-disk block layout or entry order as part of what
they're testing; converging those onto `EncodeDirectoryBlock`'s
always-sorted output would silently change what those tests exercise, well
outside this subtask's scope — the same call subtask 2's write-up made
about `BuildFileHeaderBytes`.

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

**Shipped.** `OpenBitmap(dev *Device) (*Bitmap, error)` opens `BITMAP.SYS`
(`ondisk.BitmapFileFid`, reserved file 2) the same way any other file is
opened internally — `readFileHeaderViaIndex` + `buildFile` against
`dev.IndexFile.Extents` — rather than going through `Volume.OpenFID`,
since that resolves its target device from `Fid.Rvn`, and this type is
explicitly handed the device it should operate on already. It fails
immediately, before reading anything, if `dev.Container` doesn't type-
assert to `diskimage.WritableContainer`, so a read-only mount fails with a
clear error rather than succeeding and only failing later on `Flush`. It
also cross-checks the decoded `StorageControlBlock.ClusterSize` against
`HomeBlock.ClusterSize` and rejects a mismatch outright — the two are
supposed to always agree (`StorageControlBlock`'s own doc comment already
noted this), and catching a disagreement here is cheap insurance against
ever computing a cluster's LBN range with the wrong stride.

`FindFree`/`MarkAllocated`/`MarkFree` all work in terms of `ondisk.Extent`
(blocks, matching every other package's vocabulary) and convert to/from
cluster numbers internally via a shared `clusterRange` helper, which
doubles as the alignment/bounds check: `MarkAllocated`/`MarkFree` reject
an extent that isn't a whole, cluster-aligned run within the volume's
actual cluster count, on the theory that such an extent could never have
come from this bitmap's own `FindFree` and almost certainly indicates a
caller bug worth surfacing immediately rather than silently corrupting a
neighboring cluster's bit.

`Flush` writes each dirty bitmap block directly via
`WritableContainer.WriteBlock` at the LBN a new, small `resolveExtentLBN`
helper (factored out of `file.go`'s existing `readExtents`, which now
calls it) resolves from the bitmap file's own extents — deliberately not
going through a not-yet-existent `File.WriteBlock` (that's subtask 10,
which depends on this one, not the other way around). Dirty tracking is a
single whole-bitmap flag rather than per-block, matching this phase's
stated preference for the simplest design that satisfies the actual
requirement (see [Caching strategy](#caching-strategy)) — a volume's
bitmap is small enough that rewriting all of it on a dirty `Flush` is
cheap, and per-block tracking would be complexity with no measured benefit
here. `Flush` never rewrites the `StorageControlBlock` itself (VBN 1):
this package's decoded `ondisk.StorageControlBlock` has no modeled
running free-cluster count for a mutation to update (the reference
implementation computes one on demand rather than persisting it), so
there is nothing in that block a bitmap mutation would ever need to
change.

Tests build their fixture as the subtask's own test plan asked —
`diskimage.Create`, not `odstest.MemContainer` — since a `WritableContainer`
is a hard requirement here and `MemContainer` (Phase 1's read-only in-
memory fixture) doesn't implement it. `internal/odstest`'s existing
byte-level builders (`BuildHomeBlockBytes`, `BuildFileHeaderBytes`,
`EncodeExtentFormat2`) still do the encoding; the difference from Phase
1's fixtures is only which `diskimage.Container` the resulting bytes get
written into. Coverage: first-fit run selection (deliberately using two
differently-sized free runs to distinguish first-fit from best-fit);
no-match and zero-cluster error paths; `MarkAllocated`/`MarkFree` each
observably changing what a later `FindFree` returns; misaligned and
out-of-range extents rejected; the read-only-device and cluster-size-
mismatch rejections at `OpenBitmap` time; and the deferred-flush behavior
the subtask's test plan specifically called out, via two independently-
opened `Bitmap`s over the same device confirming an unflushed mutation is
invisible until `Flush` is called.

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

**Shipped.** `OpenIndexBitmap(dev *Device) (*IndexBitmap, error)` reads
`HomeBlock.IndexBitmapVBN`/`IndexBitmapSize` blocks straight out of
`dev.IndexFile` (already resolved by `Mount`/`bootstrapIndexFile` — unlike
subtask 6's `Bitmap`, there's no separate file to open here, since this
bitmap lives *inside* `INDEXF.SYS` itself), rejecting a read-only device
and a zero/oversized `HomeBlock.MaxFiles` up front the same way `OpenBitmap`
does for a mismatched cluster size.

The one real surprise: **this bitmap's free/allocated polarity is the
opposite of `BITMAP.SYS`'s.** Subtask 4's `BitmapTest`/`BitmapSet`/
`BitmapClear` document "1 = free" for the storage bitmap, and this
subtask's write-up above assumed the same convention would carry over —
but it doesn't. Checked empirically against `testdata/rq0-ra92.dsk` (read
every one of its 38900 header slots directly, classified each as
"looks unused" by the same zero-checksum/zero-file-number test
`FindFreeSlot` uses below, and compared against this region's actual
bits): a set bit means the slot is **in use**, a clear bit means free —
zero mismatches under that polarity, thousands under the reverse. This
also matches the reference implementation's own `headmap_clear()`
(`update.c:230`, clearing a bit to free a slot) and `update_findhead()`
(`update.c:264`, treating a clear bit as a free candidate) — unlike most
of `update.c` (see [What we're deliberately not
porting](#what-were-deliberately-not-porting-from-the-c-reference)), this
one bit's meaning is genuine on-disk data, not a reference artifact, so it
has to be reproduced exactly rather than "corrected" for consistency with
`BITMAP.SYS`. `ondisk.BitmapTest`/`BitmapSet`/`BitmapClear` are still
reused as pure bit-position mechanics; only the interpretation at each
call site in `IndexBitmap` is reversed, documented at length on the type
itself so the discrepancy isn't rediscovered by surprise again later.

`FindFreeSlot() (uint32, error)` scans bit indices from
`HomeBlock.ReservedFiles` (always read from the home block, never a
hardcoded constant — matching the table entry above about
`headmap_clear`'s own hardcoded `10`) up to `MaxFiles`, and, matching the
reference's own worthwhile safety check in `update_findhead()`, reads the
candidate slot's actual on-disk header before trusting a clear bit:
`ondisk.DecodeFileHeader` is called directly on it (its returned struct is
usable even when the checksum doesn't validate, per its own doc comment),
and a slot only counts as genuinely free if both `Checksum` and
`Fid.Number()` come back zero. A mismatch — a clear bit over a slot whose
header looks real — is treated as a bitmap/reality inconsistency and
fails loudly with an error rather than silently skipping to the next
candidate, on the theory that silently working around it would risk
eventually handing out a slot that still holds live data; surfacing it
clearly is also exactly the kind of check `ANALYZE/DISK` (subtask 15) is
designed to run at a whole-volume level later. `MarkAllocated`/`MarkFree`
take a file number directly (not an `Extent` — a header slot isn't a
block range) and validate it against `MaxFiles`, mutating the in-memory
bits and marking the cache dirty; `Flush` writes every bitmap block back
via `resolveExtentLBN(dev.IndexFile.Extents, vbn)` + `WriteBlock`, the same
deferred, whole-bitmap-dirty design as subtask 6's `Bitmap.Flush`.

`internal/odstest.HomeBlockFixture` gained a `ReservedFiles` field
(plumbed through to `ondisk.EncodeHomeBlock`) since no existing test
needed to control it before this subtask's `FindFreeSlot` tests did.
Coverage: reserved-slot skipping combined with an additionally-marked-in-
use slot (distinguishing "skipped because reserved" from "skipped because
the bit says in use"); the bitmap/header consistency check catching a
deliberately-corrupted fixture (a clear bit over a real-looking header);
`MarkAllocated`/`MarkFree` each observably changing what a later
`FindFreeSlot` returns; file number 0 and beyond-`MaxFiles` rejected; the
read-only-device and zero-`MaxFiles` rejections at `OpenIndexBitmap` time;
and the same deferred-flush proof subtask 6's tests used (two
independently-opened `IndexBitmap`s over the same device confirming an
unflushed mutation is invisible until `Flush`).

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

**Shipped**, as `volume/writeheader.go`. `CreateHeader(dev *Device, ib
*IndexBitmap, opts NewFileHeader) (*File, error)` allocates a slot via
`ib.FindFreeSlot`, builds a Fid whose sequence number is one more than
whatever the slot's previous occupant (if any) last held (matching the
reference implementation's `update_addhead()` — genuine on-disk semantics
`readFileHeaderViaIndex`'s stale-Fid check depends on, not a reference
artifact, so it's reproduced rather than simplified away), and encodes/
writes the header immediately via the new `EncodeFileHeader`. Owner/
FileProtection come from `HomeBlock.VolumeOwner`/`FileProtection` as
planned, not a hardcoded UIC.

`Extend(f *File, bm *Bitmap, ib *IndexBitmap, additionalBlocks uint32)
error` turned out to need more supporting machinery than the subtask
write-up's one-paragraph sketch implied, because "append the new extent to
whichever header currently has room" has to actually locate that header
(walking the `ExtensionFid` chain via a new `tailHeader`), decide whether it
fits (`appendExtent`, which tries a real `EncodeFileHeader` call and treats
its only failure mode — the 402-byte variable area overflowing — as "no
room left" rather than an error), and, when it doesn't, allocate and link a
new segment (`linkNewExtensionSegment`). One real discovery along the way:
the reference implementation's `update_extend()` links a new extension
segment's `Backlink` to the segment it extends (primary or a prior
extension), not to the file's parent directory the way a primary header's
own `Backlink` works — confirmed by reading `update_extend()`/
`update_addhead()` together (the `back` parameter passed at the extension
call site is `&head->fh2$w_fid`, the *current tail's* own Fid, not the
directory Fid `update_create()` passes for a brand-new file). Reproduced as
real on-disk semantics, not "corrected" to match the primary's convention.

A caller needing more space than the single largest free run offers (per
subtask 6's own note that this is expected, not `Bitmap`'s job to solve) is
handled by a new `allocateExtents` helper: a simple linear "try the full
remaining amount, then one less" search for the largest run that still
fits, looping until the full request is satisfied or space runs out — the
simplest thing that works, matching this project's general preference over
a cleverer search these volumes have no real need for. It rolls back (via
`Bitmap.MarkFree`) whatever it allocated on its own failure path, so a
failed `Extend` leaves `bm`'s in-memory state exactly as it found it, not
just the file untouched.

`HighWaterMark` needed less "correct updating" than the subtask write-up
anticipated once the mechanics were worked out: `Extend` never moves it at
all. Newly allocated blocks are, by construction, always at or beyond the
file's *previous* `HighestBlock`, and `HighWaterMark` can never exceed that
— so leaving it untouched is what keeps new space reading back as zero
per `File.ReadBlock`'s existing guarantee, with nothing to "get right"
beyond not touching it. `RecordAttributes.HighestBlock` on the primary
header is what actually needs updating, and always gets one final rewrite
at the end of `Extend` (via a new `existingAreas` helper that reconstructs
a decoded header's current IDENT/map content for a re-encode) to record the
new total, even on a call where every new extent landed in an extension
segment and the primary's own map/ident never changed.

One bug caught before it shipped: `linkNewExtensionSegment` rewrites the
old tail's on-disk `ExtensionFid` immediately, but its first draft discarded
the freshly-decoded result — if that old tail was the *primary* header,
`Extend`'s in-memory `f.Header` would stay stale (still showing a zero
`ExtensionFid`) until the function's own final "record `HighestBlock`"
rewrite at the end, which would then silently re-encode from that stale
copy and erase the very link just written. Fixed by having
`linkNewExtensionSegment` return the relinked tail's decoded header
alongside the new segment's, and syncing `f.Header` from it when the
relinked tail is the primary.

Also fixed in passing (in scope per this session's own instructions, since
found while doing this subtask's work): `readFileHeaderViaIndex` and
`IndexBitmap.headerVBN` each independently computed a file number's header
VBN with the same formula; `writeheader.go` needed that arithmetic a third
time, so it's now a single shared `fileHeaderVBN` helper (`file.go`) both
existing call sites were changed to use, rather than a third copy.

`internal/odstest.HomeBlockFixture` gained `VolumeOwner`/`FileProtection`
fields (same pattern as subtask 7's `ReservedFiles` addition) since no
existing test needed to control them before `CreateHeader`'s owner/
protection-defaulting tests did. `ondisk` gained one new exported constant,
`FileHeaderStructureLevel` (513 decimal — structure level 2, version 1),
the value the reference implementation writes into every header it
creates.

**Correction (subtask 12):** this entry originally claimed
`FileHeaderStructureLevel` was "distinct from `HomeBlock.StructureLevel`'s
own 0x0102" — backwards. Reading testdata/rq0-ra92.dsk's actual home
block bytes at that field (`01 02`) during subtask 12 showed they decode
little-endian to 0x0201 (513), the *same* value as
`FileHeaderStructureLevel`, not a different one; `0x0102` was this pass's
own byte-order slip, not a real on-disk value. `ondisk.HomeBlock`'s and
`ondisk.FileHeader`'s doc comments have been corrected to match.

Tests build on a new, wider writable fixture
(`newWritableHeaderTestVolume`/`installWideTestBitmap` in
`writeheader_test.go`) with far more free clusters than this package's
existing bitmap fixtures provide, specifically so a test could force the
extension-segment fallback for real (150 one-block `Extend` calls,
comfortably past the ~70-entry capacity a header's map area has once its
IDENT area is populated) rather than only unit-testing the internal helpers
in isolation. Coverage: slot allocation and field defaults (owner,
protection, backlink, zeroed record-attribute bookkeeping); sequence-number
increment from a hand-built "previously used, since freed" slot fixture
(deliberately not built via `odstest.BuildFileHeaderBytes`, since that
helper always computes a genuinely matching checksum, and this scenario
specifically needs a *stored* checksum of zero over *non-zero* other
content); round-tripping a created header through `Volume.OpenFID`; `Extend`
growing a file, zero-filling the newly-allocated space, accumulating
correctly across multiple calls, rejecting zero blocks, rolling back
cleanly when the volume is full, and — the main correctness target —
actually producing a linked extension segment with the right `Backlink`/
`SegmentNumber` when a header's map area fills up, verified via a completely
independent `OpenFID` re-read.

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

**Shipped.** `(*Directory) Insert(name string, version uint16, fid
ondisk.Fid, bm *Bitmap, ib *IndexBitmap) error` — `bm`/`ib` weren't in the
subtask's original one-line signature sketch above, but turned out to be
unavoidable: growing the directory's own allocation goes through subtask
8's `Extend`, which needs both to allocate space and (if the directory's
own header map ever fills up) a new extension-header segment.

Rather than an in-place byte-splice into one affected block, `Insert`
re-reads the directory's *entire* current content (`List()`), adds the new
entry to that set, and re-lays out the complete result from scratch via a
new `packDirectoryBlocks` helper — greedily filling one `EncodeDirectoryBlock`
worth of whole name-groups per block, moving to a new block only when the
next name group doesn't fit. This leans on subtask 5's `EncodeDirectoryBlock`
as the *sole* authority on "does this fit in a block," rather than
duplicating its byte-layout math to answer that question independently
(deliberately paying the small extra cost of a trial `EncodeDirectoryBlock`
call per candidate group over the life of a `packDirectoryBlocks` call,
in exchange for a single, already-tested source of truth for the on-disk
layout). If the new total needs more blocks than the directory currently
has allocated, `Insert` calls `Extend` for the difference before writing
anything; the newly written blocks are always written via `WritableContainer.
WriteBlock` directly (through `resolveExtentLBN`), not through a `File.
WriteBlock` — that's subtask 10, which depends on this one.

One real correctness subtlety, not called out in the subtask's original
write-up: writing real directory content into newly-extended space requires
advancing the directory header's `HighWaterMark`, not just its `HighestBlock`
(which `Extend` already updates). `Extend` itself *never* touches
`HighWaterMark` — by design, since it only allocates space without writing
into it (see subtask 8's own doc comment) — but `Insert` is the opposite
case: every block up to the new total is unconditionally overwritten with
real, freshly-encoded directory content. Leaving `HighWaterMark` where
`Extend` left it would make `File.isUnwritten` (and therefore `Directory.
List`, which uses it as a defensive bound) treat that freshly-written data
as still-simulated-zero unwritten space and stop reading before reaching it
— a real entry silently dropped from every subsequent `List()`. A new
`(*Directory) recordUsedBlocks` helper, called at the end of `Insert`, fixes
this by rewriting `RecordAttributes.EndOfFileBlock`/`FirstFreeByte` *and*
`HighWaterMark` together to reflect the directory's new logical size —
directory blocks are always written out at exactly `ondisk.BlockSize`
bytes, so the content always ends precisely on a block boundary, the same
`FirstFreeByte`-0/`EndOfFileBlock`-one-past-the-last-block convention
`File.UsedBlocks`' own doc comment already describes.

`NextVersion(name string) (uint16, error)` doesn't call `Lookup` directly
(despite the doc's original one-line description of it doing so) — `Lookup`
signals "name not found" via a formatted error string, and distinguishing
that from a genuine I/O/decode error by string-matching would be fragile.
Instead it calls `List()` itself and scans for the highest existing version
of `name`, returning `highest + 1` (which is already correctly `1` when
`highest`'s zero value means "no existing entries").

Tests reuse `writeheader_test.go`'s writable fixture
(`newWritableHeaderTestVolume`/`installWideTestBitmap`) via a new
`newWritableTestDirectory` helper (a `CreateHeader` call with `FchDirectory`
set). Coverage: insert into a freshly created, zero-block directory
(confirming the auto-extend-from-nothing path); a second version of an
existing name, confirming `NextVersion` predicts it and that `List()`
returns both versions highest-first; and 60 distinctly-named entries (each
needing 26 on-disk bytes, comfortably more than one 512-byte block holds),
confirming the directory's `Blocks()` grew past 1 and that every entry
survives, correctly grouped and versioned — checked both against the
in-memory `Directory` just mutated and, independently, through a completely
fresh `Volume.OpenDirectory` re-read (this phase's usual "dogfood Phase 1's
read path" cross-check).

No pre-existing bugs were found in the code this subtask built on.

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

**Shipped**, as `volume/writefile.go`. `(*File) WriteBlock`/`Close` and
`(*Volume) CreateFile` all needed `bm`/`ib` params the subtask's original
one-line sketches didn't show — the same deviation subtasks 8 and 9's own
write-ups already called out for the same reason (allocating/extending
needs both caches, and nothing else on `File`/`Volume` holds a reference to
them). Rather than thread them through every call, a `File` is instead
**armed for writing** once, via a new `(*File) OpenForWrite(bm *Bitmap, ib
*IndexBitmap) error`, which stores them as new unexported `bm`/`ib` fields
on `File` — `nil` on every ordinary `File` returned by `OpenFID` (Phase 1's
read path is completely unchanged), set once a `File` goes through
`OpenForWrite`. `WriteBlock` and `Close` both use "`bm`/`ib` are nil" as the
signal that a given `File` isn't writable, rather than adding a separate
boolean. `CreateFile` builds on `Directory.NextVersion` + `CreateHeader` +
`Directory.Insert` (subtasks 8/9) exactly as its subtask write-up describes,
then calls `OpenForWrite` itself so a caller creating a brand-new file can
go straight into `WriteBlock`/`Close` without an extra step.

`WriteBlock` extends via `Extend` (subtask 8) whenever `vbn` is beyond
`f.Blocks()`, then writes straight through via `resolveExtentLBN` +
`WritableContainer.WriteBlock` — no write-back caching for file data,
matching the design overview's "only the two bitmaps are cached" rule.
Unlike `CreateHeader`/`Extend`/`Insert`, it deliberately does *not* rewrite
the header on every call: it only records, in memory, the highest `vbn`
written so far (`maxWrittenVBN`), leaving `Close` to do the one header
rewrite needed to record `RecordAttributes.EndOfFileBlock`/`FirstFreeByte`/
`HighWaterMark` — otherwise writing a file block-by-block would cost one
full header re-encode/write per block. `Close` uses this package's existing
whole-block EOF convention (the same one `Directory.recordUsedBlocks`
already established for subtask 9: `FirstFreeByte` 0, `EndOfFileBlock` one
past the last block written), and only ever moves `HighWaterMark` forward,
never back, so it can't make an already-written block start reading as
simulated-zero again. `Close` is a no-op (not an error) on a `File` that was
never armed for writing, and disarms `f.bm`/`f.ib` at the end so it's safe
to call twice and so a `WriteBlock` after `Close` fails cleanly instead of
writing through stale state.

One real design question worth recording: `WriteBlock` allows out-of-order
writes (`vbn` 4 before `vbn` 2, say), but this package's on-disk
`isUnwritten` check is a single scalar `HighWaterMark`, not a per-block
bitmap — so `Close` advancing it past `maxWrittenVBN` necessarily also
"un-protects" any lower `vbn` that was skipped over, which would read back
as whatever is physically on the underlying container rather than a
guaranteed zero. In practice this is harmless for everything Phase 2 itself
ever writes (no file/directory deletion exists yet — see the non-goals
section — so no cluster this project hands out could ever hold a since-
deleted file's leftover data, and every fixture's free space starts
genuinely zero-filled, via `diskimage.Create`), but it's a real
simplification of VMS's own stronger guarantee, documented on `WriteBlock`
itself rather than silently assumed. A future record-aware writer (subtask
13) that needs the stronger guarantee, or partial-final-block accuracy, is
expected to build its own bookkeeping on top rather than this being
`WriteBlock`'s job.

Tests reuse `writeheader_test.go`'s `newWritableHeaderTestVolume`/
`installWideTestBitmap` fixture and `directory_test.go`'s
`newWritableTestDirectory` helper. Coverage: `WriteBlock`/`Close` on an
unarmed `File` (from `CreateHeader` alone, without `OpenForWrite`) reject
cleanly rather than panicking on nil `bm`/`ib`; wrong-sized data and VBN 0
rejected; the core out-of-order-write scenario (VBN 1, then a jump to VBN
4, confirming VBN 3 reads zero both before *and* after `Close`, then VBN 2
filled in), verified through a completely independent `OpenFID` re-read;
reopening an existing, already-written file via `OpenFID` + `OpenForWrite`
and overwriting one block in place, confirming the rest of the file and its
`Blocks()` are undisturbed; `CreateFile` end-to-end (directory entry
findable via `Lookup`, content round-trips through a fresh `OpenFID`, and a
second `CreateFile` of the same name gets the next version with a distinct
Fid); and `Close` being safe to call twice, with a `WriteBlock` after
`Close` rejected.

No pre-existing bugs were found in the code this subtask built on.

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

**Shipped**, as `volume/dismount.go`. The subtask write-up's one-line
`Dismount() error` sketch glossed over a real question: flushing "every
device's bitmap caches... if this mount session created any" requires
somewhere to actually find those caches, and subtasks 6/7 as shipped don't
give `Volume` one — `OpenBitmap`/`OpenIndexBitmap` are plain functions each
returning a brand-new, independent instance on every call (deliberately,
per their own tests: two independently-opened `Bitmap`s over the same
device is exactly how those subtasks prove the deferred-flush design
works), and `CreateHeader`/`Extend`/`Insert`/`CreateFile` all take a
`*Bitmap`/`*IndexBitmap` as an explicit caller-supplied parameter rather
than looking one up themselves.

Resolved by adding two new methods, `(*Device) Bitmap()` and `(*Device)
IndexBitmap()`, alongside two new unexported fields on `Device`
(`bitmap`/`indexBitmap`, both nil until first use): each opens its cache
via the existing `OpenBitmap`/`OpenIndexBitmap` the first time it's called
and returns that same cached instance on every later call. This is
additive, not a replacement — `OpenBitmap`/`OpenIndexBitmap` themselves are
unchanged, so every existing test's "two independent instances" scenario
keeps working exactly as before. The new methods are what future session-
level write-path code (subtask 14 and beyond) is expected to call to get a
`Bitmap`/`IndexBitmap` to pass into `CreateFile` and friends, and — the
point of this subtask — what `Dismount` itself uses to find whatever a
session actually opened: a nil field means "never asked for, so nothing in
memory could possibly be dirty," which is what makes an all-read-only
mount session's `Dismount` a genuine no-op rather than needing a separate
code path.

`(*Volume) Dismount() error` flushes every device's non-nil `bitmap`/
`indexBitmap` (in that order) and then closes every device's container,
deliberately attempting every device's flush and close even if an earlier
one fails — a `WriteBlock` error on device 1 of a volume set shouldn't
leave device 2's already-pending writes stranded in memory or its
container needlessly held open — reporting the first error encountered, if
any, wrapped with `volume: dismount:` context. `cmd/ods2/internal/session`'s
`cmdDismount` (`mount.go`) now calls it instead of looping
`dev.Container.Close()` directly, propagating any error instead of
silently discarding it as the old loop did (a small pre-existing rough
edge, not a functional bug since `plainImage.Close`/`MemContainer.Close`
never actually fail in this project's own test suite, but worth fixing
while touching this exact line).

Tests (`volume/dismount_test.go`) cover: `Device.Bitmap`/`IndexBitmap`
each returning the identical cached instance across two calls; `Dismount`
as a safe no-op on a volume that never had either method called on it (the
read-only-mount case); the primary acceptance scenario from this subtask's
own test plan — allocate through `Device.Bitmap()` with no intervening
`Flush`, `Dismount()`, then reopen the same host file as a completely
independent container/`Volume`/`OpenBitmap` and confirm the allocated
cluster's bit actually reads back as allocated on disk; and that `Dismount`
genuinely closes the container (a write afterward fails), not just
flushes.

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
   | --- | --- | --- |
   | 1 | INDEXF.SYS | index file (this volume's own file-header table) |
   | 2 | BITMAP.SYS | storage bitmap |
   | 3 | BADBLK.SYS | bad-block list (empty on a fresh volume) |
   | 4 | 000000.DIR | master file directory (root) |
   | 5 | CORIMG.SYS | core image file (historical; empty) |
   | 6 | VOLSET.SYS | volume-set list (empty for a single-disk volume) |
   | 7 | CONTIN.SYS | continuation file (historical; empty) |
   | 8 | BACKUP.SYS | backup journal (empty) |
   | 9 | BADLOG.SYS | bad-block log (empty) |

   **Update, once this subtask actually checked `testdata/rq0-ra92.dsk`:**
   there are no placeholder slots 10/11 — a volume's reserved set is
   exactly these nine files (`HomeBlock.ReservedFiles` = 9 on the real
   volume), and file numbers 10 onward are in ordinary, ReservedFiles-
   `FindFreeSlot`-visible use the moment anything creates a file (this
   real volume's own file 10 is `SECURITY.SYS`, file 11 `SYSEXE.DIR` —
   both perfectly ordinary files, not part of any reserved table). See
   this subtask's own "Shipped" write-up below for how this was confirmed
   and what changed as a result.

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

**Shipped.** Before writing any code, the reserved-file table's open
question (file numbers 5-11, never named anywhere in the C reference) was
resolved by actually inspecting `testdata/rq0-ra92.dsk` (subtask 0's own
note flagged this as worth doing once subtask 12 started): its home
block's `ReservedFiles` field reads 9, not 11, and `DIRECTORY /FULL
[000000]*.*` against it shows file numbers 1-9 are exactly this table's
nine names, in this table's own order, while file 10 is `SECURITY.SYS`
and file 11 `SYSEXE.DIR` — two perfectly ordinary files a real,
actively-used system happened to create in the header slots right after
the reserved ones, not further reserved placeholders. The table above is
updated accordingly, and `ondisk.CoreImageFileFid`/`VolumeSetFileFid`/
`ContinuationFileFid`/`BackupFileFid`/`BadBlockLogFileFid` (file numbers
5-9) plus `ondisk.ReservedFileCount = 9` were added to `ondisk/fid.go`,
completing what subtask 2 left pending.

The same real volume's headers also settled two more open questions this
subtask's original write-up hadn't anticipated needing:

- Every reserved file's `Backlink` — including `000000.DIR`'s own,
  self-referentially — points at `ondisk.MasterFileDirectoryFid`. Every
  header `Initialize`/`writeReservedFile` builds does the same.
- A reserved file's `RecordAttributes.EndOfFileBlock`/`HighWaterMark` are
  always `HighestBlock + 1` with `FirstFreeByte` 0, even for a genuinely
  empty file (`HighestBlock` 0, `EndOfFileBlock`/`HighWaterMark` 1) —
  the same "whole allocation counts as used content" convention this
  package's own `Directory.recordUsedBlocks` (subtask 9) already
  established for a freshly written directory block, now confirmed as
  real on-disk convention rather than this project's own invention.
  `writeReservedFile` (`volume/initialize.go`) reproduces it exactly.

This same pass also caught an unrelated, pre-existing documentation bug
while cross-checking the real volume's home block byte-for-byte: both
`ondisk.HomeBlock.StructureLevel`'s and `ondisk.FileHeaderStructureLevel`'s
doc comments claimed the home block's own structure-level value was
`0x0102`, distinct from a file header's `0x0201` (513) — backwards. The
real volume's raw bytes at that field are `01 02`, which decode
little-endian to `0x0201`, the *same* value a file header carries, not a
different one; `0x0102` was a byte-order slip in an earlier pass at that
comment, not a real on-disk value. Both doc comments are corrected (see
`ondisk/homeblock.go`/`ondisk/fileheader.go`); `Initialize` writes the
now-correctly-understood `0x0201` (via the existing
`ondisk.FileHeaderStructureLevel` constant, reused rather than a second
copy of the same magic number) into the home block it builds.

`volume.Initialize(c diskimage.WritableContainer, opts InitializeOptions)
error` (`volume/initialize.go`) matches the exposed shape the original
plan called for. `InitializeOptions` (`Label`, `Owner`, `FileProtection`,
`ClusterSize`, `MaxFiles`) is entirely optional — every field's zero
value selects a documented default (`NONAME`; UIC `[1,1]`; `0xFA00`,
also confirmed against the real volume's own reserved-file protection;
cluster size 1; and a volume-size-scaled `MaxFiles` with a floor that
always leaves room for a few user files even on a tiny volume) —
matching how little real VMS's own `INITIALIZE` actually requires beyond
a label.

The six-step plan above survived intact once the reserved-file-table
question was settled, with one structural decision the original
one-paragraph sketch didn't spell out: rather than going through
`volume.Mount` at any point before the home block is written (step 6),
`Initialize` builds its own `*Device` directly, with `Home` set to a
`HomeBlock` value that exists only in memory until the very last write.
This is what actually makes "write the home block last" possible: `Mount`
itself requires a *valid, on-disk* home block to find before it will
bootstrap `dev.IndexFile`, so using it mid-`Initialize` would force the
home block to be written first, exactly backwards from the ordering this
subtask's plan called for. Once `dev.IndexFile` is resolved by hand (via
the existing private `buildFile`, given the primary header
`Initialize` just wrote directly to the fixed, computable LBN
`bootstrapIndexFile` always reads it from — step 2, brute-forced exactly
as planned), every other reserved file's header is written through the
same private `writeHeader` helper `CreateHeader`/`Extend`
(`writeheader.go`) already use, rather than a duplicate encode-and-write
path — real code reuse, not just a shared convention. `BITMAP.SYS`'s and
`000000.DIR`'s own data (step 3's "minimal data" and step 4) are written
directly at their own computed LBNs before their headers, the same
brute-force approach as INDEXF.SYS itself, since neither `Bitmap`,
`IndexBitmap`, nor `Directory.Insert` exist yet at that point in the
sequence to allocate that space through. Step 5 (marking the reserved
region allocated in both bitmaps) is the first point `Initialize` uses
the ordinary `Bitmap`/`IndexBitmap` machinery at all — via `Device.
Bitmap`/`IndexBitmap` (subtask 11's accessors), not `OpenBitmap`/
`OpenIndexBitmap` directly, so a caller that goes on to create a file
right after `Initialize` returns (as the acceptance test below does) sees
the same cached instances rather than a second, independent read of what
was just written.

A new private `computeLayout` function owns all of step 1's arithmetic
(home block, INDEXF.SYS's bitmap and header area, BITMAP.SYS's data,
000000.DIR's one block, all laid out contiguously from LBN 0), returning
a clear error rather than a corrupt or truncated volume when the
container is too small for even the minimal reserved layout at the
requested `MaxFiles`/`ClusterSize`. One correctness point worth calling
out: `computeLayout`'s formula for how many blocks BITMAP.SYS's own bits
need is deliberately copied verbatim from `OpenBitmap`'s own (subtask 6)
rather than derived independently — the two have to agree exactly on
this number, or `OpenBitmap` would later try to read more (or fewer)
bitmap-bit blocks than `Initialize` actually allocated space for.

`cmd/ods2/internal/session/initialize.go` wraps it as `INITIALIZE path
size-in-blocks [label] [/CLUSTER=n]`, creating the host file via
`diskimage.Create` before calling `volume.Initialize`. Deliberately not
registered as a one-shot subcommand (`main.go`'s `oneShotCommands`) the
way `dir`/`copy`/`type` are: every one-shot command mounts its first
argument as an existing image before running anything, which cannot work
for a command whose whole job is to create that file — the same reason
`mount`/`dismount` themselves aren't one-shot subcommands either.
`INITIALIZE` also deliberately does not mount the volume it just built,
matching real VMS's own `INITIALIZE` (formats a device without mounting
it); a following `MOUNT path` picks it up like any other image.

Tests: `volume/initialize_test.go` covers the primary acceptance scenario
from this subtask's own test plan (`Initialize` a fresh container, `Mount`
it, confirm the master file directory lists all nine reserved files at
the right Fids with headers that decode cleanly, then `CreateFile` +
`WriteBlock` + `Close` + `Dismount`, reopen completely independently, and
confirm the new file reads back correctly — subtasks 10 and 11
end-to-end, not just Initialize in isolation), every `InitializeOptions`
field's default and override, a too-small container and a `MaxFiles`
below the 9 reserved slots both rejected with a clear error rather than a
corrupt volume, and the reserved region actually reading as allocated
(not just logically accounted for) in both bitmaps' raw bits, with the
very next cluster/slot past it reading free. `cmd/ods2/internal/session/
initialize_test.go` covers the command wrapper itself: end-to-end
`INITIALIZE` followed by an ordinary `MOUNT` of the file it created,
`/CLUSTER` reaching the resulting home block, and the same invalid-size/
undersized-volume rejections at the command layer.

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

**Shipped**, as `rms/writer.go`. `NewWriter(f *volume.File) (*Writer,
error)` mirrors `NewReader`'s own format dispatch and validation exactly;
`(*Writer) Put(record []byte) error` frames one record per call the same
way `Reader.Next()` reads one back (2-byte length prefix for Variable/VFC,
a delimiter appended after every Stream record, no framing at all for
Fixed/Undefined beyond the exact-size check), buffering framed bytes and
flushing a full `ondisk.BlockSize` chunk out via `File.WriteBlock` as soon
as one accumulates — no caching beyond that one in-flight partial block,
matching subtask 10's "no write-back caching for file data" rule.
`(*Writer) Close() error` zero-pads and flushes whatever partial block is
left buffered, then finalizes the header.

The one real design gap this subtask actually had to close: subtask 10's
own write-up flagged that `File.Close()` only ever records `FirstFreeByte`
as 0 (data ending exactly on a block boundary), and explicitly expected "a
future record-aware writer... to build its own bookkeeping on top rather
than this being `WriteBlock`'s job." Rather than have `rms.Writer` reach
into `volume`'s unexported header-rewrite machinery (`writeHeader`/
`existingAreas`, package-private and rightly so), `volume.File` gained one
new exported method, `CloseWithFinalByte(finalByte uint16) error`, and
`Close()` was redefined as exactly `CloseWithFinalByte(0)`: `finalByte` is
the offset within the highest block `WriteBlock` was asked to write of the
first byte past the file's real content, i.e. precisely
`RecordAttributes.FirstFreeByte`'s own on-disk meaning, sidestepping
`Close`'s whole-block-only assumption without duplicating any of its
`HighWaterMark`/disarm/error-handling logic. This is exactly the kind of
in-scope "found while building on it" fix the subtask 10 write-up
anticipated, not a bug in subtask 10's own shipped behavior (nothing before
this subtask needed partial-final-block accuracy).

Tests live in both packages: `volume/writefile_test.go` gained direct
coverage of `CloseWithFinalByte` itself (a partial final block's
`EndOfFileBlock`/`FirstFreeByte`/`UsedBlocks()` all correct and durable
across an independent re-`OpenFID`; `Close()`'s continued equivalence to
`CloseWithFinalByte(0)`; an out-of-range `finalByte` rejected). `rms`'s own
`writer_test.go` builds a genuinely writable volume from scratch for each
test via the public `diskimage.Create` + `volume.Initialize` + `volume.
Mount` + `volume.Volume.CreateFile` path (subtask 12) — package `volume`'s
own writable test fixtures are unexported and unreachable from a different
package — then round-trips every format (Fixed, Variable, VFC, all three
Stream variants) through `Writer` and a fresh `Reader` on an independently
re-opened `File`, plus: a record spanning a block boundary (200 fixed
4-byte records, forcing `append`'s flush-mid-`Put` path), data landing
exactly on a block boundary (confirming `Writer` reproduces `Close`'s own
whole-block convention rather than diverging from it), a wrong-sized fixed
record and an over-length Variable record both rejected, `MaxRecordSize` 0
rejected (`Reader`'s equivalent case treats it as an already-empty file
instead, since reading has no "further calls would be meaningless" failure
mode the way writing does), an empty file closing exactly like a
freshly-created one, and `Close` idempotent with `Put` afterward rejected.

No pre-existing bugs were found in the code this subtask built on, beyond
the already-anticipated `Close`/`FirstFreeByte` gap above.

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

**Shipped.** `cmdDismount` already called `Volume.Dismount()` as of
subtask 11's own commit (it wired that up as part of adding `Dismount`
itself, ahead of this subtask actually landing) — the only piece this
subtask still had to do was `MOUNT /WRITE` itself. `cmdMount` now checks
`quals.Has("write")` and, when given, opens every device via
`diskimage.OpenWritable` instead of `diskimage.Open`, collecting the
results into the same `[]diskimage.Container` slice `mountContainers`
already expected (a `WritableContainer` satisfies `Container`, so no
signature changes were needed downstream). Opening a raw-CD image `/WRITE`
now fails right at `MOUNT` with `OpenWritable`'s own clear error, instead
of mounting successfully and only failing later, confusingly, on the first
write attempt. No separate "is this volume writable" flag was added to
`Session` or `Volume`: whichever device(s) a write-path operation touches
already decide this the same way subtask 10's `File.OpenForWrite` does —
by type-asserting `Device.Container` to `diskimage.WritableContainer` —
and a device mounted without `/WRITE` now genuinely fails that assertion
(see the bug fix below), so a future mutating command (subtask 15/16) can
rely on that existing check rather than needing a second copy of the same
state.

**Bug found and fixed while building on subtask 1:** the type-assertion
check above (`f.Device.Container.(diskimage.WritableContainer)`, used
throughout package `volume` — `OpenForWrite`, `Bitmap`/`IndexBitmap`
opening, `writeHeader`, directory mutation, and more) only means anything
if a `Container` obtained from the read-only `Open`/`OpenFormat` can
actually *fail* that assertion. It couldn't: `plainImage`, the concrete
type both `Open` and `OpenWritable` returned, carried a `WriteBlock`
method unconditionally, regardless of whether the `*os.File` underneath it
had actually been opened `O_RDWR` or plain `O_RDONLY`. A type assertion
only inspects a value's method set, not how its fields were initialized,
so *every* plain-image `Container` — mounted `/WRITE` or not — already
satisfied `WritableContainer` before this subtask's `MOUNT /WRITE` wiring
gave that distinction any way to actually differ in practice. The write
itself would still have reached `os.File.WriteAt` and failed there with a
raw OS permission error, rather than the clean, intentional "device is not
open for write" error the check exists to produce — exactly the kind of
latent bug that stays invisible until something (this subtask) finally
exercises the read-only-vs-writable distinction end to end. Fixed by
splitting the single `plainImage` type in two:
`plainImage` (`diskimage/plain.go`) keeps only `ReadBlock`/`Blocks`/
`Close` and is what `Open`/`OpenFormat` construct; a new
`writablePlainImage` embeds `plainImage` and adds `WriteBlock`, and is
what `OpenWritable`/`OpenFormatWritable`/`Create` construct instead. Added
`TestOpenPlainImageDoesNotImplementWritableContainer` to
`diskimage/container_test.go` as a regression guard, and updated
`TestOpenWritableAutoDetectsPlainImage`'s type assertion to the new
`*writablePlainImage`.

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

**Shipped.** The core logic lives in `volume` (`volume/analyze.go`), not
`cmd/ods2` — `AnalyzeDisk(dev *Device) (*DiskReport, error)` for the
read-only pass and `RepairDisk(dev *Device) (*DiskReport, error)` for
`/REPAIR` — with `cmd/ods2/internal/session/analyze.go` a thin command
wrapper around both, matching how `INITIALIZE`'s own command/library split
(subtask 12) already works.

One real simplification over the subtask write-up's original sketch:
there's no need to separately chase "its directory's own extents, and any
extension segments" the way the write-up above describes. An extension
header segment occupies its **own** header slot with its **own** file
number and its **own** retrieval-pointer map (confirmed by re-reading
subtask 8's `linkNewExtensionSegment`: the new segment's Fid comes from
`ib.FindFreeSlot()`, an ordinary, independent slot allocation, not
anything derived from the primary file's own number) — so simply walking
*every* file number `1..MaxFiles` and decoding whichever slots currently
hold a genuinely in-use header already visits every block any file, primary
or extension segment alike, actually claims, exactly once each. No
`ExtensionFid`-chain walk (the way `buildFile` resolves one specific file's
complete extent list) is needed at all here, because this pass isn't
resolving any one file — it's visiting every header slot regardless of
which file it belongs to.

A gap the original write-up didn't anticipate: the boot block (LBN 0) and
the volume's home block (`HomeBlock.HomeLBN`) are reserved space that
exists *before* any file does, so no file's retrieval pointers ever claim
them — but `Initialize` (subtask 12) correctly marks them allocated in
`BITMAP.SYS` regardless (its own `reservedExtent` covers LBN 0 through the
master file directory's block). Without accounting for this separately,
`AnalyzeDisk` would misreport those two blocks as reclaimable on *every*
volume, including a freshly `Initialize`d one — caught by this subtask's
own primary acceptance test before it shipped. Fixed by unconditionally
treating LBN 0 through `HomeBlock.HomeLBN` as allocated, independent of
what any file's headers claim. A real volume's alternate/backup home block
and index-file copies (`HomeBlock.AlternateHomeLBN`/`AlternateIndexLBN`),
where present, are **not** currently given the same treatment — this
project's own `Initialize` never writes them, so no test fixture exercises
that gap; documented on `AnalyzeDisk` itself as a known limitation worth
revisiting if `ANALYZE/DISK` is ever run against a real volume that has
them (`testdata/rq0-ra92.dsk`, say), rather than guessed at without a real
example to confirm the exact reserved region's size against.

`OpenBitmap` (subtask 6) requires a `diskimage.WritableContainer` — sensible
for its own purpose (nothing obtained through it can ever be flushed
otherwise), but wrong for `AnalyzeDisk`'s read-only pass, which needs to
work against a volume mounted without `/WRITE` too (only `/REPAIR` should
need write access). Resolved by factoring `OpenBitmap`'s body into a new,
unexported `loadBitmap(dev *Device) (*Bitmap, error)` that reads
`BITMAP.SYS` into memory without checking (or storing) a container at all;
`OpenBitmap` itself becomes that fast-fail write check followed by a
`loadBitmap` call. `AnalyzeDisk` calls `loadBitmap` directly, so it never
needs write access; `RepairDisk` only reaches for `dev.Bitmap()` (which
does still require it) after confirming there's actually something to fix,
so running `/REPAIR` against an already-clean, read-only-mounted volume is
still a safe no-op that never touches the write path at all.

Every in-use header's own retrieval pointers are trusted as-is; a header
slot that doesn't decode cleanly (checksum mismatch) or whose stored Fid
doesn't match its own slot number is treated as free (contributes no
claimed blocks) rather than guessed at — a corrupt *header*, as opposed to
a corrupt *bitmap*, isn't what this subtask's own scope covers (the
per-file safety checks subtask 7's `FindFreeSlot` already performs are
what catch that class of inconsistency), and a bitmap-consistency pass
that silently patched over an unreadable header would risk computing a
wrong "truth" to repair *against*. If a legitimately in-use header's own
map area fails to decode (`RetrievalPointers()` returning an error),
`AnalyzeDisk` fails loudly with that error instead of proceeding on
incomplete information, on the same "surface it, don't guess" principle
subtask 7's own `FindFreeSlot` already established for a bitmap/header
mismatch.

`BitmapDiscrepancy` additionally records `ClaimedByFile`, the file number
whose retrieval pointers claim a "marked free but used" cluster (0 for the
"marked allocated but unused" case, where by definition no file claims
it) — not called for explicitly by the write-up above, but a small,
essentially-free addition once the per-cluster claimant is already being
tracked to build the computed bitmap in the first place, and genuinely
useful in `ANALYZE/DISK`'s own printed output (`cmd/ods2/internal/session/
analyze.go`) for pointing at *which* file's data was at risk.

`cmd/ods2`'s own `analyze.go` registers a single-word command, `analyze`,
with `disk` and `repair` as ordinary qualifiers (`Qualifiers: []string{
"disk", "repair"}`) — not a two-word verb the way `SET DEFAULT`/`SHOW
DEFAULT` are, and not a literal `"analyze/disk"` table entry either. This
follows the same normalization `MOUNT`/`WRITE` and `INITIALIZE`/`CLUSTER`
already established: this project's own command-line tokenizer
(`tokenize.go`) recognizes a `/name` qualifier anywhere on the line
regardless of spacing, so `ANALYZE device /DISK` and `ANALYZE/DISK device`
parse identically — gluing `/DISK` onto the verb with no space (the way
real DCL usually writes it, and the way this document itself refers to the
command throughout) works fine, but isn't the only accepted spelling.
Unlike `/WRITE`, `/DISK` is *required* here, not optional: it's the only
`ANALYZE` mode this project implements (real VMS's `ANALYZE` also has
unrelated modes like `/RMS_FILE` this project has no reason to support),
and requiring it keeps the command self-documenting rather than letting a
bare `ANALYZE device` silently mean the same thing. `ANALYZE/DISK` is
deliberately **not** registered as a one-shot subcommand (`main.go`'s
`oneShotCommands`) — like `MOUNT`/`DISMOUNT`/`INITIALIZE`, its argument is
a device name that must already be mounted, not a host path to mount
automatically, and one-shot mode's fixed `mount <image>` (no `/WRITE`)
couldn't support `/REPAIR` regardless.

A mounted volume set (more than one `Device`) is rejected by
`cmd/ods2/internal/session/analyze.go` itself, before either device is
ever touched, matching this phase's own non-goal scoping of `ANALYZE/DISK`
to single-device volumes (see this document's own non-goals section) —
`volume.AnalyzeDisk`/`RepairDisk` themselves take a single `*Device`, so
there's no ambiguity for the command layer to resolve about which member
of a set to check.

Tests: `volume/analyze_test.go` builds its fixture the way this phase's
`Testing strategy` section calls for (`Initialize` + `Mount` + `CreateFile`,
not hand-rolled bytes), and covers exactly this subtask's own test plan —
zero discrepancies on a known-good, freshly written volume; a real file's
own data cluster hand-flipped free (plus, independently, an unused cluster
hand-flipped allocated) reported as exactly two discrepancies, correctly
classified in each direction, with the used-cluster one correctly
attributing `ClaimedByFile`; `/REPAIR`'s fix confirmed via a completely
independent follow-up `AnalyzeDisk` call (a fresh on-disk re-read, not an
in-memory check); `/REPAIR` on an already-clean volume never touching
write-only state at all; `/REPAIR` against a read-only-mounted device
failing with a clear error; and `AnalyzeDisk` against a never-mounted
`Device` failing cleanly instead of a nil-pointer panic. One test-writing
subtlety worth recording: hand-corrupting a bitmap by freeing a real file's
cluster and then calling `Bitmap.FindFree` to pick an unrelated cluster to
over-allocate has to find that second cluster *before* freeing the first
— otherwise `FindFree`'s first-fit scan can pick the very cluster just
freed, and the two edits cancel out into an undetectable net-zero
"corruption," which is exactly what happened on this test's first attempt.
`cmd/ods2/internal/session/analyze_test.go` covers the command-layer
wiring on top (qualifier requirement, unmounted/multi-device rejection,
and the full report → repair → re-verify cycle's output through
`cmdAnalyze`), leaning on `volume/analyze_test.go`'s own coverage for the
underlying analysis logic itself rather than duplicating it.

No pre-existing bugs were found in the code this subtask built on.

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

**Shipped**, but as `COPY` *volume → volume* rather than strictly *host →
volume* — the subtask title undersold what turned out to be the natural
scope once `copy.go`'s existing destination-resolution logic
(`resolveDestination`, `destIsDirectory`) was extended to recognize a
second kind of destination at all: any VMS-syntax destination
(`device:[dir]name.type`) naming a *currently mounted* volume, checked via
a new `volumeDestination` helper (looked up by the text before dest's
first `:` — the overwhelmingly common `dest` shape, an ordinary host path
with no `:` at all or one that doesn't name a mounted device, is never
mistaken for one). Since `copy`'s *source* is always a file spec on an
already-mounted volume regardless of which direction the destination
turns out to be, "host → volume" and "volume → volume" are the same code
path once the destination is recognized as a volume at all — there was no
extra cost to also covering the volume-to-volume case, and no clean way to
exclude it.

New library-level support this needed, not called for by the subtask's
own one-paragraph sketch: `filespec.ResolveDirectory(vol, dirs)`
(`filespec/glob.go`), factored out of `Glob`'s own internal `walkDirs` —
the directory-path-walking logic `Glob` already uses to reach the
directory a file spec's name/type pattern is matched *within* is exactly
what's needed to open the destination directory itself (for
`Directory.Insert`), just exposed as its own function rather than folded
into a file listing.

`cmdCopy`'s destination handling branches once, early, on
`volumeDestination`'s result, but the loop body genuinely bifurcates: a
volume destination resolves its target `Directory` and bitmap caches
(`Device.Bitmap`/`IndexBitmap`) once, up front (not per matched file —
every match shares one destination directory), then for each match either
calls the new `copyOneFileToVolume`, which creates the file via
`Volume.CreateFile` (subtask 10) and writes its content one of two ways:

- `/BINARY`: `copyRawToVolume` copies the source's exact bytes
  block-by-block (`File.ReadBlock`/`WriteBlock`), the same convention
  `copyBinary` already established for the read direction, finishing with
  `CloseWithFinalByte` (subtask 13) rather than `Close`'s whole-block
  rounding, so a file whose length isn't a multiple of `ondisk.BlockSize`
  still round-trips to its exact byte count.
- Default (text mode): `copyRecordsToVolume` builds a Stream_LF file via
  `rms.Writer` (subtask 13) — the simplest text convention to target
  without negotiating VMS's full record-format/carriage-control space on
  the write side. It reuses `writeRecords`' own per-source-format logic
  (VFC carriage-control expansion, Fixed/Variable/Stream all normalized to
  plain `\n`-terminated text) — the same code that already builds a host
  text file's content — through a small new `lineSplitWriter` adapter that
  re-splits that already-linear text back into individual records for
  `Writer.Put` to apply Stream_LF's own framing to, rather than
  duplicating any per-format read logic a second time.

This is also why /TIME, /IGNORE, /DIRS, and the line-ending qualifiers
don't carry over, as this subtask's own write-up anticipated: /TIME has no
host mtime to preserve; /DIRS' "materialize an empty directory" has no
write-side equivalent yet (a matched `.DIR` source entry is always skipped
for a volume destination, regardless of /DIRS); and /IGNORE/`/CRLF`/`/LF`
are about *how* text gets reframed, a choice this subtask deliberately
narrowed to "always Stream_LF" rather than opening in full. `/QUIET`,
`/VERBOSE`, `/TEST`, and `/BINARY` all work exactly as they do for a host
destination, including `/TEST` never touching the destination volume's
bitmap caches at all (not just skipping the write) — resolving the
destination directory is itself skipped under `/TEST`, since nothing
afterward needs it.

Destination name/type resolution (`volumeDestName`) mirrors
`resolveDestination`'s existing `*`-substitution rule for a host
destination: a destination naming no file of its own at all (just
`device:` or `device:[dir]`) keeps every matched file's own name, and `*`
or `%` in the destination's name or type individually falls back to the
source's own value for that component — the same "wildcard-copy" rule
real DCL applies. Version numbers are never taken from the destination
text (a real VMS destination version is meaningless for `COPY`'s own
auto-versioning goal); `CreateFile`'s own `Directory.NextVersion` call
always decides it, and `copyOneFileToVolume` looks the result back up via
`Directory.Lookup` afterward (rather than having `CreateFile` return it
directly) purely to report it in `/VERBOSE`'s and the final confirmation
message's output.

One design point worth recording since it wasn't obvious until working
through it: a destination directory mounted read-only fails with a clear
error at the `Device.Bitmap()`/`IndexBitmap()` call resolved up front
(subtask 6/7's own read-only rejection), before any file is touched —
`cmdCopy` itself needs no separate "is the destination volume writable"
check of its own, matching subtask 14's own design of not tracking that
as separate session state.

Tests: `filespec/glob_test.go` covers `ResolveDirectory` directly (root,
a nested path, case-insensitivity, not-found, and a plain file name
correctly rejected — it only ever resolves `.DIR` entries).
`cmd/ods2/internal/session/copytovolume_test.go` builds a session with one
read-only source volume (`newTypeTestSession`'s existing fixture) and one
freshly `Initialize`d, genuinely writable destination volume (`diskimage.
Create` + `volume.Initialize` + `volume.Mount`, mounted under device
"DEST"), and covers: default text-mode copy reading back correctly through
`TYPE` (and landing as genuine Stream_LF, not just coincidentally correct
bytes); `/BINARY` round-tripping through both directions (host → volume →
host) and matching the source's exact bytes; a wildcard, directory-only
destination keeping every matched file's own name; a second copy to the
same name/type getting the next version, confirmed both in the
confirmation message and via an independent `Glob`; an ambiguous
multi-match literal volume destination rejected the same as a literal host
one; a destination volume mounted read-only rejected with a clear error;
`/TEST` writing nothing at all; and a plain host-path destination
continuing to behave exactly as before this subtask (no regression to
`copy`'s original direction).

No pre-existing bugs were found in the code this subtask built on.

**Follow-up: real `/HOST` source support.** A user hitting `copy go.sum
dua1:*.* /verbose` after this subtask shipped got a confusing
`%ODS2-E-ERROR, copy: go.sum not found` — exactly the gap this subtask's
own write-up called out (it shipped as volume → volume, not the
host → volume its title promised): `cmdCopy` always parses `source-spec`
as a VMS spec against the mounted volume, so a bare host filename that
happens to also be a syntactically valid (if nonexistent) VMS name just
looks like a typo for a volume file, never a host path.

Added a `/HOST` qualifier (`cmd/ods2/internal/session/copy.go`) rather
than guessing the direction from context: with it, `source-spec` is a
literal host path (single file, no wildcard expansion) and `destination`
must resolve to a volume via the existing `volumeDestination` — a
non-volume destination under `/HOST` is rejected outright, since
host → host isn't this command's job. `cmdCopyFromHost` mirrors
`cmdCopy`'s own volume-destination branch closely: `resolveVolumeDest`
was factored out of that branch (directory + bitmap-cache resolution) so
both paths share it; `volumeDestName` is reused as-is for the
`*`/`%`-substitution rule, fed a synthetic `filespec.Match{Name, Type}`
built from the host path's base name (`hostBaseNameType`, splitting on the
last `.`, same convention `splitNameTypeVersion` already uses — but,
unlike every other name in this project, upper-cased: a host base name is
ordinary host-filesystem text with no VMS casing convention behind it at
all, so the auto-derived name is upper-cased to look like a real VMS name
rather than merely resembling one, the same normalization real VMS's own
DCL applies to unquoted command-line text. This is the one place in the
whole project that upper-cases a name at all — every *typed* VMS name
elsewhere is still used exactly as given, matching case-insensitively).
`/BINARY` writes the
host file's exact bytes block-by-block (`copyHostRawToVolume`, the
`/HOST` counterpart of `copyRawToVolume`); the text-mode default
(`copyHostRecordsToVolume`) reads the host file as plain lines — CRLF
normalized to LF — and writes each as a `Stream_LF` record via
`rms.Writer`, needing none of `writeRecords`' VMS-record-format-aware
logic since a host file has no record structure of its own to interpret.

**A real, pre-existing bug surfaced while manually verifying this fix,
unrelated to `/HOST` itself:** reading back a `/BINARY`-created
(`Undefined`-format) file via `TYPE` — or via a plain, non-`/BINARY`
`COPY` off the volume — corrupts it whenever its length isn't an exact
multiple of 512 bytes, and errors outright once record parsing runs past
the last real byte. Root cause: `rms.Reader` (`rms/reader.go`) treats
`RecordFormatUndefined` exactly like `RecordFormatFixed` — one
block-sized "record" per read — with no awareness that `/BINARY`'s own
writer (`copyOneFileToVolume`/`copyHostFileToVolume`) already recorded the
file's true byte length via `CloseWithFinalByte`. Reproduced with no
`/HOST` involved at all, purely through this subtask's original
volume → volume `/BINARY` direction, so it predates this follow-up
entirely — it was simply never exercised by subtask 16's own tests, which
only round-tripped `/BINARY` back through `COPY` (a plain block-by-block
copy, per `copyBinary`/`rms.FileByteLength`, that never goes through
`rms.Reader` at all) rather than `TYPE`. Documented as a known limitation
in `COMMANDS.md`'s `COPY` section rather than fixed here — an `rms.Reader`
fix is unrelated in scope to `/HOST`'s own write-side addition, and
deserves its own change and tests.

Tests (`cmd/ods2/internal/session/copyfromhost_test.go`) reuse
`newVolumeDestTestSession`'s fixture and cover: default text-mode copy
reading back correctly through `TYPE`, landing as genuine `Stream_LF`;
`/BINARY` round-tripping through `COPY ... /BINARY` back to a host path
(not `TYPE` — see the bug above) and matching the source's exact bytes;
a wildcard volume destination taking the host file's own base name,
upper-cased; a second copy to the same name/type getting the next
version; a non-volume destination rejected; a missing or directory host
source rejected; `/TEST` writing nothing; and CRLF-to-LF normalization,
including a final line with no trailing newline at all.

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
