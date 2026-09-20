// Package diskimage provides read access to ODS-2 disk containers, presenting
// them uniformly as a sequence of 512-byte logical blocks regardless of the
// underlying container framing.
//
// Two container kinds are supported:
//
//   - Plain images: a disk or CD-ROM image whose bytes are already the raw
//     ODS-2 volume content back to back (this includes ISO images ripped as
//     plain 2048-byte-sector user data, since 2048 is an exact multiple of
//     512).
//   - Raw CD-ROM sector dumps: a 2352-byte-per-sector dump that still
//     contains the sync pattern, header, and ECC/EDC bytes surrounding each
//     2048-byte payload. These are detected automatically and de-framed on
//     the fly, so no separate conversion pass is required.
package diskimage
