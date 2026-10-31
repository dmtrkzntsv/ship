// Package store abstracts the object storage (Cloudflare R2) ship publishes to.
package store

import (
	"context"
	"errors"
	"io"
	"time"
)

// Object describes a stored object.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// PutOptions are the per-object metadata set on upload.
type PutOptions struct{ CacheControl, ContentType string }

// Store is the set of object operations ship needs.
type Store interface {
	// List returns all objects under prefix recursively, sorted by key.
	List(ctx context.Context, prefix string) ([]Object, error)
	// ListDirs returns the immediate child "directories" under prefix (names without slash), sorted.
	ListDirs(ctx context.Context, prefix string) ([]string, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, o PutOptions) error
	Delete(ctx context.Context, keys []string) error
}

var (
	ErrNotFound     = errors.New("not found")
	ErrAccessDenied = errors.New("access denied")
)
