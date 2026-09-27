# durablewrite

Atomic file writes for Go, with the parent-directory fsync that the popular
libraries — in every language — leave out.

```
[15:04:05.000000] created temp file /tmp/durablewrite-demo-.../config.json.18nk6drj0b8f3
[15:04:05.000000] fsynced temp file /tmp/durablewrite-demo-.../config.json.18nk6drj0b8f3
[15:04:05.000000] renamed .../config.json.18nk6drj0b8f3 to .../config.json
[15:04:05.000000] opened parent directory /tmp/durablewrite-demo-...
[15:04:05.000000] fsynced parent directory /tmp/durablewrite-demo-...
[15:04:05.000000] closed parent directory /tmp/durablewrite-demo-...
```

That's real output from `go run ./examples/durable-demo` (timestamps trimmed
for width; see below for the full run including microseconds).

## The problem

The standard "atomic write" recipe is: write a temp file, fsync it, rename
it over the destination. Almost every popular implementation of that recipe
stops there. It's incomplete.

`rename(2)` is a metadata change in the **parent directory**. On POSIX
filesystems, a directory's own metadata is not guaranteed durable until the
directory itself has been fsynced. Skip that, and a crash or power loss
immediately after a successful rename can leave the rename not durably
recorded — on remount, the destination can revert to its old content or
disappear entirely, despite the temp file having been fsynced and the
rename call having returned success.

This is exactly the reasoning Ted Ts'o (the ext2/3/4 maintainer) gives for
why the *file's own* fsync is mandatory even under `data=ordered` — ordering
guarantees relative write order, not durability of any of them — and the
same reasoning extends one level up, to the directory entry that makes the
renamed file reachable at all.

### The libraries that get this wrong

Checked by fetching their actual source, not from memory:

- **[write-file-atomic@8](https://github.com/npm/write-file-atomic)**
  (used internally by the npm CLI) — fsyncs the temp fd, renames. The
  string `dirname` does not appear in its source at all.
- **[atomically@2.1.1](https://github.com/sindresorhus/atomically)** —
  uses `path.dirname` only to compute where to `mkdir`; fsyncs the temp fd,
  renames, never opens the directory.
- **[google/renameio](https://github.com/google/renameio)** (`v2`, current
  `master`, module `github.com/google/renameio/v2`) — its own
  `CloseAtomicallyReplace` quotes the same Ted Ts'o reasoning above as
  justification for the temp-file fsync it does perform:

  ```go
  // Even on an ordered file system (e.g. ext4 with data=ordered) or file
  // systems with write barriers, we cannot skip the fsync(2) call as per
  // Theodore Ts'o (ext2/3/4 lead developer): ...
  if err := t.Sync(); err != nil {
      return err
  }
  t.closed = true
  if err := t.File.Close(); err != nil {
      return err
  }
  ...
  if err := os.Rename(t.Name(), t.path); err != nil {
      return err
  }
  t.done = true
  return nil
  ```

  That's the whole function. `filepath.Dir` appears three times in the
  repo, all to compute where to create a temp file or symlink — never to
  open the destination's directory. `dirname` doesn't appear at all.
- **[natefinch/atomic](https://github.com/natefinch/atomic)** — same
  shape: `f.Sync()` on the temp file ("fsync is important, otherwise
  os.Rename could rename a zero-length file"), then `ReplaceFile`. No
  directory is ever opened.

None of this makes those libraries broken for their stated purpose — the
file-level fsync they all do is the important, commonly-skipped step, and
without it you really can get a zero-length file after a crash. But none of
them close the second gap, and none say so.

### The one popular implementation that gets it right

Python's [`atomicwrites`](https://github.com/untitaker/python-atomicwrites)
(PyPI, unmaintained since 2020) does this correctly, including the macOS
`F_FULLFSYNC` case this package also handles (see below):

```python
def _sync_directory(directory):
    # Ensure that filenames are written to disk
    fd = os.open(directory, 0)
    try:
        _proper_fsync(fd)
    finally:
        os.close(fd)

def _replace_atomic(src, dst):
    os.rename(src, dst)
    _sync_directory(os.path.normpath(os.path.dirname(dst)))
```

The recipe is known. It's just not what the popular Go or Node packages
implement. `durablewrite` is the Go equivalent of `_sync_directory` plus
`_replace_atomic`, packaged with tests, options, and a documented failure
model.

## Install

```sh
go get github.com/__GH_OWNER__/durablewrite
```

Go 1.27+, standard library only, no runtime dependencies.

## Use

```go
package main

import (
	"context"
	"log"

	"github.com/__GH_OWNER__/durablewrite"
)

func main() {
	err := durablewrite.WriteFile(context.Background(), "/etc/myapp/config.json",
		[]byte(`{"enabled":true}`), 0o644)
	if err != nil {
		log.Fatal(err)
	}
}
```

For content generated incrementally, so it never has to be buffered in
memory as a single `[]byte`, use `Do` with a callback, or `NewWriter`
directly:

```go
err := durablewrite.Do(ctx, path, 0o644, func(w io.Writer) error {
	return json.NewEncoder(w).Encode(largeValue)
})
```

```go
w, err := durablewrite.NewWriter(ctx, path, 0o644)
if err != nil {
	return err
}
defer w.Close() // no-op once Commit has already succeeded

if _, err := io.Copy(w, someLargeSource); err != nil {
	return err
}
return w.Commit()
```

## What "durable" means here, precisely

When `WriteFile`, `Do`, or `Writer.Commit` return `nil`, the following has
happened, in this order:

1. The new content was written to a temp file in the destination's
   directory (or an explicit [`WithTempDir`], same filesystem required).
2. That temp file was fsynced (`F_FULLFSYNC` on macOS by default — see
   below).
3. The temp file was renamed onto the destination path.
4. The destination's parent directory was opened and fsynced
   (`F_FULLFSYNC` on macOS by default).

Given that sequence, and given that the filesystem and storage device honor
fsync/flush as POSIX and the vendor's own documentation describe, the
destination path is expected to durably contain the new content across a
crash or power loss that happens after the call returns.

**That is a correctness argument, not a test result.** This package cannot,
and does not claim to, have verified crash safety against an actual
power-loss event — nobody can do that from a CI sandbox. What was actually
tested (see `durablewrite_test.go`) is that the syscalls happen, in the
right order, and that the filesystem ends up in the state each step
predicts (content correct, no stray temp files, destination untouched on a
pre-rename failure, destination present but sync-status uncertain on a
post-rename failure). Two things stay outside any userspace library's
control regardless: a storage device whose write cache ignores flush
commands entirely (some cheap USB/SD media), and a filesystem whose
`rename(2)` isn't itself atomic (NFS with multiple clients — the same
caveat `renameio` documents applies here).

### Failure behavior

- **Before the rename** (creating, writing, fsyncing, or closing the temp
  file fails, or the rename call itself fails): the destination path is
  left completely untouched, and the temp file is removed on a best-effort
  basis. `Writer.Close()` (called via `defer` after `NewWriter`, as the
  examples above do) performs this cleanup automatically for `WriteFile`
  and `Do`.
- **After a successful rename** (opening, fsyncing, or closing the parent
  directory fails): the destination path *already has the new content,
  right now*. The returned error wraps `durablewrite.ErrDirSyncFailed`.
  Nothing is undone — reversing the rename could destroy a newer version of
  the file if something else replaced it in the meantime, and undoing a
  rename provides no extra safety when directory-metadata durability is
  exactly what's in question. Treat this error as "this write may need to
  be redone after a crash" and decide what that means for your data (retry
  the directory sync yourself, alert, mark the path for re-verification).

## macOS: fsync(2) is not enough

Apple's own `fsync(2)` man page says plain `fsync` "does not necessarily
flush data caches inside disk drives" and directs callers who need that
guarantee to `fcntl(F_FULLFSYNC)`. `os.File.Sync()` in Go calls plain
`fsync(2)`. On Darwin, `durablewrite` instead issues `F_FULLFSYNC` for both
the temp file and the parent directory, by default, via the raw `fcntl`
syscall using only `syscall` package constants (no `golang.org/x/sys`
dependency) — see `fsync_darwin.go`.

`F_FULLFSYNC` can be significantly slower than plain `fsync`, because it
asks the drive to actually flush its cache rather than just accept the
write. [`WithFastDarwinSync`] opts back into plain `fsync` on macOS when
that tradeoff is acceptable to you; it's a no-op on every other platform.
**If you don't pass it, macOS writes are correct but not free — see the
benchmark below for what that costs on this machine.**

## Options

| Option | Effect |
| --- | --- |
| `WithPermissions(perm)` | File mode for the created/replaced file (subject to umask); overrides the `perm` parameter if both are given. |
| `WithTempDir(dir)` | Create the temp file in `dir` instead of the destination's own directory. Must be the same filesystem, or the final rename fails with `EXDEV`. |
| `WithUnsafeNoDirSync()` | Skip only the parent-directory fsync — reproduces the exact guarantee of write-file-atomic / atomically / renameio / natefinch-atomic. |
| `WithUnsafeNoSync()` | Skip both fsyncs — plain write + rename, no durability at all. For disposable data only. |
| `WithFastDarwinSync()` | On macOS, use plain `fsync` instead of `F_FULLFSYNC`. No-op elsewhere. |
| `WithTrace(func(string))` | Called with a line of text for each durability step as it happens — see `examples/durable-demo`. |

## Benchmark

`go test -bench=. -benchtime=200x` on the machine this was built on (Apple
M5 Pro, macOS, APFS on the internal SSD), three separate runs, so this is a
**range**, not a single cherry-picked number:

| Benchmark | ns/op (3 runs) | What it measures |
| --- | --- | --- |
| `WriteFile_Default` | 6.62 ms – 8.11 ms | Full default: fsync temp file (`F_FULLFSYNC`), rename, fsync dir (`F_FULLFSYNC`) |
| `WriteFile_NoDirSync` | 3.77 ms – 4.29 ms | `WithUnsafeNoDirSync` — same as the audited libraries above |
| `WriteFile_FastDarwinSync` | 6.57 ms – 8.19 ms | `WithFastDarwinSync` — plain `fsync` instead of `F_FULLFSYNC`, both steps |
| `WriteFile_NoSync` | 100 µs – 159 µs | `WithUnsafeNoSync` — write + rename, nothing durable |
| `OSWriteFile_NoAtomicity` (baseline) | 54 µs – 80 µs | plain `os.WriteFile`, no atomicity and no fsync at all |

Raw output from one of the three runs:

```
goos: darwin
goarch: arm64
pkg: github.com/__GH_OWNER__/durablewrite
cpu: Apple M5 Pro
BenchmarkWriteFile_Default-15           	     200	   7127222 ns/op	    1731 B/op	      25 allocs/op
BenchmarkWriteFile_NoDirSync-15         	     200	   4293502 ns/op	    1483 B/op	      19 allocs/op
BenchmarkWriteFile_NoSync-15            	     200	    130821 ns/op	    1420 B/op	      18 allocs/op
BenchmarkWriteFile_FastDarwinSync-15    	     200	   7312160 ns/op	    1729 B/op	      25 allocs/op
BenchmarkOSWriteFile_NoAtomicity-15     	     200	     53661 ns/op	     360 B/op	       5 allocs/op
PASS
ok  	github.com/__GH_OWNER__/durablewrite	4.370s
```

Two honest observations from these numbers, not the ones you might expect
going in:

- **The directory fsync roughly doubles the cost of the file-only fsync**
  (≈4 ms → ≈7 ms here), which is the real price of closing the gap this
  package exists for. It is not free, and this README isn't going to
  pretend it is.
- **`F_FULLFSYNC` was *not* measurably slower than plain `fsync` on this
  machine** (`WriteFile_Default` and `WriteFile_FastDarwinSync` overlap in
  their ranges above). That's a real, surprising result on this specific
  Apple Silicon + APFS + internal SSD combination — the commonly cited
  "`F_FULLFSYNC` is much slower" claim is about spinning disks and some
  older SSD controllers forcing a full cache flush; this particular drive
  appears to make that flush cheap. Don't take this one machine's number as
  a guarantee for yours — SSD firmware and controller behavior vary, and a
  spinning disk or a different SSD can absolutely show the gap the option
  exists for. Run the benchmark on your own target hardware before deciding
  `WithFastDarwinSync` is or isn't worth it.

Run it yourself: `cd durablewrite && go test -bench=. -benchtime=200x -count=3`
(use `-count=1` if you just want a quick check; `go test`'s own result
cache never applies to `-bench`, so every invocation re-measures).

## Example program

```sh
$ go run ./examples/durable-demo
writing /var/folders/.../durablewrite-demo-1331480419/config.json (dir sync enabled: true)

[08:12:43.953908] created temp file .../.config.json.18nk6drj0b8f3
[08:12:43.965008] fsynced temp file .../.config.json.18nk6drj0b8f3
[08:12:43.965423] renamed .../.config.json.18nk6drj0b8f3 to .../config.json
[08:12:43.965507] opened parent directory /var/folders/.../durablewrite-demo-1331480419
[08:12:43.968801] fsynced parent directory /var/folders/.../durablewrite-demo-1331480419
[08:12:43.968990] closed parent directory /var/folders/.../durablewrite-demo-1331480419

committed, on-disk content: {"example":"durablewrite","time":"2026-09-27T08:12:43+03:00"}

$ go run ./examples/durable-demo -no-dir-sync
writing /var/folders/.../durablewrite-demo-863396908/config.json (dir sync enabled: false)

[08:12:44.503335] created temp file .../.config.json.22j2y33qq3278
[08:12:44.508537] fsynced temp file .../.config.json.22j2y33qq3278
[08:12:44.508853] renamed .../.config.json.22j2y33qq3278 to .../config.json

committed, on-disk content: {"example":"durablewrite","time":"2026-09-27T08:12:44+03:00"}
```

The second run's log is missing the three "parent directory" lines —
`-no-dir-sync` maps to `WithUnsafeNoDirSync`, which is exactly the gap this
package closes for the default path.

This uses `WithTrace` rather than a real syscall tracer: this sandbox has
no working `strace` and no passwordless `dtruss`/`fs_usage` (both need root
on macOS, and `dtruss` further needs SIP considerations that don't apply
here). On a Linux box with `strace` available, the equivalent and stronger
evidence is:

```sh
strace -f -e trace=openat,fsync,rename,renameat,renameat2,close \
  go run ./examples/durable-demo
```

which was not run here for lack of a Linux host in this environment — said
plainly rather than faked.

## What it does not do

- **It does not protect against a storage device that ignores flush
  commands.** Some cheap USB/SD media accept `fsync`/`F_FULLFSYNC` and lie
  about having actually persisted the data. No software fsync call can
  detect or fix that.
- **It does not make `rename(2)` atomic on filesystems where it isn't.**
  Multi-client NFS is the standard example — same caveat `renameio`
  documents.
- **It is not a general-purpose file locking or coordination primitive.**
  Concurrent writers to the same path each get their own temp file and
  fsync, but the last rename to complete wins; this package does not
  serialize or merge concurrent writes to one destination.
- **It has not been tested against a real crash or power-loss event**, and
  makes no claim to have been — see "What durable means here, precisely"
  above.
- **Windows is not a target of this package.** It builds there (everything
  is stdlib `os`/`syscall`), but the directory-fsync-after-rename argument
  above is POSIX-specific, `os.Rename`'s overwrite behavior differs from
  POSIX `rename(2)`, and none of this was tested on Windows.

## Develop

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .        # must print nothing
```
