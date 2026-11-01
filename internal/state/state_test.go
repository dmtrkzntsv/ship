package state

import (
	"os"
	"path/filepath"
	"syscall"
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

func TestLoadNormalisesNullsAndMissingServices(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty object":  `{}`,
		"null service":  `{"services":{"api":null}}`,
		"null services": `{"services":null,"self_channel":"dev"}`,
	} {
		p := filepath.Join(dir, "state.json")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		s, err := File{Path: p}.Load()
		require.NoError(t, err, name)
		require.NotNil(t, s.Services, name)
		for svcName, svc := range s.Services {
			require.NotNil(t, svc, "%s: %s", name, svcName)
			assert.NotNil(t, svc.Refs, "%s: %s", name, svcName)
		}
	}
}

func TestLoadUnreadable(t *testing.T) {
	// A directory where the file should be is a read error, not "missing".
	p := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.Mkdir(p, 0o755))
	_, err := File{Path: p}.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read state "+p)
}

func TestSaveErrors(t *testing.T) {
	s := &State{Services: map[string]*Service{}}

	t.Run("dir is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(blocker, nil, 0o644))
		err := File{Path: filepath.Join(blocker, "state.json")}.Save(s)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create state dir")
	})
	t.Run("read-only dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write anywhere")
		}
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		err := File{Path: filepath.Join(dir, "state.json")}.Save(s)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp state file")
	})
	t.Run("unencodable", func(t *testing.T) {
		bad := &State{Services: map[string]*Service{"api": {Refs: map[string]*RefState{
			"api": {Unit: "api", InstalledAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		}}}}
		dir := t.TempDir()
		err := File{Path: filepath.Join(dir, "state.json")}.Save(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "encode state")
		entries, rerr := os.ReadDir(dir)
		require.NoError(t, rerr)
		assert.Empty(t, entries)
	})
	t.Run("write fails", func(t *testing.T) {
		// RLIMIT_FSIZE makes the write fail with EFBIG; the Go runtime
		// ignores the SIGXFSZ that comes with it. No other test in this
		// package runs in parallel, so lowering the limit is contained.
		var lim syscall.Rlimit
		require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_FSIZE, &lim))
		low := lim
		low.Cur = 8
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &low))
		dir := t.TempDir()
		p := filepath.Join(dir, "state.json")
		err := File{Path: p}.Save(&State{Services: map[string]*Service{}, SelfChannel: "latest"})
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write state ")
		assert.ErrorIs(t, err, syscall.EFBIG)
		entries, rerr := os.ReadDir(dir)
		require.NoError(t, rerr)
		assert.Empty(t, entries, "the temp file is removed and no state is written")
	})
	t.Run("target is a directory", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "state.json")
		require.NoError(t, os.MkdirAll(filepath.Join(p, "keep"), 0o755))
		err := File{Path: p}.Save(s)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "replace state "+p)
		entries, rerr := os.ReadDir(dir)
		require.NoError(t, rerr)
		assert.Len(t, entries, 1, "the temp file is removed")
	})
}

func TestLockErrors(t *testing.T) {
	t.Run("dir is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(blocker, nil, 0o644))
		_, err := File{Path: filepath.Join(blocker, "state.json")}.Lock()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create lock dir")
	})
	t.Run("lock path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "state.lock"), 0o755))
		_, err := File{Path: filepath.Join(dir, "state.json")}.Lock()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "open lock "+filepath.Join(dir, "state.lock"))
	})
}
