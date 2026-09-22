# Phase 3 — deletion, version limits, and PURGE

This is the design and progress-tracking document for Phase 3: adding file
deletion, version-limit enforcement, and a `PURGE` command to the Go ODS-2
implementation. [PHASE-01.md](PHASE-01.md) covered the initial read-only
build-out; [PHASE-02.md](PHASE-02.md) added write support (creating files,
extending them, and building/repairing a volume) but explicitly deferred
deletion — its own [non-goals
section](PHASE-02.md#non-goals-explicitly-out-of-scope-for-phase-2) points
here: *"Phase 2 only creates file-header and directory-entry state; freeing
it is a future phase with its own design pass."* This is that phase.

**Like PHASE-02.md, this document is a living record, not just a
retrospective checklist.** It's written *before* implementation starts, and
each subtask below is updated in place — status, design decisions actually
made, deviations from the plan, and a link to the commit(s) that closed it —
as work on it lands. Treat the "Subtasks" section as the source of truth for
"what's left" at any point during Phase 3; the top status table is a quick
summary of the same information.

The reference C implementation has nothing to consult for any of this:
its own delete path, `deallocfile()`, is author-acknowledged broken
(`update.c:451-453`: *"This routine has bugs and does NOT work properly
yet!!!!"*, PHASE-02.md's own non-goals entry already quotes this), and it
implements no version-limit enforcement or `PURGE` equivalent at all. Every
design decision below is this project's own, not a port.

## Goals

- A `Volume.DeleteFile`-shaped API (name TBD when subtask 3 starts) that
  fully reclaims a file's storage: every extension-header segment's header
  slot returned to `INDEXF.SYS`'s bitmap, every data extent (across every
  segment) returned to `BITMAP.SYS`, and the directory entry removed —
  precisely reversing what `CreateHeader`/`Extend`/`Directory.Insert`
  (PHASE-02.md subtasks 8-9) did to create the file in the first place.
- A `DELETE` CLI command built on that API: `DELETE name.type;n` deletes one
  specific version; `DELETE name.type;*` deletes every version of that
  name; the name portion may be wildcarded (`DELETE *.TXT;3`) to hit
  several files in one command, resolved the same way `COPY`/`DIRECTORY`
  already resolve a glob. A version is always required — there is no
  "defaults to the highest version" form, unlike `DIRECTORY`'s convenience
  default, specifically so a bare `DELETE *.TXT` can never delete anything.
- Version-limit enforcement: `ondisk.RecAttr.VersionLimit` (already fully
  decoded/encoded since Phase 1 — see [Version-limit
  design](#version-limit-design)) becomes a live constraint. Creating
  version N+1 of a name automatically deletes however many of that name's
  older versions are needed to bring the count back within the limit that
  applies to the *new* version.
- A `SET FILE/VERSION_LIMIT=n file-spec` CLI command/API — the only way to
  actually set that attribute on an existing file (or directory; see
  below), without which the whole feature would be unobservable from the
  CLI in this phase.
- A `PURGE file-spec` CLI command with a `/LIMIT=n` qualifier (default 1,
  meaning "keep only the single most recent version"): for every distinct
  name the glob matches, deletes all but the `n` most recent versions.

Because file/directory deletion was explicitly out of scope for Phase 2,
**this phase is free to add new exported `volume` API** (`Directory.Remove`,
a delete/deallocate entry point, a version-limit-aware wrapper around
`CreateFile`) without needing to preserve any existing deletion-shaped
contract — there isn't one yet.

## Non-goals (explicitly out of scope for Phase 3)

- **Directory storage shrink-back.** `Directory.Insert` only ever grows a
  directory's own block allocation (via `Extend`); nothing releases
  now-unused trailing directory blocks back to `Bitmap`. `Directory.Remove`
  (subtask 2) is the mirror image of `Insert` and inherits the same
  one-directional behavior deliberately — a directory that once needed N
  blocks keeps all N allocated even after every entry that justified the
  Nth block is deleted. Reclaiming that space is a real optimization a
  future phase could add; it's pure follow-on work with no effect on
  correctness (see that subtask's own write-up for why a directory
  legitimately *shrinking its logical size* while keeping its physical
  allocation is already a safe, tested shape by the time this phase is
  done).
- **`FchLocked` ("locked against deletion") enforcement.** `ondisk.FchLocked`
  already decodes (`ondisk/fileheader.go:12`) but nothing sets or checks it
  anywhere in this codebase, and no `SET FILE/LOCK`-equivalent command
  exists to set it. There is nothing yet to protect, so `DeleteFile`
  doesn't check it; adding a lock-setting command and wiring up the check
  is future work if it's ever needed.
- **`FchMarkDel`-based two-phase delete or crash-recovery reclamation.**
  `ondisk.FchMarkDel`/`FileHeader.IsMarkedForDeletion()` already decode but
  nothing sets or acts on them anywhere. This project has no journaling
  anywhere else either, and PHASE-02.md's own [Caching
  strategy](PHASE-02.md#caching-strategy) section already establishes the
  governing assumption: a single-process, single-writer-at-a-time CLI tool,
  not a multi-handle, crash-recoverable RMS server. Phase 3's `DeleteFile`
  reclaims a file's storage in one direct pass rather than marking it
  deleted and reclaiming it in a later sweep. The one real consequence,
  spelled out in [Ordering for
  safety](#ordering-for-safety-without-journaling) below, is accepted
  explicitly rather than solved: an operation interrupted mid-delete can
  leak space as an orphaned, no-longer-referenced file, recoverable only by
  a future `ANALYZE/DISK` enhancement that scans for exactly that shape —
  itself future work, not this phase's.
- **Retroactive version-limit enforcement.** Lowering a name's effective
  version limit (via `SET FILE/VERSION_LIMIT`) doesn't immediately purge
  any of that name's already-existing excess versions — enforcement only
  ever runs at the moment a *new* version is created (matching real VMS's
  own behavior, which doesn't retroactively purge on `SET FILE
  /VERSION_LIMIT` either). Running `PURGE` is how an already-over-the-new-
  limit name gets cleaned up immediately.
- **Multi-device volume sets, for writing** — same single-device scope
  Phase 2 already drew for every write-path operation; unchanged.
- **ODS-5 support, indexed/relative RMS files** — same scope boundary
  Phase 1 and Phase 2 both drew; unchanged.

## Status

| # | Subtask | Status |
|---|---|---|
| 1 | `volume`: file & header-chain deallocation primitive | Done |
| 2 | `volume`: `Directory.Remove` | Done |
| 3 | `volume`: `DeleteFile` (ties 1+2 together) | Done |
| 4 | `cmd/ods2`: `DELETE` command | Not started |
| 5 | `volume`: version-limit resolution + create-time enforcement | Not started |
| 6 | `volume`: `SetVersionLimit` | Not started |
| 7 | `cmd/ods2`: `SET FILE/VERSION_LIMIT=n` | Not started |
| 8 | `volume`: `PurgeVersions` | Not started |
| 9 | `cmd/ods2`: `PURGE` command | Not started |

Legend: **Not started** / **In progress** / **Done** (commit `abc1234`) /
**Deferred** (with a reason).

---

## Design overview

### No `ondisk` changes this phase

Worth calling out explicitly, since every prior phase touched `ondisk`
first: Phase 3 needs **no new `ondisk` code at all**. Everything it needs
already exists and already round-trips correctly:

- `ondisk.RecAttr.VersionLimit` (`ondisk/recattr.go:108`) is already
  decoded and encoded — Phase 1/2 built this field's wire format without
  Phase 3's use case in mind, but it's exactly what's needed.
- `Bitmap.MarkFree(ondisk.Extent) error` and `IndexBitmap.MarkFree(fileNumber
  uint32) error` (PHASE-02.md subtasks 6-7) are the exact primitives to
  release data extents and header slots.
- `ondisk.EncodeFileHeader`/`existingAreas` (PHASE-02.md subtask 2,
  `volume/writeheader.go`) already support "decode a header, change one
  field, re-encode, rewrite" — exactly what freeing a header slot and what
  `SetVersionLimit` (subtask 6) both need.
- `ondisk.EncodeDirectoryBlock`/`packDirectoryBlocks` (PHASE-02.md subtask
  5, `volume/directory.go`) already support "given a full entry set, lay it
  out across blocks" — `Directory.Remove` (subtask 2) needs no new encoding
  logic, just a different entry set than `Insert` builds.

The one on-disk field this phase deliberately does **not** wire up: the
per-name-record version-limit word inside a directory block (documented,
never read, in `ondisk/directory.go`'s package comment and
`DecodeDirectoryBlock`/`EncodeDirectoryBlock`). See [Version-limit
design](#version-limit-design) for why the directory file's own header
field is used instead, making that word still unnecessary.

### What deleting a file actually reverses

A file created by PHASE-02.md's write path has three things a delete needs
to undo, all discoverable by walking the same `ExtensionFid` chain
`buildFile` (`volume/file.go`) and `tailHeader` (`volume/writeheader.go`)
already walk for other reasons:

1. **One or more header slots.** A file's primary header occupies one slot
   in `INDEXF.SYS`; a large or fragmented file's additional extension
   segments (linked via `ExtensionFid`, one allocated per
   `linkNewExtensionSegment` call during `Extend`) each occupy their own
   slot. Every slot in the chain — not just the primary's — must be marked
   free in `IndexBitmap` and, critically, physically zeroed on disk:
   `IndexBitmap.FindFreeSlot` (`volume/indexbitmap.go`) doesn't just trust
   the bitmap bit, it re-reads the candidate slot and requires *both* a
   zero checksum and a zero file number before handing it out (a
   deliberate carry-over from the reference implementation's own
   `update_findhead()` safety check — PHASE-02.md subtask 7's write-up).
   Marking the bitmap bit free without also zeroing the slot's actual bytes
   would make `FindFreeSlot` refuse to ever reuse it, or worse, treat
   whatever stale header content is still sitting there as a real
   in-use/blank ambiguity. Writing an all-zero 512-byte block satisfies
   this exactly: an all-zero header decodes with `Checksum == 0` and
   `Fid.Number() == 0` (the checksum of an all-zero block is itself zero),
   which is precisely what the safety check requires.
2. **Every data extent in every segment's retrieval-pointer map.** Each
   segment's `RetrievalPointers()` (`ondisk/retrieval.go`) lists the
   extents *that segment* owns; a delete has to call it once per segment in
   the chain and mark every returned extent free via `Bitmap.MarkFree`, not
   just the primary's.
3. **The one directory entry** naming this file, via the new
   `Directory.Remove` (subtask 2).

No data blocks are physically zeroed. `File.ReadBlock`'s existing
high-water-mark guarantee (a block at or beyond `HighWaterMark` reads as
software-synthesized zero, regardless of what's physically on disk — see
`volume/file.go`) means a newly-created file that later gets allocated one
of these reused clusters is safe the instant its own `HighWaterMark` starts
at 0 and only advances as it's actually written; the old bytes underneath
are never exposed. This mirrors `WriteBlock`'s own doc comment, which
already anticipated this exact case: *"Phase 2 has no file-deletion support
yet and therefore never hands out a cluster that once belonged to a
since-deleted file"* — Phase 3 is what makes that sentence stop being true,
and the existing guarantee is exactly what makes it safe to.

### Ordering for safety without journaling

There's no atomic multi-block transaction primitive anywhere in this
project (PHASE-02.md didn't need one, and per [What we're deliberately not
porting](PHASE-02.md#what-were-deliberately-not-porting-from-the-c-reference)
isn't planned). Deleting a file is inherently multi-write (one directory
rewrite, one write per header slot being zeroed, plus deferred bitmap
flushes) — worth deciding up front which partial-completion state is
"safe" if a delete is interrupted (crash, killed process) partway through.

**`DeleteFile` (subtask 3) removes the directory entry first, then reclaims
storage.** Concretely: resolve and read the full header chain (read-only),
then call `Directory.Remove` (an immediate, non-deferred write, like every
other directory mutation), and only after that succeeds start zeroing
header slots and marking bitmap extents/slots free. This ordering means:
once `Directory.Remove` returns successfully, nothing on the volume
references this file's Fid anymore — the file is unambiguously gone from
every reader's point of view — regardless of whether the reclamation steps
that follow ever complete. An interruption after that point leaks the
file's header slots and data extents as an **orphan**: still marked
allocated in both bitmaps, still holding real (if now-unreachable) header
content, but referenced by nothing. That's a real cost, but a bounded and
recoverable one — a future `ANALYZE/DISK` enhancement scanning for header
slots with no owning directory entry would find and reclaim it — and
strictly better than the alternative ordering (reclaim first, remove the
directory entry last), which risks a directory entry surviving a crash
while pointing at an already-zeroed, corrupt-looking header.

`DeleteFile` does not attempt to roll back a partial failure during the
reclamation phase either (e.g. segment 2 of 3 fails to zero) — for the same
reason: there's no way to "undo" a directory-entry removal that's already
succeeded and been written, so a rollback of the reclamation half alone
wouldn't restore a consistent state anyway. The failure is surfaced as an
error; the caller's command layer (`DELETE`/`PURGE`) reports it and moves
on to (or stops before) its next target, per subtask 4/9's own design.

### Version-limit design

Three decisions, made up front rather than discovered mid-implementation:

1. **A directory's own default version limit lives on the directory
   *file's* own header — `RecAttr.VersionLimit` on the `.DIR` file itself —
   not the currently-unused per-name word inside directory records.** A
   directory is an ordinary `File` with a header
   (`Directory.File.Header.RecordAttributes.VersionLimit`), and that field
   already round-trips correctly today with zero new on-disk work. This
   also means `SetVersionLimit`/`SET FILE/VERSION_LIMIT` (subtasks 6-7)
   need no special-casing for "am I targeting a plain file or a directory"
   — a directory is just a file whose header happens to also have
   `FchDirectory` set, and the exact same rewrite-one-header-field logic
   applies uniformly to both.
2. **`VersionLimit == 0` always means "unlimited," full stop — it is never
   ambiguous with "unset, look at the directory."** A brand-new file name
   (its very first version) with no explicit override inherits its
   directory's *current* `VersionLimit` at the moment of creation; every
   later version of that same name simply carries forward whatever
   `VersionLimit` the immediately-previous version's own header already
   had, regardless of what the directory's default has since become. This
   matches real VMS's actual behavior (an attribute captured at creation
   and propagated version-to-version, not re-derived from the directory on
   every single create) and resolves what would otherwise be a genuine
   ambiguity about what `0` means. One direct consequence: `CreateFile`
   (subtask 5) computes the new version's `VersionLimit` itself and
   **ignores** whatever `VersionLimit` a caller's `ondisk.RecAttr` argument
   carries — there's no existing caller (`copy.go`'s `copyOneFileToVolume`/
   `copyHostFileToVolume`) that has ever had a reason to set it explicitly,
   and giving `CreateFile` sole authority over this one field removes any
   question about which of "caller said N" vs "inherited value" wins.
3. **`SET FILE/VERSION_LIMIT=n` is the only way to change it**, either on a
   specific file name (rewriting its current highest version's own header —
   future new versions of that name then carry the new value forward per
   point 2) or on a directory (rewriting the directory file's own header,
   changing the default *future* new names in it will inherit — existing
   names' already-captured limits are unaffected, per point 2 again).

### Where the new code lives

Mirroring both prior phases' package boundaries:

- **`volume`** — every subtask in this phase except the two CLI commands.
  `directory.go` gains `Remove`; new code (file TBD — `delete.go`,
  mirroring `writeheader.go`'s naming) gets the header-chain deallocation
  primitive, `DeleteFile`, `SetVersionLimit`, and `PurgeVersions`; existing
  `writefile.go`'s `CreateFile` gains the version-limit resolution +
  enforcement call from subtask 5.
- **`cmd/ods2/internal/session`** — two new command files, `delete.go` and
  `purge.go`, following the exact `Command{...}` + `init()`-registration
  pattern every existing command uses (`mount.go` for the simplest
  example, `analyze.go` for a flag-qualifier example, `initialize.go` for
  a value-qualifier example directly analogous to `PURGE`'s `/LIMIT=n`);
  `setshow.go`'s `cmdSet` gains a `file` sub-verb alongside its existing
  `default` one.

### Flush strategy

`DELETE`, `PURGE`, and `SET FILE/VERSION_LIMIT` each flush their target
device's `Bitmap`/`IndexBitmap` (via `Device.Bitmap()`/`IndexBitmap()`,
then explicit `.Flush()` calls) once, at the end of the command — matching
`initialize.go`'s and `analyze.go`'s existing explicit-flush pattern —
rather than leaving it to `Dismount`'s safety-net flush. This matters more
here than it did for most of Phase 2's own commands: a `DELETE`/`PURGE`
that deletes several matched files touches the bitmap caches repeatedly in
one command invocation, and a session that runs several such commands
before ever dismounting should not accumulate unbounded in-memory-only
dirty state across all of them.

---

## Subtasks

Each subtask is sized to be one commit (per this repo's convention: commit
each complete, testable task on its own). "Depends on" lists other Phase 3
subtasks that must land first; Phase 1 and Phase 2 are assumed complete
throughout.

### 1. `volume`: file & header-chain deallocation primitive

**Depends on:** nothing (first subtask; builds on Phase 2's `buildFile`/
`tailHeader` chain-walking precedent).

A new, low-level function (name/file TBD — something like `freeFileStorage
(dev *Device, primary ondisk.FileHeader, bm *Bitmap, ib *IndexBitmap)
error`) that, given an already-loaded primary `FileHeader`, walks its
`ExtensionFid` chain (the same traversal `tailHeader` already performs,
likely factored into a small shared helper returning every segment rather
than just the last one) and, for each segment: calls
`segment.RetrievalPointers()` and `Bitmap.MarkFree` on every returned
extent, then `IndexBitmap.MarkFree` on the segment's own file number, then
writes an all-zero `ondisk.BlockSize` block to that segment's header slot
(via the same `resolveExtentLBN(dev.IndexFile.Extents, ...)` +
`WriteBlock` pattern `writeHeaderBytes` already uses). This function does
**not** touch the directory — it's purely "given a file's header chain,
reclaim everything a header knows how to reclaim," reusable both by
subtask 3's `DeleteFile` and, potentially, a future orphan-reclamation
pass (see [Ordering for safety](#ordering-for-safety-without-journaling)).

**Tests:** build a synthetic file (via `CreateHeader`/`Extend`, same
fixture-construction style PHASE-02.md subtask 8's tests used) spanning
multiple extension segments and multiple extents per segment; call the new
function; confirm every extent it should have freed is now findable by
`Bitmap.FindFree` and every header slot is `IndexBitmap.FindFreeSlot`-
eligible again (both only after `Flush`, matching the deferred-cache
pattern's existing test idiom); confirm each zeroed header slot's raw bytes
really are all-zero, not just checksum-passing.

**Shipped**, as `volume/delete.go`'s `freeFileStorage(dev *Device, primary
ondisk.FileHeader, bm *Bitmap, ib *IndexBitmap) error`, matching the
sketch above almost exactly. One refactor along the way: `tailHeader`
(`volume/writeheader.go`) previously walked the `ExtensionFid` chain
itself, discarding every segment but the last; that walk is now factored
into a new package-level `fileHeaderChain(dev, primary)
([]ondisk.FileHeader, error)`, with `tailHeader` reduced to `chain[len(chain)-1]`.
`freeFileStorage` calls the same shared helper to get every segment, not
just the last. `buildFile` (`volume/file.go`) was deliberately left alone
rather than also converted to build on `fileHeaderChain` — it has its own
bootstrap-time special case (falling back to its own not-yet-fully-resolved
`Extents` as the index-file extents to search, needed only while resolving
`INDEXF.SYS`'s own `File` for the very first time) that `fileHeaderChain`
has no reason to carry, since both of its callers only ever run against an
already-fully-mounted device.

Tests (`volume/delete_test.go`) cover a single-segment file and a
150-block multi-segment file (forcing a real extension segment, the same
technique `TestExtendAllocatesExtensionHeaderSegmentWhenMapIsFull` already
used), a read-only-device rejection, and — beyond what the plan called
for — an explicit check that a freed header slot's bytes are genuinely
all-zero on disk (not merely checksum-and-Fid-zero, which a corrupted but
coincidentally-zero-looking block could also satisfy). The "every extent
was reclaimed" check turned out to have a clean, strong form given how the
existing `installWideTestBitmap` fixture is shaped: since every extent
either test ever allocates comes from one originally-contiguous free run,
successfully finding that entire run free again in one piece after
`freeFileStorage`+`Flush` (via a fresh `Bitmap.FindFree` for the whole
run's size) proves every last block was returned, not just some of them.

### 2. `volume`: `Directory.Remove`

**Depends on:** nothing (independent of subtask 1; parallel work).

`func (d *Directory) Remove(name string, version uint16, bm *Bitmap, ib
*IndexBitmap) error` — the mirror image of `Insert`
(`volume/directory.go`): `List()` the full entry set, filter out the one
matching `(name, version)` (case-insensitive name match, exact version —
`version 0` is not accepted here, unlike `Lookup`'s "0 means highest"
convenience, since silently guessing which version to remove is exactly
the kind of ambiguity `DELETE`'s own "version is always required" rule
(see Goals) exists to avoid at the CLI layer; erroring if no such entry
exists), re-`packDirectoryBlocks` the remaining set, rewrite however many
blocks that takes (fewer than before, generally — see [Non-goals](#non-goals-explicitly-out-of-scope-for-phase-3)
on why the directory's own block *allocation* never shrinks even though
its logical content does), and call `recordUsedBlocks` with the new,
possibly-smaller count.

One real wrinkle worth flagging up front: `recordUsedBlocks`
(`volume/directory.go`) sets `HighWaterMark = usedBlocks + 1` on every
call, and `Insert`/`Extend` both currently only ever move it forward. A
`Remove` that reduces the block count moves it *backward* — which is
correct here (those trailing blocks genuinely aren't directory content
anymore, so `List`'s `UsedBlocks()`-bounded walk correctly stops seeing
them) but is a new case worth its own explicit regression test, since nowhere
else in the codebase does `HighWaterMark` ever legitimately shrink.

**Tests:** remove the only entry in a single-block directory (result:
one empty-but-valid block, matching `packDirectoryBlocks`' existing
"empty entries still produces one block" fallback); remove one entry from
a name with multiple versions, confirming siblings survive; remove an
entry whose block, once it's gone, allows the whole directory to shrink
from N blocks' worth of content to fewer — confirming `HighWaterMark`
moves backward correctly and a subsequent `List()` doesn't see stale
trailing content; removing a nonexistent `(name, version)` errors without
modifying anything on disk.

**Shipped**, as `volume/directory.go`'s `func (d *Directory) Remove(name
string, version uint16, bm *Bitmap, ib *IndexBitmap) error`, matching the
sketch above exactly — including the `bm`/`ib` parameters, which `Remove`
never actually ends up using: removing entries from an already-packed
layout can only need the same or fewer blocks than before (a strict
subset of the same content, packed by the same greedy, order-preserving
`packDirectoryBlocks` algorithm `Insert` already used to lay it out),
never more, so the `Extend`-if-short-on-space branch `Insert` needs has no
equivalent here. They're kept in the signature anyway, both to mirror
`Insert`'s shape and as a hedge against that invariant ever changing.

Tests (`volume/directory_test.go`): `TestDirectoryRemoveOnlyEntry`,
`TestDirectoryRemoveOneOfSeveralVersions` (including a case-insensitive
name match, confirming `Remove` follows the same convention as
`Lookup`/`List`), `TestDirectoryRemoveShrinksUsedBlocks` (inserts 56
entries to force a second block, then removes every entry but one,
confirming `Blocks()` — the directory's allocation — stays put per this
phase's own directory-shrink-back non-goal, while `List()`, both on the
live `Directory` and after an independent reopen, correctly stops seeing
the second block's now-stale physical content), and
`TestDirectoryRemoveNonexistentEntryErrors` (a wrong version, a wrong
name, and version 0, each confirmed to leave the directory's on-disk
content byte-for-byte unchanged).

### 3. `volume`: `DeleteFile`

**Depends on:** 1, 2.

Ties the two together in the safe order described in [Ordering for
safety](#ordering-for-safety-without-journaling): given a directory and a
`(name, version)` (or an already-resolved `ondisk.DirEntry`/`Fid` — exact
signature TBD when this subtask starts, likely something taking whatever
`filespec.Match` already gives the CLI layer), read the primary header via
the existing `readFileHeaderViaIndex` path, call `Directory.Remove` first,
then subtask 1's chain-deallocation primitive. Returns whatever error
either step produced; a failure in the second step after the first
succeeded is reported but not rolled back (see the design section above).

**Tests:** end-to-end delete of a small file and of a multi-segment file,
each confirmed via: the directory no longer lists it (`Directory.List`),
its former header slot(s) are reusable (`IndexBitmap.FindFreeSlot`
eventually returns them again), its former extents are reusable
(`Bitmap.FindFree` can find them again), all after an explicit `Flush` of
both caches. Also: deleting one version of a multi-version name leaves
the others completely intact and independently readable via `OpenFID` —
the regression case for this API accidentally sharing state across
versions of the same name that it shouldn't.

**Shipped**, as `volume/delete.go`'s `func DeleteFile(dir *Directory, name
string, version uint16, bm *Bitmap, ib *IndexBitmap) error`, matching the
sketch above — a plain package function rather than a `*Volume` method,
since (per the single-device write-path scope this phase inherits
unchanged from Phase 2) `dir.Device` alone is always the right device to
resolve the target file's header against; there's no `vol` receiver to
thread a multi-device lookup through here that `freeFileStorage` doesn't
already avoid the same way.

Two safety checks were added beyond the original sketch, both caught
during review rather than planned up front:

- **The volume's master file directory can never be deleted**, checked
  purely from the resolved entry's Fid (`ondisk.MasterFileDirectoryFid`)
  before anything else is even read — there's no scenario where deleting
  it is recoverable or meaningful, since every other piece of this
  project (`Volume.OpenDirectory`, `filespec.ResolveDirectory`'s root,
  a future `ANALYZE/DISK`'s own walk) assumes it's always there to start
  from.
- **A directory file can only be deleted while empty.** If the resolved
  header has `FchDirectory` set, `DeleteFile` resolves its full data
  (`buildFile`) and `List`s its entries, refusing outright if even one is
  found — deleting a non-empty directory would orphan whatever it still
  names, since nothing would ever reach those entries again through an
  ordinary directory walk once the one entry leading here is gone. Both
  checks run before `Directory.Remove` is ever called, so a rejected
  delete leaves the volume provably untouched, not just "safe by
  accident."

Tests (`volume/delete_test.go`) cover: version 0 rejected without touching
`dir`/`bm`/`ib` at all; end-to-end delete of a single-segment and a
multi-segment file (the latter forcing a real extension segment the same
way subtask 1's own tests do), each confirmed via `Directory.List`,
`IndexBitmap.FindFreeSlot`, and `Bitmap.FindFree` after an explicit
`Flush` of both caches — accounting for the one cluster the target
directory's own first `Insert` permanently consumes growing it from 0 to
1 block, which `Directory.Remove` never gives back (this phase's own
directory-shrink-back non-goal); deleting one version of a multi-version
name leaving its siblings' directory entries and data completely intact;
deleting a nonexistent name/version erroring without modifying the
directory; a non-empty directory's deletion rejected with both the parent
directory's entry and the subdirectory's own content left untouched; an
empty directory deleted exactly like any other file; and the master file
directory's deletion rejected purely from its Fid, before any header is
even read.

### 4. `cmd/ods2`: `DELETE` command

**Depends on:** 3.

New `delete.go`, `Command{Name: "delete", ..., Run: cmdDelete}`. Parses its
one argument via `filespec.Parse`, **rejects it outright if no version was
given** (`spec.Version == ""` — the exact check TBD once this subtask
starts, since `filespec.Spec.Version` is deliberately raw, unresolved
text per `filespec/spec.go`), resolves matches via `filespec.Glob` (which
already understands `;n` and `;*` via its existing `versionSelector`
machinery — no changes needed there), and calls subtask 3's `DeleteFile`
once per match. Flushes the target device's `Bitmap`/`IndexBitmap` once at
the end (see [Flush strategy](#flush-strategy)), after every match has
been processed, so a `DELETE *.TXT;3` that successfully deletes some
matches before failing on a later one still keeps whatever it already
freed rather than discarding it.

**Tests:** `DELETE FOO.TXT;3` on a mounted, writable test volume, confirmed
by a following `DIRECTORY` no longer listing it and the file's storage
being reusable by a subsequent `COPY`; `DELETE FOO.TXT;*` removing every
version of a name in one call; `DELETE *.TXT;2` across multiple matching
names; `DELETE FOO.TXT` (no version) and `DELETE FOO.TXT;` rejected with a
clear error before anything is touched; deleting a nonexistent file/version
errors cleanly. Session-level test (`cmd/ods2/main_test.go`'s existing
pattern of copying a real writable test image into a temp directory before
mutating it) exercising `DELETE` against `testdata/rq0-ra92.dsk`'s copy, the
same real-volume validation approach PHASE-02.md leaned on throughout.

### 5. `volume`: version-limit resolution + create-time enforcement

**Depends on:** 3 (reuses `DeleteFile` to actually remove excess old
versions).

Two changes to `CreateFile` (`volume/writefile.go`), per [Version-limit
design](#version-limit-design):

1. Before building the new header, resolve the `VersionLimit` the new
   version will actually carry: if this is the name's first version
   (`NextVersion` returned 1), it's `dir.Header.RecordAttributes
   .VersionLimit` (the directory's current default); otherwise it's
   whatever `VersionLimit` the name's immediately-previous highest version
   already had (one extra header read, of a header `NextVersion`'s own
   `List()` call already had to look at — worth checking whether `List()`
   or `NextVersion()` should be extended to hand this back directly rather
   than requiring a second read, once this subtask is in progress).
   Whatever `VersionLimit` the caller's own `recAttr` argument carries is
   ignored for this field specifically.
2. After `Insert` succeeds (the new version now genuinely exists), if the
   resolved limit is nonzero and the name now has more versions than that
   limit allows, delete the oldest excess versions (via subtask 3) down to
   exactly the limit. "Oldest" and "excess" both come from a fresh
   `List()` filtered to this name, sorted by version ascending, dropping
   everything but the highest `limit` entries.

**Tests:** creating successive versions of a name in a directory with a
nonzero default confirms each new version's own `VersionLimit` matches the
directory's (first version) or the previous version's (later ones);
creating past the limit auto-deletes the correct (oldest) version(s), and
never more than needed; a limit of 0 (directory default also 0) never
deletes anything, matching today's Phase 2 behavior with zero observable
change for every existing caller that never sets a limit; changing the
directory's default between creating two different, unrelated names
produces the expected different limits on each (proving the "captured
once" rule from the design section, not a live re-read).

### 6. `volume`: `SetVersionLimit`

**Depends on:** nothing new beyond Phase 2's `writeHeader`/`existingAreas`
(same pattern `CloseWithFinalByte` already uses to rewrite one field of an
already-on-disk header).

`func SetVersionLimit(f *File, limit uint16) error` (or equivalent; exact
signature TBD) — reads `f.Header`, reconstructs its existing IDENT/map
content via `existingAreas`, sets `RecordAttributes.VersionLimit = limit`,
re-encodes and rewrites the header immediately (no deferred flush — this
is a header write, not a bitmap mutation). Works identically whether `f`
is a plain file or a directory file, per [Version-limit
design](#version-limit-design) point 1 — no special-casing needed.

**Tests:** setting a plain file's limit and confirming it round-trips via a
fresh `OpenFID`; setting a directory's limit and confirming a
subsequently-created new name in it inherits that value (an integration
check with subtask 5, once both exist); setting it back to 0 restores
"unlimited" behavior.

### 7. `cmd/ods2`: `SET FILE/VERSION_LIMIT=n`

**Depends on:** 6.

Extends `cmdSet` (`cmd/ods2/internal/session/setshow.go`) with a `file`
sub-verb alongside its existing `default` one, and adds a `"version_limit"`
qualifier (or `"limit"` — exact spelling TBD; `/VERSION_LIMIT` is more
explicit and matches this doc's own naming throughout, at the cost of being
longer to type) to the `set` command's qualifier list. `SET
FILE/VERSION_LIMIT=n file-spec` parses `file-spec` and resolves it via
`filespec.Glob` (supporting wildcards, so one command can set a limit
across several names or, degenerately, target a single directory spec),
calling subtask 6's `SetVersionLimit` on each match. Requires `n` to parse
as a `uint16` (matching `RecAttr.VersionLimit`'s own on-disk width),
following `initialize.go`'s existing `/CLUSTER` value-qualifier as the
direct precedent for parsing and error-reporting an invalid value.

**Tests:** `SET FILE/VERSION_LIMIT=3 FOO.TXT` followed by `SHOW` or a
direct API check confirming the value stuck; targeting a directory spec
(`[SUBDIR]` or similar — exact VMS syntax for naming a directory's own
file TBD, matching however `ANALYZE/DISK` or other commands already name a
directory-as-a-file if any precedent exists, otherwise established fresh
here); an invalid (non-numeric, or out-of-range for `uint16`) value
rejected with a clear error; a wildcard spec setting the limit on every
match.

### 8. `volume`: `PurgeVersions`

**Depends on:** 3 (reuses `DeleteFile`).

`func PurgeVersions(dir *Directory, name string, keep uint16, bm *Bitmap,
ib *IndexBitmap) error` (or equivalent) — lists every version of `name`
in `dir`, and if there are more than `keep`, deletes the oldest excess
ones via subtask 3, exactly like subtask 5's create-time enforcement logic
(worth checking, once both exist, whether the "which versions are excess"
selection logic should be factored into one shared helper both subtasks 5
and 8 call, rather than writing it twice). `keep == 0` is rejected as
invalid (VMS's own `PURGE` has no "delete every version" meaning for
`/LIMIT=0`; deleting every version of a name is what `DELETE name;*`
subtask 4 already provides, cleanly, with no need for `PurgeVersions` to
also support the degenerate case).

**Tests:** a name with more versions than `keep` is trimmed to exactly
`keep`, always removing the oldest; a name with fewer versions than `keep`
(or exactly `keep`) is left untouched, including confirming no
unnecessary writes/flushes happen for it; `keep == 0` rejected.

### 9. `cmd/ods2`: `PURGE` command

**Depends on:** 8.

New `purge.go`, `Command{Name: "purge", ..., Qualifiers: []string{"limit"},
Run: cmdPurge}`. Parses its one argument via `filespec.Parse`/
`filespec.Glob` (defaulting Name/Type to `*` the same way `DIRECTORY`
does when nothing more specific is given, and — unlike `DELETE` — a
version is never required or even meaningful here, since `PURGE` always
means "consider every version of each matched name"), groups the matches
by distinct `(Dirs, Name, Type)` (a single name can appear many times in
`Glob`'s flat match list, once per surviving version), and calls subtask
8's `PurgeVersions` once per distinct name with `/LIMIT`'s value (parsed
the same `strconv.ParseUint` way subtask 7 and `initialize.go`'s
`/CLUSTER` already do) or a default of 1 if the qualifier is absent.
Flushes once at the end, per [Flush strategy](#flush-strategy).

**Tests:** `PURGE *.TXT` (default `/LIMIT=1`) leaves exactly the highest
version of every matching `.TXT` name; `PURGE FOO.TXT/LIMIT=2` keeps the
two highest versions of just that one name; a glob matching several
distinct names purges each independently (a name with only 1 version to
begin with is correctly left alone under the default limit, not treated
as an error); `/LIMIT=0` rejected (per subtask 8); session-level test
against a copy of `testdata/rq0-ra92.dsk`, same real-volume validation
approach as subtask 4.
