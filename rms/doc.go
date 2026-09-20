// Package rms reads records from an open volume.File according to its RMS
// record format: Fixed, Variable, VFC (variable with fixed control),
// StreamCRLF, StreamLF, StreamCR, or Undefined.
//
// This package implements read access only; there is no record-write
// support.
package rms
