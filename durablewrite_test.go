package durablewrite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteFile_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(context.Background(), path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestWriteFile_ReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := os.WriteFile(path, []byte("old content, longer than new"), 0o600); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}

	if err := WriteFile(context.Background(), path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q (old content must not linger past the new file's end)", got, "new")
	}
}

func TestWriteFile_NoTempFileLeftBehindOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(context.Background(), path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.txt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory entries = %v, want exactly [out.txt]", names)
	}
}

func TestWriteFile_MissingDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does", "not", "exist", "out.txt")

	err := WriteFile(context.Background(), path, []byte("hi"), 0o644)
	if err == nil {
		t.Fatal("WriteFile: got nil error, want an error for a missing destination directory")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist, Stat err = %v", statErr)
	}
}

func TestWriteFile_NoStrayTempFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does", "not", "exist", "out.txt")

	if err := WriteFile(context.Background(), path, []byte("hi"), 0o644); err == nil {
		t.Fatal("expected an error")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory entries = %v, want none (nothing should be created under dir when the destination's own parent doesn't exist)", entries)
	}
}

func TestWriteFile_CanceledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := WriteFile(ctx, path, []byte("hi"), 0o644)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist after a canceled context, Stat err = %v", statErr)
	}
}

func TestDo_StreamsWithoutBufferingCallerSide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	want := strings.Repeat("chunk-", 1000)
	err := Do(context.Background(), path, 0o644, func(w io.Writer) error {
		for i := 0; i < 1000; i++ {
			if _, err := io.WriteString(w, "chunk-"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != want {
		t.Fatalf("content length = %d, want %d", len(got), len(want))
	}
}

func TestDo_FnErrorLeavesDestinationUntouchedAndCleansUpTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("boom")
	err := Do(context.Background(), path, 0o644, func(w io.Writer) error {
		io.WriteString(w, "partial")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want to wrap sentinel", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("content = %q, want untouched %q", got, "original")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory entries = %v, want exactly [out.txt] (temp file must be cleaned up)", entries)
	}
}

func TestWriter_CloseAfterCommitIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	w, err := NewWriter(context.Background(), path, 0o644)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close after Commit should be a no-op, got: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close should also be a no-op, got: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriter_CommitTwiceErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	w, err := NewWriter(context.Background(), path, 0o644)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if err := w.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := w.Commit(); err == nil {
		t.Fatal("second Commit should error")
	}
}

func TestWriter_WriteAfterCommitErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	w, err := NewWriter(context.Background(), path, 0o644)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if err := w.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := w.Write([]byte("late")); err == nil {
		t.Fatal("Write after Commit should error")
	}
}

func TestWriter_CloseWithoutCommitRemovesTempAndLeavesDestinationUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := NewWriter(context.Background(), path, 0o644)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Write([]byte("abandoned")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("content = %q, want untouched %q", got, "original")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory entries = %v, want exactly [out.txt]", entries)
	}
}

func TestWithUnsafeNoSync_StillWritesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(context.Background(), path, []byte("fast"), 0o644, WithUnsafeNoSync()); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "fast" {
		t.Fatalf("content = %q", got)
	}
}

func TestWithUnsafeNoDirSync_StillWritesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(context.Background(), path, []byte("fast"), 0o644, WithUnsafeNoDirSync()); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "fast" {
		t.Fatalf("content = %q", got)
	}
}

func TestWithTempDir_SameFilesystem(t *testing.T) {
	dir := t.TempDir()
	scratch := filepath.Join(dir, "scratch")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(context.Background(), path, []byte("via scratch"), 0o644, WithTempDir(scratch)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "via scratch" {
		t.Fatalf("content = %q", got)
	}

	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("ReadDir scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch dir entries = %v, want none left behind", entries)
	}
}

func TestWithPermissions_OverridesPositionalPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := WriteFile(context.Background(), path, []byte("x"), 0o644, WithPermissions(0o600)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWithTrace_ObservesDirectoryFsyncStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	var mu sync.Mutex
	var steps []string
	trace := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, step)
	}

	if err := WriteFile(context.Background(), path, []byte("traced"), 0o644, WithTrace(trace)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	joined := strings.Join(steps, "\n")
	for _, want := range []string{"created temp file", "renamed", "opened parent directory", "fsynced parent directory", "closed parent directory"} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace steps = %q, missing step containing %q", steps, want)
		}
	}
}

func TestErrDirSyncFailed_WrapsRealFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	// Force the directory-open step in Commit to fail by removing the
	// directory's execute permission after the Writer has already opened
	// its temp file inside it (so the temp file's own creation succeeds,
	// only the later os.Open(dir) in Commit is denied).
	w, err := NewWriter(context.Background(), path, 0o644)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer os.Chmod(dir, 0o755) // restore so t.TempDir() cleanup can remove it
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := os.Chmod(dir, 0o300); err != nil { // write+exec bits without read; os.Open for sync needs read
		t.Fatalf("Chmod: %v", err)
	}

	err = w.Commit()
	os.Chmod(dir, 0o755)

	if err == nil {
		t.Fatal("Commit: got nil error, want ErrDirSyncFailed")
	}
	if !errors.Is(err, ErrDirSyncFailed) {
		t.Fatalf("Commit err = %v, want it to wrap ErrDirSyncFailed", err)
	}

	// The rename must still have happened: the destination has the new
	// content even though the directory sync failed.
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(got) != "data" {
		t.Fatalf("content = %q, want %q (rename must not be undone on a dir-sync failure)", got, "data")
	}
}

func TestWriteFile_LargeContentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")

	data := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x01}, 1<<20/4) // 1 MiB
	if err := WriteFile(context.Background(), path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round-tripped content mismatch, len got=%d want=%d", len(got), len(data))
	}
}

func TestWriteFile_ConcurrentWritesToDifferentFilesDoNotCollide(t *testing.T) {
	dir := t.TempDir()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprintf("file-%d.txt", i))
			data := []byte(fmt.Sprintf("content-%d", i))
			if err := WriteFile(context.Background(), path, data, 0o644); err != nil {
				errs <- err
				return
			}
			got, err := os.ReadFile(path)
			if err != nil {
				errs <- err
				return
			}
			if string(got) != string(data) {
				errs <- fmt.Errorf("file %d: got %q want %q", i, got, data)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCreateTempFile_UniqueNamesUnderCollisionPressure(t *testing.T) {
	dir := t.TempDir()

	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		f, name, err := createTempFile(dir, "base", 0o600)
		if err != nil {
			t.Fatalf("createTempFile: %v", err)
		}
		f.Close()
		if seen[name] {
			t.Fatalf("duplicate temp name %s", name)
		}
		seen[name] = true
	}
}
