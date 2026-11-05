// Package storage abstracts where media bytes live. Today: local disk.
// Tomorrow: S3 / R2 / a video host — implement Storage (and optionally
// URLSigner so the API can redirect clients straight to a CDN).
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

var ErrNotFound = errors.New("object not found")

type Object struct {
	io.ReadSeekCloser
	Size    int64
	ModTime time.Time
}

type Storage interface {
	// Put streams r into key, returning the number of bytes written.
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	// PutFile moves/copies a local file into key.
	PutFile(ctx context.Context, key, localPath string) (int64, error)
	Open(ctx context.Context, key string) (*Object, error)
	Delete(ctx context.Context, key string) error
	// LocalPath returns a filesystem path for key (downloading it first for
	// remote backends). release must be called when done.
	LocalPath(ctx context.Context, key string) (p string, release func(), err error)
}

// URLSigner is implemented by backends that can hand out time-limited direct
// URLs (S3 presigned URLs, CDN signed URLs).
type URLSigner interface {
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// NewID returns a random identifier for building storage keys.
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Local stores objects under a root directory.
type Local struct {
	root string
	tmp  string
}

func NewLocal(root string) (*Local, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	l := &Local{root: filepath.Join(abs, "media"), tmp: filepath.Join(abs, "tmp")}
	for _, d := range []string{l.root, l.tmp} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// TempDir is a scratch directory on the same filesystem (cheap renames).
func (l *Local) TempDir() string { return l.tmp }

func (l *Local) path(key string) (string, error) {
	clean := path.Clean("/" + key)
	if clean == "/" || strings.Contains(key, "..") || strings.ContainsRune(key, 0) {
		return "", fmt.Errorf("invalid storage key %q", key)
	}
	return filepath.Join(l.root, filepath.FromSlash(clean)), nil
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	dst, err := l.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(l.tmp, "upload-*")
	if err != nil {
		return 0, err
	}
	tmpName := f.Name()
	n, err := io.Copy(f, ctxReader{ctx, r})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return n, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return n, err
	}
	return n, nil
}

func (l *Local) PutFile(ctx context.Context, key, localPath string) (int64, error) {
	dst, err := l.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, err
	}
	if err := os.Rename(localPath, dst); err == nil {
		st, err := os.Stat(dst)
		if err != nil {
			return 0, err
		}
		return st.Size(), nil
	}
	f, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := l.Put(ctx, key, f)
	if err == nil {
		os.Remove(localPath)
	}
	return n, err
}

func (l *Local) Open(_ context.Context, key string) (*Object, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Object{ReadSeekCloser: f, Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Remove now-empty parent directories up to the root (best effort).
	for dir := filepath.Dir(p); dir != l.root && strings.HasPrefix(dir, l.root); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}

func (l *Local) LocalPath(_ context.Context, key string) (string, func(), error) {
	p, err := l.path(key)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, ErrNotFound
		}
		return "", nil, err
	}
	return p, func() {}, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
