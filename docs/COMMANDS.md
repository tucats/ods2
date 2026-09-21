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
ODS2 (Go port) -- type HELP for a command summary, EXIT to quit.
ODS2> mount myvolume.iso
%MOUNT-I-MOUNTED, Volume MYVOLUME mounted on myvolume.iso
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
mount myvolume.dsk /write
analyze myvolume.dsk /disk
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
MOUNT device[,device...] [label[,label...]] [/WRITE]
```

Opens one or more disk image files and makes them available under the
given device name(s). `device` is a **host file path** to the image (an
ISO, a plain block dump, or a raw CD-ROM sector dump — both are detected
automatically), not VMS syntax.

Give a comma-separated list of devices to mount a **volume set** (several
member disks presented as one logical volume), in the same order VMS's
own MOUNT command expects. Labels are accepted for compatibility but
aren't checked against the volume's actual label.

The **first** device name you mount becomes your current default device
(with directory `[000000]`) if you haven't set one yet.

- `/WRITE` — mounts the volume for write access instead of the default
  read-only mode, required before any write-path operation (`INITIALIZE`
  already writes directly to an image without going through `MOUNT`, but
  future write commands will need this). Fails with a clear error if the
  image can't be written to — a raw CD-ROM sector dump, in particular, can
  never be mounted `/WRITE`.

```text
ODS2> mount DUA0: myvolume.iso
%MOUNT-I-MOUNTED, Volume MYVOLUME mounted on myvolume.iso
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
without mounting it. Follow it with `MOUNT path`. It's also not available
as a one-shot subcommand (`ods2 initialize ...`): the one-shot form's
convention of mounting `path` before running the command doesn't apply to
a file that doesn't exist yet.

```text
ODS2> initialize myvolume.dsk 4000 MYVOL
%INITIALIZE-I-DONE, Volume MYVOL initialized on myvolume.dsk (4000 blocks, 9 reserved files)
ODS2> mount myvolume.dsk
%MOUNT-I-MOUNTED, Volume MYVOL mounted on myvolume.dsk
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
ODS2> mount myvolume.dsk /write
%MOUNT-I-MOUNTED, Volume MYVOL mounted on myvolume.dsk
ODS2> analyze myvolume.dsk /disk
%ANALYZE-W-DISCREP, 1 discrepancy(ies) found (400 cluster(s) examined)
  cluster 57 (LBN 57-57) is marked allocated but is not used by any file
ODS2> analyze myvolume.dsk /disk /repair
%ANALYZE-W-DISCREP, 1 discrepancy(ies) found and repaired (400 cluster(s) examined)
  cluster 57 (LBN 57-57) is marked allocated but is not used by any file
ODS2> analyze myvolume.dsk /disk
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

FOO.EXE;1  (42)
BAR.EXE;3  (17)

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
     [/IGNORE] [/DIRS] [/STREAM] [/VFC] [/CRLF] [/LF]
```

Copies one or more files off the volume. `source-spec` always names files
on an already-mounted volume; `destination` is usually a **host path** (the
original, and still the most common, direction), but may instead be VMS
syntax (`device:[dir]name.type`) naming a location on a *different* (or
the same) volume that's mounted `/WRITE` — in which case the copy goes
volume-to-volume instead of onto the host filesystem. A `destination`
string is only ever treated as VMS syntax if the text before its first
`:` actually names a currently mounted device; anything else (including
every ordinary host path, which typically has no `:` at all) is a host
path exactly as before.

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

```text
ODS2> copy *.txt ./extracted/ /verbose
%COPY-I-COPYING, copying FOO.TXT;1 to ./extracted/FOO.TXT;1
%COPY-S-COPIED, FOO.TXT;1 copied to ./extracted/FOO.TXT;1
```

Copying onto a volume mounted `/WRITE` (`DUA1:` here) instead of the host
filesystem:

```text
ODS2> mount dua1 /write
%MOUNT-I-MOUNTED, Volume SCRATCH mounted on DUA1
ODS2> copy foo.txt DUA1:*.* /verbose
%COPY-I-COPYING, copying FOO.TXT;1 to DUA1:[000000]FOO.TXT
%COPY-S-COPIED, FOO.TXT;1 copied to DUA1:[000000]FOO.TXT;1
```

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
