# Test fixtures

Small ODS-2 volume images used by the automated test suite, in both
supported container framings:

- a plain image (raw ODS-2 volume bytes, 512-byte-block-aligned)
- a raw CD-ROM sector dump (2352 bytes/sector: 12-byte sync + 4-byte header +
  2048 bytes user data + ECC/EDC)

The full-size sample images used for manual/local smoke testing
(`image.iso`, `image2.iso` from the reference C project) are not committed
here due to their size (~200MB each); point an environment variable or flag
at a local copy for those tests instead.
