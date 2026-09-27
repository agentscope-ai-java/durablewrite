// Package durablewrite atomically replaces a file's content and makes the
// replacement durable against a crash or power loss immediately afterward.
//
// # The gap this closes
//
// The well known "atomic write" recipe is: write a temp file, fsync it,
// rename it over the destination. That recipe is incomplete. rename(2) is a
// metadata change in the parent directory, and on POSIX filesystems a
// directory's own metadata is not guaranteed durable until the directory
// itself has been fsynced. Without that second fsync, a crash right after a
// successful rename can leave the directory entry not durably recorded: on
// remount the destination can revert to its old content or disappear
// entirely, even though the rename call itself returned success and the temp
// file was fsynced first.
//
// This is not a theoretical nitpick. Ted Ts'o (the ext2/3/4 maintainer)
// describes exactly why the file's own fsync is mandatory even under
// data=ordered; the same reasoning is why the directory's fsync is mandatory
// for the rename to be durable — data=ordered and write barriers order
// writes relative to each other, they do not force any of them to stable
// storage. See https://lwn.net/Articles/322823/ and the discussion linked
// from Python's atomicwrites package (github.com/untitaker/python-atomicwrites,
// unmaintained since 2020 but the one popular implementation that gets the
// directory fsync right).
//
// This package's README documents which well known Node and Go packages
// were checked and found to skip the directory fsync (write-file-atomic,
// atomically, google/renameio, natefinch/atomic).
//
// # Guarantee, precisely
//
// When [WriteFile], [Do], or [Writer.Commit] return nil, all of the
// following have happened, in order:
//
//  1. The new content was written to a temporary file in the destination's
//     directory (or an explicitly configured directory on the same
//     filesystem).
//  2. That temporary file was fsynced (F_FULLFSYNC on macOS, see below).
//  3. The temporary file was renamed onto the destination path.
//  4. The destination's parent directory was opened and fsynced (F_FULLFSYNC
//     on macOS).
//
// Given that sequence, and given that the underlying filesystem and storage
// device honor fsync/flush as POSIX specifies, the destination path is
// expected to durably contain the new content across a crash or power loss
// that happens after the call returns. That is a correctness argument built
// on cited, documented OS behavior — this package cannot and does not claim
// to have verified it against an actual crash or power-loss event. What was
// verified (see the tests and the examples/ program) is that the syscalls
// happen, and happen in this order.
//
// Two things are outside any userspace library's control and are not, and
// cannot be, guaranteed by this package or any other: a storage device whose
// write cache ignores flush/FUA commands entirely (some cheap USB and SD
// media), and a filesystem whose rename(2) is not itself atomic (notably NFS
// with multiple clients — see the renameio package's own caveat, which
// applies here too).
//
// # macOS: fsync(2) is not enough
//
// On Darwin, the fsync(2) syscall (which is what [os.File.Sync] calls) only
// asks the drive to accept the data; it does not wait for the drive to
// actually persist it, because Apple's fsync(2) man page documents that
// fsync "does not necessarily flush data caches inside disk drives" and
// directs callers who need that to use fcntl(F_FULLFSYNC). This package
// issues F_FULLFSYNC (via the raw fcntl syscall number, stdlib syscall
// package only, see fsync_darwin.go) for both the temporary file and the
// parent directory by default on darwin. F_FULLFSYNC is meaningfully slower
// than fsync — see the benchmarks in the README — so [WithFastDarwinSync] is
// provided to opt back into plain fsync on macOS specifically, when the
// caller has decided that tradeoff is acceptable. It is a no-op on other
// platforms.
//
// # What happens when it fails partway
//
// See the doc comments on [WriteFile], [Writer.Commit] and [ErrDirSyncFailed]
// for exactly what state the filesystem is left in for each failure point,
// and what, if anything, is cleaned up automatically.
package durablewrite
