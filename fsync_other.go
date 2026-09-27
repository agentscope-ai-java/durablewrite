//go:build !darwin

package durablewrite

import "os"

// fullFsync flushes f's data to storage using the platform's ordinary
// fsync(2)/FlushFileBuffers. Unlike on macOS (see fsync_darwin.go), Linux's
// fsync(2) is documented to transfer "all modified in-core data ... to the
// disk device", which is the guarantee this package relies on.
func fullFsync(f *os.File) error {
	return f.Sync()
}
