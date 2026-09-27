package durablewrite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// These benchmarks measure the real cost of the durability steps this
// package adds over the naive "write, rename" recipe: the temp file fsync,
// and the parent directory fsync. Each writes a small (typical config file
// sized) payload, since the fsync/rename overhead — not payload size — is
// what these options change. Run with -count=N and compare ranges across
// runs; a single run on a shared or virtualized disk is not representative.
var benchPayload = []byte(`{"setting":"value","enabled":true,"retries":3}`)

func benchmarkWriteFile(b *testing.B, opts ...Option) {
	dir := b.TempDir()
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f-%d.json", i))
		if err := WriteFile(ctx, path, benchPayload, 0o644, opts...); err != nil {
			b.Fatalf("WriteFile: %v", err)
		}
	}
}

// BenchmarkWriteFile_Default is the full, safe default: fsync the temp
// file, rename, fsync the parent directory (F_FULLFSYNC on darwin).
func BenchmarkWriteFile_Default(b *testing.B) {
	benchmarkWriteFile(b)
}

// BenchmarkWriteFile_NoDirSync skips only the parent directory fsync —
// the same guarantee as write-file-atomic, google/renameio and
// natefinch/atomic.
func BenchmarkWriteFile_NoDirSync(b *testing.B) {
	benchmarkWriteFile(b, WithUnsafeNoDirSync())
}

// BenchmarkWriteFile_NoSync skips both fsyncs entirely: write + rename,
// nothing durable.
func BenchmarkWriteFile_NoSync(b *testing.B) {
	benchmarkWriteFile(b, WithUnsafeNoSync())
}

// BenchmarkWriteFile_FastDarwinSync uses plain fsync(2) instead of
// F_FULLFSYNC on macOS; it is identical to BenchmarkWriteFile_Default on
// non-darwin platforms, kept here so the README numbers line up across
// platforms.
func BenchmarkWriteFile_FastDarwinSync(b *testing.B) {
	benchmarkWriteFile(b, WithFastDarwinSync())
}

// BenchmarkOSWriteFile_NoAtomicity is *not* atomic and *not* fsynced at
// all — plain os.WriteFile, included only as the "do nothing" floor so the
// other numbers have a baseline to compare against.
func BenchmarkOSWriteFile_NoAtomicity(b *testing.B) {
	dir := b.TempDir()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f-%d.json", i))
		if err := os.WriteFile(path, benchPayload, 0o644); err != nil {
			b.Fatalf("os.WriteFile: %v", err)
		}
	}
}
