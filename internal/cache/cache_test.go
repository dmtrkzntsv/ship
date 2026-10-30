package cache_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
