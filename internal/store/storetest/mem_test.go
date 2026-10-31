package storetest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ store.Store = (*storetest.Mem)(nil)

func put(t *testing.T, m *storetest.Mem, key, data string) {
	t.Helper()
	require.NoError(t, m.Put(context.Background(), key, bytes.NewReader([]byte(data)), int64(len(data)), store.PutOptions{CacheControl: "no-cache", ContentType: "application/octet-stream"}))
}

func TestMemListDirs(t *testing.T) {
	m := storetest.NewMem()
	for _, k := range []string{"econumo/v1/a", "econumo/latest/a", "billing/v1/a"} {
		put(t, m, k, "x")
	}
	got, err := m.ListDirs(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []string{"billing", "econumo"}, got)
	got, err = m.ListDirs(context.Background(), "econumo/")
	require.NoError(t, err)
	assert.Equal(t, []string{"latest", "v1"}, got)
}

func TestMemListSortedRecursive(t *testing.T) {
	m := storetest.NewMem()
	put(t, m, "b/2", "22")
	put(t, m, "a/1", "1")
	put(t, m, "c/3", "333")
	objs, err := m.List(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, objs, 3)
	assert.Equal(t, "a/1", objs[0].Key)
	assert.Equal(t, int64(1), objs[0].Size)
	assert.Equal(t, "c/3", objs[2].Key)
	assert.False(t, objs[0].LastModified.IsZero())
	objs, err = m.List(context.Background(), "b/")
	require.NoError(t, err)
	require.Len(t, objs, 1)
}

func TestMemPutGetDelete(t *testing.T) {
	m := storetest.NewMem()
	put(t, m, "k", "hello")
	assert.Equal(t, "no-cache", m.Objects["k"].Opts.CacheControl)
	assert.WithinDuration(t, time.Now(), m.Objects["k"].LastModified, time.Minute)
	rc, err := m.Get(context.Background(), "k")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	rc.Close()
	assert.Equal(t, "hello", string(b))
	require.NoError(t, m.Delete(context.Background(), []string{"k", "missing"}))
	_, err = m.Get(context.Background(), "k")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestMemGetMissing(t *testing.T) {
	_, err := storetest.NewMem().Get(context.Background(), "nope")
	assert.True(t, errors.Is(err, store.ErrNotFound))
}

func TestMemReadOnly(t *testing.T) {
	m := storetest.NewMem()
	put(t, m, "k", "v")
	m.ReadOnly = true
	err := m.Put(context.Background(), "k2", bytes.NewReader([]byte("v")), 1, store.PutOptions{})
	assert.ErrorIs(t, err, store.ErrAccessDenied)
	assert.ErrorIs(t, m.Delete(context.Background(), []string{"k"}), store.ErrAccessDenied)
	assert.Contains(t, m.Objects, "k")
}

func TestMemPutOrder(t *testing.T) {
	m := storetest.NewMem()
	put(t, m, "z", "1")
	put(t, m, "a", "1")
	put(t, m, "m", "1")
	assert.Equal(t, []string{"z", "a", "m"}, m.PutOrder)
}

func TestMemFailPutAndFailDelete(t *testing.T) {
	boom := errors.New("boom")
	m := storetest.NewMem()
	m.FailPut = func(key string) error {
		if key == "bad" {
			return boom
		}
		return nil
	}
	put(t, m, "good", "1")
	err := m.Put(context.Background(), "bad", bytes.NewReader([]byte("1")), 1, store.PutOptions{})
	assert.ErrorIs(t, err, boom)
	assert.NotContains(t, m.Objects, "bad")
	assert.NotContains(t, m.PutOrder, "bad")

	m.FailDelete = func(key string) error {
		if key == "good" {
			return boom
		}
		return nil
	}
	put(t, m, "other", "1")
	err = m.Delete(context.Background(), []string{"other", "good"})
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, m.Objects, "good")
}
