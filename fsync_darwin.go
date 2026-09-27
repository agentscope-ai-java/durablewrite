//go:build darwin

package durablewrite

import (
	"os"
	"syscall"
)

// fullFsync flushes f's data all the way to stable storage on macOS.
//
// Plain fsync(2) — what [os.File.Sync] calls — is documented by Apple's
// fsync(2) man page as NOT flushing the drive's own write cache:
//
//	"For applications that require tighter guarantees about the integrity of
//	their data, Mac OS X provides the F_FULLFSYNC fcntl. The F_FULLFSYNC
//	fcntl asks the drive to flush all buffered data to permanent storage."
//
// fsync(2) still orders writes and is not pointless, but a crash or power
// loss can lose data that only reached the drive's volatile cache. This
// function issues F_FULLFSYNC via the raw fcntl syscall (stdlib syscall
// package constants only — no golang.org/x/sys dependency) so that a
// completed [Writer.Commit] means the bytes are actually on the platter or
// flash, not just handed to the drive.
//
// Some filesystems (certain network shares, some USB/exFAT stacks) return
// ENOTSUP for F_FULLFSYNC. In that case fullFsync falls back to plain
// fsync(2) rather than failing the whole write outright — a materially
// weaker guarantee on that filesystem, which is exactly the tradeoff
// [WithFastDarwinSync] lets a caller choose deliberately instead.
func fullFsync(f *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), uintptr(syscall.F_FULLFSYNC), 0)
	if errno == 0 {
		return nil
	}
	if errno == syscall.ENOTSUP {
		return f.Sync()
	}
	return errno
}
