# ODS2 command reference

This is the full reference for `ods2`'s commands — both the interactive
REPL and the equivalent one-shot command-line form. `HELP` inside the
tool only prints a bare list of command names; this document describes
what each one actually does, its arguments, its qualifiers, and how file
specifications and wildcards work.

## Running the tool

With no arguments, `ods2` starts an interactive prompt:

```text
$ ods2
ODS2> mount DUA0 myvolume.iso
%MOUNT-I-MOUNTED, Volume MYVOLUME mounted on DUA0
ODS2> dir *.txt
...
ODS2> exit
```

Five commands — `DIRECTORY`, `TYPE`, `COPY`, `SEARCH`, `DIFFERENCE` — are
also available as one-shot subcommands, for scripting. The one-shot form
always takes the image to mount as its first argument, then the rest of
an ordinary command line exactly as you'd type it at the `ODS2>` prompt:

```text
$ ods2 dir myvolume.iso *.txt
$ ods2 type myvolume.iso README.TXT
$ ods2 copy myvolume.iso "*.TXT" ./extracted/
$ ods2 search myvolume.iso "*.TXT" "TODO"
$ ods2 difference myvolume.iso NOTES.TXT ./local-notes.txt
```

Each one-shot invocation mounts the image **read-only**, runs exactly one
command against it, and exits — it does not preserve state between
invocations the way the REPL's `mount`/`set default` do across multiple
commands, and it has no way to pass `/WRITE` to that mount.

`MOUNT`, `DISMOUNT`, `INITIALIZE`, `ANALYZE/DISK`, `SET`, `SHOW`, and
`HELP` have **no one-shot subcommand form** — `ods2 mount ...` and
`ods2 analyze ...` are not recognized at the shell. Anything that needs
one of these (write access, building a new volume, checking bitmap
consistency, or a multi-command script that carries state — like `SET
DEFAULT` — across several commands) needs the interactive `ODS2>` prompt,
or an equivalent script of command lines piped in on standard input:

```text
$ ods2 <<'EOF'
initialize myvolume.dsk 4000 MYVOL
mount DUA0 myvolume.dsk /write
analyze DUA0 /disk
EOF
```

Non-interactive standard input (a pipe, a redirected file, or a heredoc
like the one above) is read one command per line, exactly as if it had
been typed at the prompt, but without the interactive-only line-editing
and history support `ODS2>` gets from a real terminal.

**A shell quoting note:** file specifications containing `[` and `]`
(directory brackets) or `*` are meaningful to most shells too. Quote them
(`"[FOO]*.TXT"`) when using the one-shot form so your shell passes them
through unchanged instead of trying to glob-expand or interpret them
itself.

## Command abbreviation

Like VMS DCL, every command name can be abbreviated to a unique prefix.
Each command below lists its **minimum abbreviation** — the fewest
letters you can type and still have it recognized. Typing more of the
name (up to the full word) always works too. If your abbreviation could
match more than one command, it's rejected as ambiguous.

| Command | Minimum abbreviation |
|---|---|
| `copy` | `copy` (4) |
| `difference` | `diff` (4) |
| `directory` | `dir` (3) |
| `dismount` | `dis` (3) |
| `exit` | `ex` (2) |
| `help` | `he` (2) |
| `initialize` | `init` (4) |
| `mount` | `mou` (3) |
| `quit` | `qu` (2) |
| `search` | `sea` (3) |
| `set` | `set` (3) |
| `show` | `sh` (2) |
| `type` | `typ` (3) |

`SET`'s and `SHOW`'s own sub-verbs (`DEFAULT`, `TIME`) can similarly be
abbreviated down to 2 characters (`SET DEF`, `SHO TIM`).

## File specifications

Most commands take a **file specification** in VMS syntax:

```text
device:[directory.subdirectory]name.type;version
```

Every part is optional. Whatever you leave out is filled in from the
current default device and directory (see `SET DEFAULT`) — typing just
`FOO.TXT` uses your current default device and directory, exactly as if
you'd typed the full spec.

### Directories

- `[FOO.BAR]` — an **absolute** path from the volume's top-level (master
  file) directory, replacing your default directory entirely.
- `[.BAR]` — **relative**: descend from your current default directory
  into subdirectory `BAR`.
- `[-]` — move **up** one level from your current default directory.
- `[-.BAR]` — move up one level, then descend into `BAR`. Each additional
  leading `-` moves up one more level (`[--.BAR]` is up two levels, then
  into `BAR`).
- `[000000]` or `[]` — the top-level (master file) directory itself.

### Wildcards

- `*` matches any run of characters (including none) in a name, type, or
  directory component.
- `%` matches exactly one character.
- `...` at the end of a directory spec means "this directory, and every
  subdirectory beneath it, to any depth" — e.g. `[SYS...]*.EXE` finds
  every `.EXE` file under `[SYS]` no matter how deeply nested.

Wildcards work in names, types, and individual directory components
(`[SYS*]*.*` matches any top-level directory starting with `SYS`).

### Versions

VMS keeps every version of a file that hasn't been deleted, so a spec's
version field selects among them:

- No version at all (`FOO.TXT`) — the single **highest** existing version.
- `FOO.TXT;5` — exactly version 5.
- `FOO.TXT;*` — **every** surviving version.
- `FOO.TXT;-1` — the highest version (same as leaving it off).
- `FOO.TXT;-2` — the **second**-highest version, and so on.

## Commands

### MOUNT

```text
MOUNT device[,device...] container[,container...] [/WRITE]
```

Opens one or more disk image files and makes them available under the
given device name(s). `device` is the VMS-style name (e.g. `DUA0`) the
volume is mounted under; `container` is a **host file path** to the
backing image (an ISO, a plain block dump, or a raw CD-ROM sector dump —
both are detected automatically), not VMS syntax. Both must always be
given — there's no VMS-style device registry here that already knows
which file a given device name backs.

Give a comma-separated list for both `device` and `container` to mount a
**volume set** (several member disks presented as one logical volume),
pairing them up positionally, in the same order VMS's own MOUNT command
expects. The two lists must be the same length — **unless** you give a
single `device` name and several `container` paths, in which case the
device name is expanded into one name per container, by synthesizing
successive unit numbers onto it:

- If the device name already ends in a unit number (e.g. `DUA1`), that
  number is the **starting** unit: `MOUNT DUA1 foo.dsk,bar.dsk` mounts
  `foo.dsk` as `DUA1` and `bar.dsk` as `DUA2`.
- If it doesn't (e.g. `DUA`), unit numbers start at 0: `MOUNT DUA
  foo.dsk,bar.dsk` mounts `foo.dsk` as `DUA0` and `bar.dsk` as `DUA1`.

The **first** device name you mount becomes your current default device
(with directory `[000000]`) if you haven't set one yet.

- `/WRITE` — mounts the volume for write access instead of the default
  read-only mode, required before any write-path operation (`INITIALIZE`
  already writes directly to an image without going through `MOUNT`, but
  future write commands will need this). Fails with a clear error if the
  image can't be written to — a raw CD-ROM sector dump, in particular, can
  never be mounted `/WRITE`.

```text
ODS2> mount DUA0 myvolume.iso
%MOUNT-I-MOUNTED, Volume MYVOLUME mounted on DUA0
```

Mounting a volume set with a synthesized device name:

```text
ODS2> mount DUA1 disk1.dsk,disk2.dsk
%MOUNT-I-MOUNTED, Volume MYSET mounted on DUA1
%MOUNT-I-MOUNTED, Volume MYSET mounted on DUA2
```

### DISMOUNT

```text
DISMOUNT device
```

Closes a mounted volume's underlying image file(s) and forgets it.

### INITIALIZE

```text
INITIALIZE path size-in-blocks [label] [/CLUSTER=n]
```

Creates a new, zero-filled host file of `size-in-blocks` 512-byte blocks
and formats it as a minimal but valid ODS-2 volume: a home block,
`INDEXF.SYS` (with its own storage and header-slot bitmaps), `BITMAP.SYS`,
and the rest of the nine reserved bookkeeping files (`BADBLK.SYS`,
`000000.DIR`, `CORIMG.SYS`, `VOLSET.SYS`, `CONTIN.SYS`, `BACKUP.SYS`,
`BADLOG.SYS`), all listed by name in a freshly built master file
directory.

`label` defaults to `NONAME` if omitted.

- `/CLUSTER=n` — the volume's allocation cluster size, in blocks. Defaults
  to 1.

Unlike every other command here, INITIALIZE doesn't mount the volume it
just built — matching real VMS's own INITIALIZE, which formats a device
without mounting it. Follow it with `MOUNT device path`. It's also not
available as a one-shot subcommand (`ods2 initialize ...`): the one-shot
form's convention of mounting `path` before running the command doesn't
apply to a file that doesn't exist yet.

```text
ODS2> initialize myvolume.dsk 4000 MYVOL
%INITIALIZE-I-DONE, Volume MYVOL initialized on myvolume.dsk (4000 blocks, 9 reserved files)
ODS2> mount DUA0 myvolume.dsk
%MOUNT-I-MOUNTED, Volume MYVOL mounted on DUA0
```

### ANALYZE/DISK

```text
ANALYZE device /DISK [/REPAIR]
```

Checks a mounted volume's storage bitmap (`BITMAP.SYS`) for consistency:
it walks every in-use file on `device`, works out which blocks its own
headers say it occupies, and compares that against what `BITMAP.SYS`
actually records. Two kinds of discrepancy are reported: a block the
bitmap marks free that some file actually uses (a corruption risk — a
future allocation could silently overwrite that file's data), and a block
the bitmap marks allocated that no file actually claims (merely
reclaimable space, not dangerous).

`/DISK` is required — it's the only structure type this command checks
(real VMS's `ANALYZE` also supports unrelated modes like `/RMS_FILE` this
project doesn't implement). `device` must already be mounted (see
`MOUNT`); this command doesn't take a host path directly.

- `/REPAIR` — in addition to reporting, rewrites `BITMAP.SYS` to match the
  computed-correct state. Requires `device` to be mounted `/WRITE`.

Only a single-device volume is supported — `ANALYZE/DISK` against a
mounted volume set (several devices mounted together) fails with a clear
error.

```text
ODS2> mount DUA0 myvolume.dsk /write
%MOUNT-I-MOUNTED, Volume MYVOL mounted on DUA0
ODS2> analyze DUA0 /disk
%ANALYZE-W-DISCREP, 1 discrepancy(ies) found (400 cluster(s) examined)
  cluster 57 (LBN 57-57) is marked allocated but is not used by any file
ODS2> analyze DUA0 /disk /repair
%ANALYZE-W-DISCREP, 1 discrepancy(ies) found and repaired (400 cluster(s) examined)
  cluster 57 (LBN 57-57) is marked allocated but is not used by any file
ODS2> analyze DUA0 /disk
%ANALYZE-I-CLEAN, no discrepancies found (400 cluster(s) examined)
```

### DIRECTORY (DIR)

```text
DIRECTORY [file-spec] [/FULL] [/FILE] [/SIZE] [/DATE]
```

Lists files matching `file-spec` (default `*.*` — everything in your
current default directory) and prints a total file/block count at the
end. If the spec's directory is wildcarded or uses `...`, matches from
different directories are printed under their own `Directory [...]:`
headers.

- `/FILE` — also show each file's file ID, `(number,sequence,rvn)`.
- `/SIZE` — also show each file's size in blocks, and include a
  total-blocks count in the summary line.
- `/DATE` — also show each file's revision date.
- `/FULL` — shorthand for `/FILE /SIZE /DATE` together, plus the file's
  record format.

```text
ODS2> dir [SYS]*.EXE /size
Directory DUA0:[SYS]

FOO.EXE;1                          42
BAR.EXE;3                          17

Total of 2 file(s), 59 block(s).
```

### TYPE

```text
TYPE file-spec
```

Writes one file's content to the screen as text. Carriage control in
VFC-format files is expanded into ordinary leading/trailing characters
(blank lines, form feeds, etc.) the way it would print on a real
terminal.

Unlike `DIRECTORY` or `COPY`, **`file-spec` must resolve to exactly one
file** — matching VMS's own TYPE command, which accepts no wildcards at
all. A name with several versions still works fine with no version given
(it types the highest one, per the usual version rule above); a spec
whose name or type wildcard matches more than one distinct file name is
rejected instead of picking one arbitrarily.

### COPY

```text
COPY source-spec destination [/QUIET] [/VERBOSE] [/TEST] [/BINARY] [/TIME]
     [/IGNORE] [/DIRS] [/STREAM] [/VFC] [/CRLF] [/LF] [/HOST]
```

Copies one file (or, without `/HOST`, one or more files) onto a
destination. Without `/HOST` (the original, and still the most common
form), `source-spec` always names one or more files on an already-mounted
volume, and `destination` is usually a **host path**, but may instead be
VMS syntax (`device:[dir]name.type`) naming a location on a *different*
(or the same) volume that's mounted `/WRITE` — in which case the copy goes
volume-to-volume instead of onto the host filesystem. A `destination`
string is only ever treated as VMS syntax if the text before its first
`:` actually names a currently mounted device; anything else (including
every ordinary host path, which typically has no `:` at all) is a host
path exactly as before.

With `/HOST`, `source-spec` is flipped instead: it names a single plain
**host file** (never a file already on a mounted volume, and never a
wildcard), and `destination` must be VMS syntax naming a location on a
volume mounted `/WRITE` — this is how a file gets *onto* a volume in the
first place. `/HOST` needs its own explicit qualifier because a bare name
like `go.sum` is a syntactically valid (if possibly nonexistent) VMS file
spec too — without `/HOST`, `copy go.sum DUA1:*.*` looks for a file named
`go.sum` on your *current default volume*, not the host file of that name
in your shell's working directory. A `/HOST` copy onto a plain host
`destination` (rather than a mounted volume) is rejected outright — that
direction is what your shell's own file-copy tools are for.

For a **host** `destination`, it can be:

- An existing **directory** — each file is written there under its own
  `name.type;version`.
- A path whose base name contains `*` — VMS-style wildcard substitution.
  Copying `*.TXT` to `*.OLD` keeps each file's own name but changes its
  type to `.OLD`; copying `*.TXT` to `README.*` keeps each file's own
  type but renames it to `README`.
- An exact literal path — only valid when `source-spec` matches exactly
  one file (copying several files to one literal name is rejected up
  front, rather than silently letting each one overwrite the last).

For a **volume** `destination`, it can be:

- `device:` or `device:[dir]` — naming no file of its own — each matched
  file is created there under its own name/type (version numbers are
  always auto-assigned; see below).
- A name/type containing `*` or `%` — the same wildcard substitution as
  the host-path case, applied component-by-component.
- An exact literal `device:[dir]name.type` — only valid when `source-spec`
  matches exactly one file, same as the host case.

A volume destination's version number is never taken from the text typed
(even if one was given): the file is always created under the next
version after whatever, if anything, already exists there under that
name/type, the same auto-versioning `INITIALIZE`d volumes and `CREATE`
give every new file. Only `/QUIET`, `/VERBOSE`, `/TEST`, and `/BINARY`
apply to a volume destination — `/TIME` (no host modification time to
preserve), `/IGNORE`, `/DIRS`, and the line-ending qualifiers are
host-file-format concerns with no volume-side equivalent (a matched
`.DIR` source entry is always skipped for a volume destination, the same
as without `/DIRS`). Without `/BINARY`, the destination file is always
created as a Stream (`Stream_LF`) file, whatever record format the source
file itself used — the simplest text convention to target without
negotiating a full record-format/carriage-control choice on the write
side.

`/HOST` shares that same restricted qualifier set for the same reason,
since its destination is always a volume too: only `/QUIET`, `/VERBOSE`,
`/TEST`, and `/BINARY` apply. Without `/BINARY`, the destination is
created `Stream_LF` and the host file's text is copied in, one line at a
time, with any `\r\n` line endings normalized to plain `\n` (a bare `\r`
with no following `\n` is left alone) — the host file doesn't need a
trailing newline on its last line for that line to still come across
intact. With `/BINARY`, the destination is created `Undefined` and the
host file's exact bytes are copied through unchanged. The destination
name/type comes from `destSpec`'s own name/type if it gave one (applying
the same `*`/`%` wildcard-substitution rule as any other volume
destination, and — like any other VMS name typed directly into this
tool — used exactly as typed, whatever case that is), or otherwise from
the host file's own base name, **upper-cased** — e.g. copying `go.sum` to
`DUA1:*.*` creates `DUA1:[dir]GO.SUM`, not `DUA1:[dir]go.sum`. A host
file's name is ordinary host-filesystem text with no VMS convention
behind its case at all; upper-casing it to look like a normal VMS name is
the equivalent of what real VMS's own DCL does to unquoted command-line
text automatically.

Qualifiers:

- `/QUIET` — don't print the `%COPY-S-COPIED` confirmation line per file.
- `/VERBOSE` — print an extra line before each file naming source and
  destination.
- `/TEST` — don't copy anything; just report what would be copied.
- `/BINARY` — copy the file's exact raw bytes with no record
  interpretation at all, instead of the default text rendering.
- `/TIME` — set the copied file's modification time to the source file's
  own revision date, instead of the current time.
- `/IGNORE` — if a corrupt/inconsistent record is found while copying in
  the default text mode, don't fail: restart the file from scratch as a
  raw binary copy instead, recovering its bytes losslessly (without the
  interrupted portion's text formatting).
- `/DIRS` — when `source-spec` matches files in more than one directory
  (a wildcarded directory component, or `...`), mirror each file's source
  subdirectory path under `destination` instead of flattening everything
  into one directory, creating host directories as needed. Also causes
  matched `.DIR` entries themselves to be materialized as host
  directories; without `/DIRS`, `.DIR` entries are skipped entirely
  rather than copied as if they were ordinary files.
- `/STREAM` — for a Stream-format source file, copy its exact original
  bytes instead of scanning for line delimiters and re-writing them with
  this tool's own line-ending convention. Without it, a Stream file's
  line endings are normalized the same way `/BINARY` would bypass
  entirely, but only stream-format files are affected.
- `/VFC` — accepted for compatibility; has no effect. Unlike the original
  VMS tool (where VFC interpretation is opt-in), this tool's default text
  mode always expands a VFC file's carriage control, the same as `TYPE`
  does, so there's no separate "raw" mode to opt into.
- `/CRLF` — use `\r\n` line endings in text-mode output instead of the
  default `\n`. Mutually exclusive with `/LF`.
- `/LF` — use `\n` line endings (the default; mainly useful to say so
  explicitly). Mutually exclusive with `/CRLF`.
- `/HOST` — `source-spec` names a plain host file instead of a file on a
  mounted volume; see above. Requires a volume `destination`.

```text
ODS2> copy *.txt ./extracted/ /verbose
%COPY-I-COPYING, copying FOO.TXT;1 to ./extracted/FOO.TXT;1
%COPY-S-COPIED, FOO.TXT;1 copied to ./extracted/FOO.TXT;1
```

Copying onto a volume mounted `/WRITE` (`DUA1:` here) instead of the host
filesystem:

```text
ODS2> mount dua1 scratch.dsk /write
%MOUNT-I-MOUNTED, Volume SCRATCH mounted on DUA1
ODS2> copy foo.txt DUA1:*.* /verbose
%COPY-I-COPYING, copying FOO.TXT;1 to DUA1:[000000]FOO.TXT
%COPY-S-COPIED, FOO.TXT;1 copied to DUA1:[000000]FOO.TXT;1
```

Copying a plain host file *onto* a volume mounted `/WRITE` with `/HOST`
(note the reversed direction — the volume is now the destination, not the
source):

```text
ODS2> mount dua1 scratch.dsk /write
%MOUNT-I-MOUNTED, Volume SCRATCH mounted on DUA1
ODS2> copy go.sum DUA1:*.* /verbose /host
%COPY-I-COPYING, copying go.sum to DUA1:[000000]GO.SUM
%COPY-S-COPIED, go.sum copied to DUA1:[000000]GO.SUM;1
```

**A `/BINARY` caveat:** a file `/BINARY` creates (in either write
direction) is stored `Undefined`-format. `TYPE`, and a plain (non-`/BINARY`)
`COPY` reading it back, currently misreads an `Undefined`-format file as
if it were fixed-length records the width of one block — harmless for a
file whose length happens to be an exact multiple of 512 bytes, but it
garbles (and eventually errors on) one that isn't. Read a `/BINARY` file
back with `COPY ... /BINARY` (to a host path), not `TYPE`, until this is
fixed.

### SEARCH

```text
SEARCH file-spec search-string
```

A simple case-insensitive substring search (like `grep`) across every
record of every file `file-spec` matches. Prints each matching file's
name once, followed by its matching lines.

```text
ODS2> search *.txt TODO
NOTES.TXT;1
TODO: finish this
```

### DIFFERENCE

```text
DIFFERENCE file-spec local-file
```

A simple line-by-line comparison between one file on the volume and a
plain file on the host filesystem. `file-spec` must match exactly one
file. Reports each line number where the two files disagree (including a
length mismatch, once one side runs out of lines the other still has).
This is a basic positional comparison, not a minimal-edit-script diff —
it doesn't try to realign after an inserted or deleted line.

### DELETE

```text
DELETE file-spec
```

Deletes one or more files: reclaims every header slot and data extent
their storage occupies and removes their directory entry(ies) (see
docs/PHASE-03.md for how this differs from the reference implementation,
whose own delete path is author-acknowledged broken).

`file-spec` must include a version — `;n` for a specific version, or `;*`
for every version of a matching name — and the name/type themselves may
be wildcarded (`*.TXT;3`) to delete the same version across several
names in one command. A bare `DELETE FOO.TXT`, or one ending in an empty
`;` (`DELETE FOO.TXT;`), is rejected before anything is touched: unlike
`DIRECTORY`'s own "no version means the highest one" convenience default,
`DELETE` never guesses which version you meant, specifically so a
mistyped command can't silently delete the newest version of every
matching name.

Requires the target volume to be mounted `/WRITE`. If several files
match, `DELETE` deletes them one at a time and stops at the first
failure; whatever it already deleted before that point stays deleted
(its storage isn't re-allocated just because a later match failed).

```text
ODS2> delete foo.txt;1
%DELETE-S-DELETED, FOO.TXT;1 deleted
ODS2> delete old.txt;*
%DELETE-S-DELETED, OLD.TXT;1 deleted
%DELETE-S-DELETED, OLD.TXT;2 deleted
```

### CREATE DIRECTORY

```text
CREATE DIRECTORY dir-spec [/VERSION=n]
```

Creates a new subdirectory. `dir-spec` is a bracketed directory path whose
**last** component names the subdirectory being created; everything
before it names the parent directory, which must already exist —
`CREATE DIRECTORY [FOO.BAR]` creates `BAR.DIR` inside `[FOO]`, the same
way real VMS's own `CREATE/DIRECTORY` works. `dir-spec` may be written
relative to your current default (`[.BAR]`) the same as any other
directory spec (see **Directories** above); it's an error for it to name
a file (a trailing name/type/version, or a `...` recursive suffix) rather
than a bare directory path.

- `/VERSION=n` — sets the new directory's own default version limit
  (`RecordAttributes.VersionLimit` — see `docs/PHASE-03.md`'s
  "Version-limit design"), the value a name created directly inside it
  later inherits if it doesn't specify its own limit. Without `/VERSION`,
  the new directory inherits its **parent's** current version limit at
  the moment of creation — a one-time snapshot, not a live link back to
  the parent.

```text
ODS2> create directory [PROJECTS]
%CREATE-S-CREATED, DUA0:[000000]PROJECTS.DIR;1 created
ODS2> create directory [PROJECTS.SCRATCH] /version=1
%CREATE-S-CREATED, DUA0:[PROJECTS]SCRATCH.DIR;1 created
```

### SET DEFAULT

```text
SET DEFAULT dir-spec
```

Changes your current default device and/or directory, which every other
command's partial file specs are resolved against. `dir-spec` follows the
same rules as any file spec's directory component (absolute, relative,
`...`, etc. — see **File specifications** above).

```text
ODS2> set default [SYS.MGR]
ODS2> show default
DUA0:[SYS.MGR]
```

### SHOW

```text
SHOW DEFAULT
SHOW TIME
```

- `SHOW DEFAULT` — prints your current default device and directory.
- `SHOW TIME` — prints the current time in VMS format
  (`dd-MMM-yyyy hh:mm:ss.cc`).

### HELP

```text
HELP
```

Lists every recognized command by name, alphabetically. For the full
description of what each one does, its arguments, and its qualifiers,
see this document.

### EXIT / QUIT

Ends the session. Equivalent to each other; either one works.

## Errors

A bad command or a failed command (file not found, ambiguous
abbreviation, wrong number of arguments, unsupported qualifier, ...)
prints a message and returns you to the prompt — it never ends the
session, the same way a DCL error at the VMS `$` prompt doesn't log you
out. Only `EXIT`/`QUIT` end an interactive session.
