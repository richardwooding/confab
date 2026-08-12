// Command compress-assets precompresses the built web client into .br (brotli)
// and .gz (gzip) siblings, so the relay can serve the smallest encoding each
// client accepts. It is pure Go — no external gzip/brotli binary — so it runs
// anywhere `go` does (including the goreleaser CI before-hook).
package main

import (
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/andybalholm/brotli"
)

// compressible extensions — text and wasm. Already-compressed assets (png, gz,
// br) are skipped.
var compressible = map[string]bool{
	".wasm": true, ".js": true, ".css": true, ".html": true,
	".json": true, ".svg": true, ".webmanifest": true, ".txt": true,
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: compress-assets <dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "compress-assets:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !compressible[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// brotli quality 11 for small files; 9 for the large wasm to keep the
		// dev loop snappy — 9 already beats gzip -9 and 11's extra gain is tiny.
		q := brotli.BestCompression
		if len(data) > 1<<20 {
			q = 9
		}
		if err := writeBrotli(p+".br", data, q); err != nil {
			return err
		}
		if err := writeGzip(p+".gz", data); err != nil {
			return err
		}
		fmt.Printf("  %-20s %8d  → br %8d  gz %8d\n", filepath.Base(p), len(data), fileSize(p+".br"), fileSize(p+".gz"))
		return nil
	})
}

func writeBrotli(path string, data []byte, quality int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w := brotli.NewWriterLevel(f, quality)
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Close()
}

func writeGzip(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Close()
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}
