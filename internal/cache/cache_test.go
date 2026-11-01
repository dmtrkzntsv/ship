package cache_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/cache"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
	"github.com/dmtrkzntsv/ship/internal/sums"
)

func putRelease(t *testing.T, m *storetest.Mem, project, channel string, files ...string) {
	t.Helper()
	s := sums.Sums{}
	for _, f := range files {
		s[f] = "0000000000000000000000000000000000000000000000000000000000000000"
		m.Objects[project+"/"+channel+"/"+f] = storetest.MemObject{Data: []byte("x")}
	}
	m.Objects[project+"/"+channel+"/"+release.SumsFile] = storetest.MemObject{Data: s.Marshal()}
}

func TestRefreshComponents(t *testing.T) {
	m := storetest.NewMem()
	putRelease(t, m, "winterflow", "v1", "winterflow-hub.service", "winterflow-agent.service", "winterflow-hub-linux-amd64")
	putRelease(t, m, "winterflow", "latest", "winterflow-hub.service", "winterflow-agent.service")
	putRelease(t, m, "econumo", "v1", "econumo.service", "econumo-linux-amd64")
	putRelease(t, m, "econumo", "dev", "econumo.service")

	l, err := cache.Refresh(context.Background(), m)
	require.NoError(t, err)
	assert.Equal(t, []string{"latest", "v1"}, l.Projects["winterflow"].Channels)
	assert.Equal(t, []string{"agent", "hub"}, l.Projects["winterflow"].Components)
	assert.False(t, l.Projects["winterflow"].Root)
	assert.Equal(t, []string{"dev", "v1"}, l.Projects["econumo"].Channels)
	assert.Empty(t, l.Projects["econumo"].Components)
	assert.True(t, l.Projects["econumo"].Root)
}

func TestRefreshUsesNewestVersionWithoutLatest(t *testing.T) {
	m := storetest.NewMem()
	putRelease(t, m, "p", "v1", "p-old.service")
	putRelease(t, m, "p", "v2", "p-new.service")
	l, err := cache.Refresh(context.Background(), m)
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, l.Projects["p"].Components)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "listing.json")
	l := cache.Listing{FetchedAt: time.Unix(100, 0).UTC(), Projects: map[string]cache.ProjectEntry{"a": {Channels: []string{"dev"}}}}
	require.NoError(t, cache.Save(p, l))
	got, err := cache.Load(p)
	require.NoError(t, err)
	assert.Equal(t, l, got)
	fi, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
	_, err = cache.Load(filepath.Join(t.TempDir(), "none.json"))
	assert.Error(t, err)
}

func TestGetFallsBackToStale(t *testing.T) {
	p := filepath.Join(t.TempDir(), "listing.json")
	old := cache.Listing{FetchedAt: time.Unix(0, 0).UTC(), Projects: map[string]cache.ProjectEntry{"stale": {}}}
	require.NoError(t, cache.Save(p, old))
	got := cache.Get(context.Background(), p, time.Now(), func() (store.Store, error) { return nil, errors.New("no credentials") })
	assert.Contains(t, got.Projects, "stale")

	none := cache.Get(context.Background(), filepath.Join(t.TempDir(), "x.json"), time.Now(), func() (store.Store, error) { return nil, errors.New("no") })
	assert.Empty(t, none.Projects)
}

func TestGetFreshSkipsStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "listing.json")
	now := time.Now()
	require.NoError(t, cache.Save(p, cache.Listing{FetchedAt: now.Add(-time.Minute), Projects: map[string]cache.ProjectEntry{"fresh": {}}}))
	got := cache.Get(context.Background(), p, now, func() (store.Store, error) {
		t.Fatal("store must not be opened for a fresh cache")
		return nil, nil
	})
	assert.Contains(t, got.Projects, "fresh")
}

func TestGetRefreshesStaleAndSaves(t *testing.T) {
	p := filepath.Join(t.TempDir(), "listing.json")
	now := time.Now()
	require.NoError(t, cache.Save(p, cache.Listing{FetchedAt: now.Add(-time.Hour), Projects: map[string]cache.ProjectEntry{"stale": {}}}))
	m := storetest.NewMem()
	putRelease(t, m, "econumo", "dev", "econumo.service")
	got := cache.Get(context.Background(), p, now, func() (store.Store, error) { return m, nil })
	assert.Contains(t, got.Projects, "econumo")
	assert.NotContains(t, got.Projects, "stale")
	onDisk, err := cache.Load(p)
	require.NoError(t, err)
	assert.Contains(t, onDisk.Projects, "econumo")
}

func TestGetUnwritablePathStillReturnsFresh(t *testing.T) {
	m := storetest.NewMem()
	putRelease(t, m, "econumo", "dev", "econumo.service")
	// parent is a file, so Save fails
	blocker := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))
	got := cache.Get(context.Background(), filepath.Join(blocker, "l.json"), time.Now(), func() (store.Store, error) { return m, nil })
	assert.Contains(t, got.Projects, "econumo")
}

// faultyStore is a Mem whose ListDirs and Get can fail for chosen keys.
type faultyStore struct {
	*storetest.Mem
	listErr map[string]error // prefix -> error from ListDirs
	getErr  map[string]error // key -> error from Get
	badBody map[string]error // key -> error from reading the body Get returns
}

func (f *faultyStore) ListDirs(ctx context.Context, prefix string) ([]string, error) {
	if err := f.listErr[prefix]; err != nil {
		return nil, err
	}
	return f.Mem.ListDirs(ctx, prefix)
}

func (f *faultyStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := f.getErr[key]; err != nil {
		return nil, err
	}
	if err := f.badBody[key]; err != nil {
		return io.NopCloser(iotest.ErrReader(err)), nil
	}
	return f.Mem.Get(ctx, key)
}

func TestRefreshErrors(t *testing.T) {
	boom := errors.New("boom")
	sumsKey := "econumo/dev/" + release.SumsFile
	for name, f := range map[string]func(*faultyStore){
		"list projects": func(f *faultyStore) { f.listErr = map[string]error{"": boom} },
		"list channels": func(f *faultyStore) { f.listErr = map[string]error{"econumo/": boom} },
		"get sums":      func(f *faultyStore) { f.getErr = map[string]error{sumsKey: boom} },
		"read sums":     func(f *faultyStore) { f.badBody = map[string]error{sumsKey: boom} },
	} {
		t.Run(name, func(t *testing.T) {
			m := storetest.NewMem()
			putRelease(t, m, "econumo", "dev", "econumo.service")
			fs := &faultyStore{Mem: m}
			f(fs)
			_, err := cache.Refresh(context.Background(), fs)
			assert.ErrorIs(t, err, boom)
		})
	}
}

func TestRefreshToleratesOddContent(t *testing.T) {
	m := storetest.NewMem()
	// Not a valid project name: skipped.
	m.Objects["Bad Name/dev/x"] = storetest.MemObject{Data: []byte("x")}
	// Only a moving dev channel, without SHA256SUMS: listed without components.
	m.Objects["nosums/dev/x"] = storetest.MemObject{Data: []byte("x")}
	// A broken SHA256SUMS must not break the listing.
	m.Objects["broken/v1/"+release.SumsFile] = storetest.MemObject{Data: []byte("not a sums file\n")}
	// Only a dev channel: its SHA256SUMS gives the components.
	putRelease(t, m, "devonly", "dev", "devonly-api.service")

	l, err := cache.Refresh(context.Background(), m)
	require.NoError(t, err)
	assert.NotContains(t, l.Projects, "Bad Name")
	assert.Equal(t, cache.ProjectEntry{Channels: []string{"dev"}}, l.Projects["nosums"])
	assert.Equal(t, cache.ProjectEntry{Channels: []string{"v1"}}, l.Projects["broken"])
	assert.Equal(t, []string{"api"}, l.Projects["devonly"].Components)
	assert.False(t, l.FetchedAt.IsZero())
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{nope"), 0o644))
	_, err := cache.Load(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse "+bad)

	empty := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(empty, []byte(`{"fetched_at":"2026-01-01T00:00:00Z"}`), 0o644))
	l, err := cache.Load(empty)
	require.NoError(t, err)
	assert.NotNil(t, l.Projects)
	assert.Empty(t, l.Projects)
}

func TestSaveErrors(t *testing.T) {
	ok := cache.Listing{Projects: map[string]cache.ProjectEntry{}}
	t.Run("dir is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(blocker, nil, 0o644))
		assert.Error(t, cache.Save(filepath.Join(blocker, "l.json"), ok))
	})
	t.Run("unencodable", func(t *testing.T) {
		dir := t.TempDir()
		err := cache.Save(filepath.Join(dir, "l.json"), cache.Listing{FetchedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)})
		require.Error(t, err)
		entries, rerr := os.ReadDir(dir)
		require.NoError(t, rerr)
		assert.Empty(t, entries)
	})
	t.Run("read-only dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write anywhere")
		}
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		assert.Error(t, cache.Save(filepath.Join(dir, "l.json"), ok))
	})
	t.Run("target is a directory", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "l.json")
		require.NoError(t, os.MkdirAll(filepath.Join(p, "keep"), 0o755))
		require.Error(t, cache.Save(p, ok))
		entries, rerr := os.ReadDir(dir)
		require.NoError(t, rerr)
		assert.Len(t, entries, 1, "the temp file is removed")
	})
}

func TestGetKeepsDiskWhenRefreshFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "listing.json")
	old := cache.Listing{FetchedAt: time.Unix(0, 0).UTC(), Projects: map[string]cache.ProjectEntry{"stale": {}}}
	require.NoError(t, cache.Save(p, old))
	fs := &faultyStore{Mem: storetest.NewMem(), listErr: map[string]error{"": errors.New("offline")}}
	got := cache.Get(context.Background(), p, time.Now(), func() (store.Store, error) { return fs, nil })
	assert.Equal(t, old, got)
	onDisk, err := cache.Load(p)
	require.NoError(t, err)
	assert.Equal(t, old, onDisk, "a failed refresh does not overwrite the file")
}
