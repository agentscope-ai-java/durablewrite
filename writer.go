package durablewrite

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

// ErrDirSyncFailed wraps the error returned by [Writer.Commit] when the
// rename onto the destination path already succeeded but fsyncing the
// parent directory afterward failed (or failed to even open).
//
// When a Commit error wraps ErrDirSyncFailed, the destination path already
// contains the new content, right now, and will keep containing it as long
// as nothing crashes. What is not guaranteed is that the rename itself will
// still be recorded after a crash: the directory entry that makes the new
// inode reachable at that path may not have reached stable storage. Commit
// deliberately does not attempt to undo the rename in this case — reversing
// it could destroy a newer version of the file if something else already
// replaced it in the meantime, and undoing a rename provides no additional
// safety when the very mechanism in question (directory metadata durability)
// is what's in doubt. Callers that see this error should treat it as "the
// write may need to be redone after a crash", for example by retrying
// Commit's directory sync themselves (open the parent directory and Sync it)
// or by recording that this path needs re-verification.
var ErrDirSyncFailed = errors.New("durablewrite: rename succeeded but syncing the parent directory failed")

// writerState tracks what has already happened to a Writer, so Close and
// Commit can be called safely (Close is always safe to call, Commit exactly
// once) without double-cleaning-up or double-renaming.
type writerState int

const (
	stateOpen writerState = iota
	stateCommitted
	stateClosed
)

func (s writerState) String() string {
	switch s {
	case stateOpen:
		return "open"
	case stateCommitted:
		return "committed"
	case stateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Writer accumulates content in a temporary file and, on [Writer.Commit],
// durably replaces the destination path with it. It is the streaming
// counterpart to [WriteFile]: use it when the content is generated
// incrementally and buffering it all in memory first (as WriteFile
// requires) is undesirable.
//
// The usual pattern is:
//
//	w, err := durablewrite.NewWriter(ctx, path, 0o644)
//	if err != nil {
//		return err
//	}
//	defer w.Close() // no-op once Commit has succeeded
//
//	if _, err := io.Copy(w, someLargeSource); err != nil {
//		return err
//	}
//	return w.Commit()
//
// A Writer is not safe for concurrent use.
type Writer struct {
	ctx      context.Context
	destPath string
	dir      string
	cfg      config
	tmp      *os.File
	tmpPath  string
	state    writerState
}

// NewWriter creates the temporary file that will eventually replace path,
// and returns a [Writer] ready to be written to. perm sets the file mode
// (subject to umask), as with [os.WriteFile]; it can be overridden by
// passing [WithPermissions] in opts.
//
// The temporary file is created in path's own directory unless
// [WithTempDir] says otherwise, using O_EXCL so an existing file is never
// silently reused, with a random suffix retried on collision.
//
// Callers must eventually call either [Writer.Commit] (to publish the
// content) or [Writer.Close] (to discard it); calling Close after a
// successful Commit is a safe no-op, which is what makes `defer w.Close()`
// immediately after NewWriter the right pattern — see the example above.
func NewWriter(ctx context.Context, path string, perm os.FileMode, opts ...Option) (*Writer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cfg := config{perm: perm}
	for _, opt := range opts {
		opt(&cfg)
	}

	dir := cfg.tempDir
	if dir == "" {
		dir = filepath.Dir(path)
	}

	tmp, tmpPath, err := createTempFile(dir, filepath.Base(path), cfg.perm)
	if err != nil {
		return nil, fmt.Errorf("durablewrite: create temp file for %s in %s: %w", path, dir, err)
	}
	cfg.tracef("created temp file %s", tmpPath)

	return &Writer{
		ctx:      ctx,
		destPath: path,
		dir:      filepath.Dir(path),
		cfg:      cfg,
		tmp:      tmp,
		tmpPath:  tmpPath,
		state:    stateOpen,
	}, nil
}

// createTempFile creates a new, exclusively-owned file at dir/.base.<random>,
// retrying on name collisions. It is modeled on the same approach used by
// google/renameio and the stdlib's own os.CreateTemp: O_EXCL guarantees we
// never write into (or truncate) a file that was already there.
func createTempFile(dir, base string, perm os.FileMode) (*os.File, string, error) {
	prefix := filepath.Join(dir, "."+base+".")
	const maxAttempts = 10000
	for attempt := 0; attempt < maxAttempts; attempt++ {
		name := prefix + strconv.FormatUint(rand.Uint64(), 36)
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return f, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("no unused temp file name after %d attempts", maxAttempts)
}

// Write appends p to the temporary file. It returns an error, without
// writing anything, if the Writer's context has been canceled, if Commit
// has already been called, or if the Writer has been closed.
func (w *Writer) Write(p []byte) (int, error) {
	if w.state != stateOpen {
		return 0, fmt.Errorf("durablewrite: Write called on a %s Writer", w.state)
	}
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.tmp.Write(p)
}

// Commit publishes the content written so far: it fsyncs the temporary
// file, renames it onto the destination path, then opens and fsyncs the
// destination's parent directory (unless [WithUnsafeNoSync] or
// [WithUnsafeNoDirSync] disabled those steps). See the package doc comment
// for exactly what "durable" means here and what it does not cover.
//
// Commit may be called at most once, and only while the Writer is still
// open (it is an error to call it after Close, or a second time after a
// prior Commit).
//
// Failure behavior, by the point at which it happens:
//
//   - Before the rename (fsyncing or closing the temp file fails, or the
//     rename call itself fails): the destination path is left completely
//     untouched, and the temporary file is removed on a best-effort basis.
//     The returned error does not wrap ErrDirSyncFailed.
//   - After a successful rename (opening, fsyncing or closing the parent
//     directory fails): the destination path already has the new content;
//     see [ErrDirSyncFailed] for what is and is not guaranteed from here.
//     There is nothing left to clean up — the temporary file no longer
//     exists under its temporary name, it exists at the destination path.
func (w *Writer) Commit() error {
	if w.state != stateOpen {
		return fmt.Errorf("durablewrite: Commit called on a %s Writer", w.state)
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}

	if !w.cfg.noFileSync {
		if err := w.syncFile(w.tmp); err != nil {
			w.abandon()
			return fmt.Errorf("durablewrite: fsync temp file %s: %w", w.tmpPath, err)
		}
		w.cfg.tracef("fsynced temp file %s", w.tmpPath)
	}

	if err := w.tmp.Close(); err != nil {
		w.state = stateClosed
		os.Remove(w.tmpPath)
		return fmt.Errorf("durablewrite: close temp file %s: %w", w.tmpPath, err)
	}

	if err := os.Rename(w.tmpPath, w.destPath); err != nil {
		w.state = stateClosed
		os.Remove(w.tmpPath)
		return fmt.Errorf("durablewrite: rename %s to %s: %w", w.tmpPath, w.destPath, err)
	}
	w.cfg.tracef("renamed %s to %s", w.tmpPath, w.destPath)

	// From here on the rename has happened: the destination holds the new
	// content. Nothing below undoes it on failure — see the doc comment.
	w.state = stateCommitted

	if w.cfg.noDirSync {
		return nil
	}

	dirFile, err := os.Open(w.dir)
	if err != nil {
		return fmt.Errorf("%w: open parent directory %s: %v", ErrDirSyncFailed, w.dir, err)
	}
	w.cfg.tracef("opened parent directory %s", w.dir)

	syncErr := w.syncFile(dirFile)
	if syncErr == nil {
		w.cfg.tracef("fsynced parent directory %s", w.dir)
	}
	closeErr := dirFile.Close()
	if syncErr != nil {
		return fmt.Errorf("%w: fsync parent directory %s: %v", ErrDirSyncFailed, w.dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: close parent directory %s: %v", ErrDirSyncFailed, w.dir, closeErr)
	}
	w.cfg.tracef("closed parent directory %s", w.dir)
	return nil
}

// syncFile fsyncs f as durably as the platform and configured options
// allow: F_FULLFSYNC on darwin by default (see fsync_darwin.go), or plain
// fsync(2)/f.Sync() everywhere else, or when [WithFastDarwinSync] opted out
// of F_FULLFSYNC.
func (w *Writer) syncFile(f *os.File) error {
	if w.cfg.fastDarwin {
		return f.Sync()
	}
	return fullFsync(f)
}

// abandon closes and removes the temp file after a pre-rename failure.
func (w *Writer) abandon() {
	w.state = stateClosed
	w.tmp.Close()
	os.Remove(w.tmpPath)
}

// Close discards the Writer if [Writer.Commit] has not been called (or did
// not succeed): it closes and removes the temporary file. If Commit already
// succeeded, Close is a no-op that returns nil — there is no temporary file
// left to remove, and the destination path must never be touched by Close.
// Close is idempotent: calling it more than once is safe.
func (w *Writer) Close() error {
	switch w.state {
	case stateCommitted, stateClosed:
		return nil
	}
	w.state = stateClosed
	closeErr := w.tmp.Close()
	removeErr := os.Remove(w.tmpPath)
	if closeErr != nil {
		return fmt.Errorf("durablewrite: close temp file %s: %w", w.tmpPath, closeErr)
	}
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return fmt.Errorf("durablewrite: remove temp file %s: %w", w.tmpPath, removeErr)
	}
	return nil
}
