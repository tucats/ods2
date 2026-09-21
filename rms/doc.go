// Package rms reads and writes records on an open volume.File according to
// its RMS record format: Fixed, Variable, VFC (variable with fixed
// control), StreamCRLF, StreamLF, StreamCR, or Undefined. Reader reads
// records from an existing file; Writer appends records to one open for
// write (see volume.File.OpenForWrite/volume.Volume.CreateFile).
package rms
