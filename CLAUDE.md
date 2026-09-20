# Project conventions for ods2

Standards for anyone (human or Claude Code) working in this repository.

## Commenting

Comment comprehensively, written for a novice Go developer who must be able
to understand and maintain this code independently. This project is a port
of VAX/VMS Files-11 (ODS-2) concepts that most Go developers won't have
encountered before, so:

- Explain unfamiliar domain concepts inline where they first appear (what a
  FID is, why retrieval pointers are extent-based, the difference between a
  VBN and an LBN, what a "swapped longword" is, etc.) — don't assume VMS/RMS
  background.
- It's fine, and encouraged, to expand beyond what the reference C source's
  own comments say when doing so helps a newcomer understand the on-disk
  format or the reasoning behind a design choice.
- Still avoid restating what a well-named identifier already makes obvious —
  the bar is "explain the unfamiliar," not "narrate every line."

## Testing

Every functional (non-trivial-logic) piece of code gets Go unit tests
(`go test`) added in the same commit that introduces it. Pure scaffolding
(package doc comments, a placeholder `main` with no logic) doesn't need
tests, but as soon as real behavior lands, tests land with it.

## Commit granularity

Commit each complete, testable task or subtask on its own, rather than
batching unrelated work into a single large commit.

## Attribution

Do not include `Co-Authored-By:` or other AI-attribution lines in commit
messages or PR descriptions for this project.

## Remote

Do not push to the remote unless explicitly asked.
