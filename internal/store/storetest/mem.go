// Package storetest provides an in-memory store.Store for tests.
package storetest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/dmtrkzntsv/ship/internal/store"
)

// MemObject is an object held by Mem.
type MemObject struct {
	Data         []byte
	Opts         store.PutOptions
	LastModified time.Time
}

// Mem is an in-memory store.Store. Fields may be inspected and mutated by tests.
type Mem struct {
	Objects  map[string]MemObject
	PutOrder []string // keys of successful Puts, in call order
	ReadOnly bool     // Put and Delete return ErrAccessDenied
	// FailPut / FailDelete, when set, are consulted per key; a non-nil result is returned as the error.
	FailPut    func(key string) error
	FailDelete func(key string) error
}

var _ store.Store = (*Mem)(nil)

func NewMem() *Mem { return &Mem{Objects: map[string]MemObject{}} }

func (m *Mem) List(_ context.Context, prefix string) ([]store.Object, error) {
	var out []store.Object
	for k, o := range m.Objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, store.Object{Key: k, Size: int64(len(o.Data)), LastModified: o.LastModified})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *Mem) ListDirs(_ context.Context, prefix string) ([]string, error) {
	seen := map[string]bool{}
	for k := range m.Objects {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		if name, _, found := strings.Cut(rest, "/"); found && name != "" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func (m *Mem) Get(_ context.Context, key string) (io.ReadCloser, error) {
	o, ok := m.Objects[key]
	if !ok {
		return nil, fmt.Errorf("get %s: %w", key, store.ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(o.Data)), nil
}

func (m *Mem) Put(_ context.Context, key string, body io.ReadSeeker, _ int64, o store.PutOptions) error {
	if m.ReadOnly {
		return fmt.Errorf("put %s: %w", key, store.ErrAccessDenied)
	}
	if m.FailPut != nil {
		if err := m.FailPut(key); err != nil {
			return err
		}
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.Objects[key] = MemObject{Data: data, Opts: o, LastModified: time.Now()}
	m.PutOrder = append(m.PutOrder, key)
	return nil
}

func (m *Mem) Delete(_ context.Context, keys []string) error {
	if m.ReadOnly {
		return fmt.Errorf("delete: %w", store.ErrAccessDenied)
	}
	for _, k := range keys {
		if m.FailDelete != nil {
			if err := m.FailDelete(k); err != nil {
				return err
			}
		}
		delete(m.Objects, k)
	}
	return nil
}
