package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMissing(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "state.json")}
	s, err := f.Load()
	require.NoError(t, err)
	require.NotNil(t, s.Services)
	assert.Empty(t, s.Services)
}

func TestLoadMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(p, []byte("{nope"), 0o644))
	_, err := File{Path: p}.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), p)
}

func TestLoadInitialisesNilRefs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"services":{"api":{}}}`), 0o644))
	s, err := File{Path: p}.Load()
	require.NoError(t, err)
	require.NotNil(t, s.Services["api"].Refs)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "sub", "state.json")}
	s := &State{Services: map[string]*Service{}, SelfChannel: "latest"}
	svc := s.Service("api")
	svc.CFZone = "zone"
	svc.Refs["api"] = &RefState{
		Unit: "api", Channel: "latest", Version: "v1", PrevVersion: "v0",
		InstalledAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	require.NoError(t, f.Save(s))

	fi, err := os.Stat(f.Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
	di, err := os.Stat(filepath.Dir(f.Path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), di.Mode().Perm())

	got, err := f.Load()
	require.NoError(t, err)
	assert.Equal(t, s, got)

	entries, err := os.ReadDir(filepath.Dir(f.Path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files left behind")
}

func TestLockExclusive(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "sub", "state.json")}
	unlock, err := f.Lock()
	require.NoError(t, err)

	_, err = f.Lock()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another ship run holds")
	assert.Contains(t, err.Error(), filepath.Join(filepath.Dir(f.Path), "state.lock"))

	unlock()
	unlock2, err := f.Lock()
	require.NoError(t, err)
	unlock2()
}

func TestUnits(t *testing.T) {
	s := &State{Services: map[string]*Service{}}
	svc := s.Service("api")
	svc.Refs["worker"] = &RefState{Unit: "api-worker"}
	svc.Refs["api"] = &RefState{Unit: "api"}
	assert.Equal(t, []string{"api", "api-worker"}, s.Units("api"))
	assert.Empty(t, s.Units("nope"))
}

func TestServiceCreatesOnce(t *testing.T) {
	s := &State{}
	a := s.Service("x")
	require.NotNil(t, a.Refs)
	assert.Same(t, a, s.Service("x"))
}
