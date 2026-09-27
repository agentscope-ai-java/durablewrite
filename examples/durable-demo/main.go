// Command durable-demo writes one file with durablewrite.WriteFile and logs
// every durability-relevant step as it happens, via durablewrite.WithTrace,
// so the parent-directory fsync is visible without a system call tracer.
//
// (A real strace/dtruss trace would be stronger evidence, but this sandbox
// has neither a working strace nor passwordless dtruss/fs_usage access —
// both require root on macOS. WithTrace is the "at minimum clear logging"
// fallback the package's spec calls for; see the README for the exact
// wording of that limitation.)
//
// Run it twice: once against a normal directory, once with -no-dir-sync to
// see that step disappear from the log.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	durablewrite "github.com/__GH_OWNER__/durablewrite"
)

func main() {
	noDirSync := flag.Bool("no-dir-sync", false, "skip the parent directory fsync (durablewrite.WithUnsafeNoDirSync)")
	flag.Parse()

	dir, err := os.MkdirTemp("", "durablewrite-demo-")
	if err != nil {
		log.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "config.json")
	data := []byte(`{"example":"durablewrite","time":"` + time.Now().Format(time.RFC3339) + `"}`)

	opts := []durablewrite.Option{
		durablewrite.WithTrace(func(step string) {
			fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05.000000"), step)
		}),
	}
	if *noDirSync {
		opts = append(opts, durablewrite.WithUnsafeNoDirSync())
	}

	fmt.Printf("writing %s (dir sync enabled: %v)\n\n", path, !*noDirSync)

	if err := durablewrite.WriteFile(context.Background(), path, data, 0o644, opts...); err != nil {
		log.Fatalf("WriteFile: %v", err)
	}

	fmt.Println()
	got, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("ReadFile: %v", err)
	}
	fmt.Printf("committed, on-disk content: %s\n", got)
}
