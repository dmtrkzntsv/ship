package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/state"
)

const legacyUnit = "[Service]\nUser=econumo-beta\nEnvironmentFile=/etc/econumo-beta/env\nExecStart=/opt/econumo-beta/econumo\n"

// legacyFixture lays out service as the old deploy script left it for project.
func legacyFixture(t *testing.T, e *env, service, project string) {
	t.Helper()
	p := e.app.Host.Paths
	for path, content := range map[string]string{
		p.UnitPath(service):                     legacyUnit,
		p.Bin(service, project):                 "script-binary",
		filepath.Join(p.EnvDir(service), "env"): "A=1",
		filepath.Join(p.DataDir(service), "db"): "data",
		p.Bin(service, project) + ".prev":       "script-prev",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

func adoptArgs() app.AdoptArgs {
	return app.AdoptArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", CFZone: "z1"}
}

func TestAdoptMovesEnvAndSwaps(t *testing.T) {
	e := newEnv(t)
	legacyFixture(t, e, "econumo-beta", "econumo")
	p := e.app.Host.Paths

	require.NoError(t, e.app.Adopt(context.Background(), adoptArgs()))

	assert.Equal(t, "A=1", readFile(t, filepath.Join(e.root, "etc/econumo-beta/econumo.env")))
	assert.NoFileExists(t, filepath.Join(e.root, "etc/econumo-beta/env"))
	assert.Equal(t, "[Service]\nUser=econumo-beta\nEnvironmentFile=/etc/econumo-beta/econumo.env\nExecStart=/opt/econumo-beta/econumo\n",
		readFile(t, p.UnitPath("econumo-beta")))
	assert.Equal(t, "econumo-amd64", readFile(t, p.Bin("econumo-beta", "econumo")))
	assert.Equal(t, "script-binary", readFile(t, p.Bin("econumo-beta", "econumo")+".prev"))
	assert.Equal(t, legacyUnit, readFile(t, p.UnitBackup("econumo-beta", "econumo-beta")))
	assert.Equal(t, "data", readFile(t, filepath.Join(p.DataDir("econumo-beta"), "db")))
	assert.Equal(t, "d /run/econumo-beta 0750 econumo-beta econumo-beta -\n", readFile(t, p.Tmpfiles("econumo-beta")))

	assert.Contains(t, e.sys.Calls, "CreateTmpfiles "+p.Tmpfiles("econumo-beta"))
	assert.Contains(t, e.sys.Calls, "DaemonReload")
	assert.Contains(t, e.sys.Calls, "systemctl restart econumo-beta")
	assert.Contains(t, e.sys.Calls, "IsActive econumo-beta")

	svc := e.state(t).Services["econumo-beta"]
	require.NotNil(t, svc)
	assert.Equal(t, "z1", svc.CFZone)
	assert.Equal(t, &state.RefState{Unit: "econumo-beta", Channel: "dev", Version: "1.4.0", PublishedAt: fixedNow, InstalledAt: fixedNow}, svc.Refs["econumo"])
	assert.Contains(t, e.out.String(), "/etc/econumo-beta/econumo.env")
	assert.NotContains(t, e.out.String(), e.root)
}

func TestAdoptRollsBackEverything(t *testing.T) {
	e := newEnv(t)
	legacyFixture(t, e, "econumo-beta", "econumo")
	p := e.app.Host.Paths
	before := files(t, e.root)
	e.sys.ActiveAfterRestart = func(string) bool {
		b, err := os.ReadFile(p.Bin("econumo-beta", "econumo"))
		require.NoError(t, err)
		return string(b) != "econumo-amd64"
	}
	e.sys.Journal = "panic: boom"

	err := e.app.Adopt(context.Background(), adoptArgs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "econumo-beta failed to come up")
	assert.Contains(t, err.Error(), "panic: boom")

	assert.Equal(t, "script-binary", readFile(t, p.Bin("econumo-beta", "econumo")))
	assert.Equal(t, "script-prev", readFile(t, p.Bin("econumo-beta", "econumo")+".prev"))
	assert.Equal(t, legacyUnit, readFile(t, p.UnitPath("econumo-beta")))
	assert.Equal(t, "A=1", readFile(t, filepath.Join(e.root, "etc/econumo-beta/env")))
	assert.NoFileExists(t, filepath.Join(e.root, "etc/econumo-beta/econumo.env"))
	assert.NoFileExists(t, p.Tmpfiles("econumo-beta"))
	assert.Equal(t, before, files(t, e.root))
	assert.Equal(t, 2, countCalls(e.sys.Calls, "systemctl restart econumo-beta"))
	assert.Empty(t, e.state(t).Services)
}

func TestAdoptRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("no legacy layout", func(t *testing.T) {
		e := newEnv(t)
		err := e.app.Adopt(ctx, adoptArgs())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "old deploy script")
		assert.Empty(t, e.sys.Calls)
		assert.Empty(t, files(t, e.root))
	})
	t.Run("already in state", func(t *testing.T) {
		e := newEnv(t)
		legacyFixture(t, e, "econumo-beta", "econumo")
		s := &state.State{Services: map[string]*state.Service{}}
		s.Service("econumo-beta").Refs["other"] = &state.RefState{Unit: "econumo-beta-other", Channel: "dev", Version: "1"}
		require.NoError(t, e.app.State.Save(s))
		before := files(t, e.root)

		err := e.app.Adopt(ctx, adoptArgs())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already managed by ship")
		assert.Empty(t, e.sys.Calls)
		assert.Equal(t, before, files(t, e.root))
	})
	t.Run("no unit template", func(t *testing.T) {
		e := newEnv(t)
		legacyFixture(t, e, "mytool", "mytool")
		before := files(t, e.root)

		err := e.app.Adopt(ctx, app.AdoptArgs{Ref: "mytool", Version: "v1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "publishes no systemd unit")
		assert.Empty(t, e.sys.Calls)
		assert.Equal(t, before, files(t, e.root))
		assert.Empty(t, e.state(t).Services)
	})
	t.Run("component ref", func(t *testing.T) {
		e := newEnv(t)
		err := e.app.Adopt(ctx, app.AdoptArgs{Ref: "winterflow/hub", Service: "wf", Version: "v3"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "adopt supports plain projects only")
		assert.Empty(t, e.sys.Calls)
	})
	t.Run("bad args", func(t *testing.T) {
		e := newEnv(t)
		for _, args := range []app.AdoptArgs{
			{Ref: "econumo", Service: "econumo-beta"},
			{Ref: "econumo", Service: "econumo-beta", Version: "../x"},
			{Ref: "econumo", Service: "Bad", Version: "dev"},
		} {
			require.Error(t, e.app.Adopt(ctx, args), args)
		}
		assert.Empty(t, e.sys.Calls)
	})
}

func TestInstallPointsToAdopt(t *testing.T) {
	e := newEnv(t)
	legacyFixture(t, e, "econumo-beta", "econumo")
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ship adopt econumo --service econumo-beta")
}

func countCalls(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}
