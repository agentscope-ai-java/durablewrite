package durablewrite

import (
	"context"
	"io"
	"os"
)

// WriteFile durably replaces the file at path with data, as if by writing
// data to a temporary file in the same directory, fsyncing it, renaming it
// onto path, then fsyncing path's parent directory. perm sets the file
// mode, subject to umask, exactly like [os.WriteFile]'s perm argument.
//
// See the package doc comment for precisely what "durably" guarantees and
// does not guarantee, and [Writer.Commit]'s doc comment for the failure
// behavior at each step — WriteFile is implemented as
// [NewWriter]+Write+[Writer.Commit] and inherits it exactly.
//
// ctx may be nil, which behaves like [context.Background]. If ctx is
// already canceled, WriteFile returns its error without creating anything.
func WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, opts ...Option) error {
	w, err := NewWriter(ctx, path, perm, opts...)
	if err != nil {
		return err
	}
	defer w.Close()

	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Commit()
}

// Do calls fn with an [io.Writer] backed by a temporary file, and, if fn
// returns nil, durably commits that content to path exactly as
// [Writer.Commit] describes. If fn returns an error, or panics, nothing is
// written to path and the temporary file is removed.
//
// Do is the callback counterpart to using [NewWriter] directly: it exists
// so that generating content that streams into an io.Writer (a
// json.Encoder, an archive/zip.Writer, io.Copy from a network response,
// ...) does not require buffering the whole thing in memory before calling
// [WriteFile].
//
//	err := durablewrite.Do(ctx, path, 0o644, func(w io.Writer) error {
//		return json.NewEncoder(w).Encode(largeValue)
//	})
func Do(ctx context.Context, path string, perm os.FileMode, fn func(io.Writer) error, opts ...Option) error {
	w, err := NewWriter(ctx, path, perm, opts...)
	if err != nil {
		return err
	}
	defer w.Close()

	if err := fn(w); err != nil {
		return err
	}
	return w.Commit()
}
