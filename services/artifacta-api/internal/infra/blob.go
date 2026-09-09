// Package infra holds persistence adapters: the filesystem blob store and the
// file-backed metadata + hash-chained audit store. Both sit behind interfaces
// declared by their consumers (see internal/api). Postgres and S3-compatible
// adapters drop in behind the same interfaces for scale.
package infra

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BlobFS is a filesystem-backed blob store. Bytes are returned only after the
// API layer's authorization allow — the store itself makes no access decisions.
// Each bundle is one immutable version, keyed by (slug, n) (ADR-0013).
type BlobFS struct{ dir string }

func NewBlobFS(dir string) (*BlobFS, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &BlobFS{dir: dir}, nil
}

// path is the on-disk name for one version's bundle: "<slug>.v<n>.bundle".
func (b *BlobFS) path(slug string, n int32) string {
	return filepath.Join(b.dir, fmt.Sprintf("%s.v%d.bundle", slug, n))
}

// Put streams r into the blob file for version n, never buffering the whole
// payload in memory.
func (b *BlobFS) Put(slug string, n int32, r io.Reader) error {
	f, err := os.OpenFile(b.path(slug, n), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Close()
}

// Get opens version n's blob for streaming; the caller owns closing the reader.
func (b *BlobFS) Get(slug string, n int32) (io.ReadCloser, error) {
	return os.Open(b.path(slug, n))
}
