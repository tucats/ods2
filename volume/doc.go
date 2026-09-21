// Package volume mounts ODS-2 volumes (and volume sets) and provides access
// to the files and directories they contain, built on top of package ondisk
// for on-disk structure decoding and package diskimage for block I/O.
//
// A file's data is located by walking its header's retrieval pointers
// (chasing header extension segments as needed) to map virtual blocks to
// logical blocks on a member device. Write support (see docs/PHASE-02.md)
// is being added incrementally alongside Phase 1's original read-only
// access; not every mutation this package will eventually support exists
// yet.
package volume
