package durablewrite

import (
	"fmt"
	"os"
)

// config holds the resolved options for a single write.
type config struct {
	perm       os.FileMode
	tempDir    string
	noFileSync bool
	noDirSync  bool
	fastDarwin bool
	trace      func(string)
}

// trace calls the configured trace function, if any. It never panics on a
// nil trace function.
func (c *config) tracef(format string, args ...any) {
	if c.trace == nil {
		return
	}
	c.trace(fmt.Sprintf(format, args...))
}

// Option configures optional behaviour of [WriteFile], [Do] and [NewWriter].
// The zero value of every option is the safe, durable default; every Option
// in this package either narrows the temp file's location/permissions or
// trades away durability, and the latter are named accordingly.
type Option func(*config)

// WithPermissions sets the file mode used to create the temporary file,
// and therefore the destination file after the rename. It behaves like the
// perm argument to [os.WriteFile]: it is subject to the process umask.
//
// [WriteFile] and [NewWriter] already take perm as a required parameter;
// WithPermissions exists so options built once (for example in a struct of
// defaults) can also carry the permission bits, and so it can override a
// perm passed to WriteFile/NewWriter when both are supplied — the last
// Option in the list wins over the perm parameter.
func WithPermissions(perm os.FileMode) Option {
	return func(c *config) { c.perm = perm }
}

// WithTempDir places the temporary file in dir instead of the destination
// file's own directory. dir must be on the same filesystem/mount as the
// destination, or the final rename will fail with a cross-device link error
// (EXDEV) — rename(2) cannot move a file across filesystems, which is
// exactly why the safe default is the destination's own directory. Use this
// only when you know the two paths share a filesystem, for example a
// dedicated scratch subdirectory next to the destination.
func WithTempDir(dir string) Option {
	return func(c *config) { c.tempDir = dir }
}

// WithUnsafeNoSync skips fsyncing both the temporary file and the parent
// directory. The write is then exactly the incomplete "write, rename"
// recipe this package exists to avoid: fast, but a crash at any point can
// leave the destination missing, truncated, or containing old data. Use
// this only for genuinely disposable data (caches that are fine to lose,
// scratch files rebuilt on every startup) where the durability this package
// otherwise provides is not worth its cost.
func WithUnsafeNoSync() Option {
	return func(c *config) { c.noFileSync = true; c.noDirSync = true }
}

// WithUnsafeNoDirSync fsyncs the temporary file as usual but skips fsyncing
// the parent directory after the rename. This reproduces the behavior of
// the popular libraries this package's README audits (write-file-atomic,
// atomically, google/renameio, natefinch/atomic): the file's own content is
// durable once written, but the rename that makes it visible at the
// destination path is not guaranteed to survive a crash. Prefer this over
// WithUnsafeNoSync when you specifically want that well known, widely
// deployed tradeoff rather than no durability at all.
func WithUnsafeNoDirSync() Option {
	return func(c *config) { c.noDirSync = true }
}

// WithFastDarwinSync is a no-op on every platform except macOS. On macOS it
// replaces the default F_FULLFSYNC (which asks the drive to actually flush
// its write cache) with plain fsync(2) (which macOS documents as not
// flushing the drive's cache). See the package doc comment and the README
// benchmark for the durability/latency tradeoff this makes; it is the same
// weaker guarantee every other Go/Node atomic-write library gives you on
// macOS, whether or not they say so.
func WithFastDarwinSync() Option {
	return func(c *config) { c.fastDarwin = true }
}

// WithTrace calls fn once for each durability-relevant step Commit performs
// — creating the temp file, fsyncing it, renaming, opening the parent
// directory, fsyncing it, closing it — in the order they happen. It exists
// so callers (and this package's examples/README) can observe that the
// directory fsync genuinely happens, without resorting to a system call
// tracer. fn is called synchronously and must not block; a nil fn (the
// default) disables tracing.
func WithTrace(fn func(step string)) Option {
	return func(c *config) { c.trace = fn }
}
