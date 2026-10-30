package app_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
)

var fixedNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

const unitTmpl = "[Service]\nUser={{USER}}\nEnvironmentFile={{ENV_FILE}}\nExecStart={{BIN}}\n"

// publish uploads project with the given assets (each with amd64 and arm64
// binaries; withUnit adds a unit template) as version and channels.
func publish(t *testing.T, st *storetest.Mem, project, version string, channels []string, withUnit bool, assets ...string) {
	t.Helper()
	publishAt(t, st, fixedNow, project, version, channels, withUnit, assets...)
}

// publishAt is publish with the given publish time.
func publishAt(t *testing.T, st *storetest.Mem, at time.Time, project, version string, channels []string, withUnit bool, assets ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, a := range assets {
		for _, arch := range []string{"amd64", "arm64"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, a+"-linux-"+arch), []byte(a+"-"+arch), 0o755))
		}
		if withUnit {
			require.NoError(t, os.WriteFile(filepath.Join(dir, a+".service"), []byte(unitTmpl), 0o644))
		}
	}
	require.NoError(t, release.Publish(context.Background(), st, release.PublishOptions{
		Project: project, Dir: dir, Version: version, Channels: channels, Keep: 0,
		Now: func() time.Time { return at },
	}))
}

func fixtureStore(t *testing.T) *storetest.Mem {
	t.Helper()
	st := storetest.NewMem()
	publish(t, st, "econumo", "1.4.0", []string{"dev"}, true, "econumo")
	publish(t, st, "twillingate", "v2", nil, true, "twillingate")
	publish(t, st, "twillingate-sql", "v5", nil, true, "twillingate-sql")
	publish(t, st, "winterflow", "v3", nil, true, "winterflow-hub", "winterflow-agent")
	publish(t, st, "mytool", "v1", nil, false, "mytool")
	return st
}

type env struct {
	app   *app.App
	sys   *host.FakeSys
	root  string
	out   *bytes.Buffer
	store *storetest.Mem
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := fixtureStore(t)
	sys := &host.FakeSys{}
	root := t.TempDir()
	out := &bytes.Buffer{}
	a := &app.App{
		Out: out, Err: &bytes.Buffer{},
		NewStore: func() (store.Store, error) { return st, nil },
		State:    state.File{Path: filepath.Join(t.TempDir(), "state.json")},
		Host:     &host.Host{Paths: host.Paths{Root: root}, Sys: sys, Out: out},
		Arch:     "amd64",
		Now:      func() time.Time { return fixedNow },
	}
	return &env{app: a, sys: sys, root: root, out: out, store: st}
}

func (e *env) state(t *testing.T) *state.State {
	t.Helper()
	s, err := e.app.State.Load()
	require.NoError(t, err)
	return s
}

// files lists every regular file under root, relative to it.
func files(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	}))
	return out
}

func TestAppInstallRecordsState(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", CFZone: "z1"}))

	svc := e.state(t).Services["econumo-beta"]
	require.NotNil(t, svc)
	assert.Equal(t, "z1", svc.CFZone)
	assert.Equal(t, &state.RefState{Unit: "econumo-beta", Channel: "dev", Version: "1.4.0", PublishedAt: fixedNow, InstalledAt: fixedNow}, svc.Refs["econumo"])

	p := e.app.Host.Paths
	assert.FileExists(t, p.Bin("econumo-beta", "econumo"))
	assert.FileExists(t, p.UnitPath("econumo-beta"))
	assert.Contains(t, e.sys.Calls, "systemctl enable econumo-beta")

	out := e.out.String()
	assert.Contains(t, out, "/etc/econumo-beta/econumo.env")
	assert.NotContains(t, out, e.root)
	assert.Contains(t, out, "ship start econumo --service econumo-beta")
}

func TestAppInstallSecondRefUnitName(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate-sql", Service: "twillingate", Version: "v5"}))

	s := e.state(t)
	assert.Equal(t, []string{"twillingate", "twillingate-sql"}, s.Units("twillingate"))
	assert.Equal(t, "twillingate-sql", s.Services["twillingate"].Refs["twillingate-sql"].Unit)
	assert.Equal(t, "v5", s.Services["twillingate"].Refs["twillingate-sql"].Version)
	p := e.app.Host.Paths
	assert.FileExists(t, p.UnitPath("twillingate-sql"))
	assert.FileExists(t, p.Bin("twillingate", "twillingate-sql"))
	assert.Equal(t, filepath.Join(e.root, "etc/twillingate/twillingate-sql.env"), p.EnvFile("twillingate", "twillingate-sql"))
	assert.FileExists(t, p.EnvFile("twillingate", "twillingate-sql"))
	assert.Contains(t, e.out.String(), "/etc/twillingate/twillingate-sql.env")
}

func TestAppInstallSecondComponentUnitName(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "winterflow/hub", Service: "wf", Version: "v3"}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "winterflow/agent", Service: "wf", Version: "v3"}))

	s := e.state(t)
	assert.Equal(t, []string{"wf", "wf-agent"}, s.Units("wf"))
	assert.Equal(t, "wf-agent", s.Services["wf"].Refs["winterflow/agent"].Unit)
	p := e.app.Host.Paths
	assert.FileExists(t, p.UnitPath("wf-agent"))
	assert.FileExists(t, p.Bin("wf", "winterflow-agent"))
	assert.FileExists(t, p.EnvFile("wf", "agent"))
}

func TestAppInstallDefaultService(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "winterflow/hub", Version: "v3"}))
	s := e.state(t)
	require.Contains(t, s.Services, "winterflow")
	assert.Equal(t, "winterflow", s.Services["winterflow"].Refs["winterflow/hub"].Unit)
	assert.Empty(t, s.Services["winterflow"].CFZone)
	assert.Contains(t, e.out.String(), "ship start winterflow/hub --service winterflow")
}

func TestAppInstallRefusesDuplicate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Version: "dev"}))
	calls := len(e.sys.Calls)
	err := e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Version: "1.4.0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already installed")
	assert.Len(t, e.sys.Calls, calls)
	assert.Equal(t, "dev", e.state(t).Services["econumo"].Refs["econumo"].Channel)
}

func TestInstallRefusesForeignUnit(t *testing.T) {
	write := func(path string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	}
	t.Run("legacy layout", func(t *testing.T) {
		e := newEnv(t)
		p := e.app.Host.Paths
		write(p.UnitPath("econumo"))
		write(p.Bin("econumo", "econumo"))
		write(filepath.Join(p.EnvDir("econumo"), "env"))
		before := files(t, e.root)

		err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ship adopt econumo --service econumo")
		assert.Equal(t, before, files(t, e.root))
		assert.Empty(t, e.sys.Calls)
		assert.Empty(t, e.state(t).Services)
	})
	t.Run("other unit", func(t *testing.T) {
		e := newEnv(t)
		write(e.app.Host.Paths.UnitPath("econumo-beta"))
		before := files(t, e.root)

		err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove it first")
		assert.NotContains(t, err.Error(), "adopt")
		assert.Equal(t, before, files(t, e.root))
		assert.Empty(t, e.sys.Calls)
		assert.Empty(t, e.state(t).Services)
	})
}

func TestInstallFetchErrorTouchesNothing(t *testing.T) {
	e := newEnv(t)
	key := "econumo/dev/econumo-linux-amd64"
	o := e.store.Objects[key]
	o.Data = append([]byte("x"), o.Data...)
	e.store.Objects[key] = o

	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
	assert.Empty(t, files(t, e.root))
	assert.Empty(t, e.sys.Calls)
	assert.Empty(t, e.state(t).Services)
}

func TestAppInstallToolProject(t *testing.T) {
	e := newEnv(t)
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "mytool", Version: "v1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, release.ErrNoUnit)
	assert.Contains(t, err.Error(), "publishes no systemd unit; it cannot be installed as a service")
	assert.Empty(t, files(t, e.root))
	assert.Empty(t, e.sys.Calls)
}

func TestAppInstallRequiresVersion(t *testing.T) {
	e := newEnv(t)
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
	assert.Empty(t, e.sys.Calls)
}

func TestAppInstallRejectsBadNames(t *testing.T) {
	e := newEnv(t)
	for _, args := range []app.InstallArgs{
		{Ref: "Econumo", Version: "dev"},
		{Ref: "econumo", Service: "bad/svc", Version: "dev"},
		{Ref: "econumo", Version: "../x"},
	} {
		require.Error(t, e.app.Install(context.Background(), args), args)
	}
	assert.Empty(t, e.sys.Calls)
	assert.Empty(t, files(t, e.root))
}
