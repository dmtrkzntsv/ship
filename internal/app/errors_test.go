package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// breakLock makes the state lock unopenable (state.lock is a directory).
func breakLock(t *testing.T, e *env) {
	t.Helper()
	lock := filepath.Join(filepath.Dir(e.app.State.Path), "state.lock")
	require.NoError(t, os.RemoveAll(lock))
	require.NoError(t, os.MkdirAll(lock, 0o755))
}

// corruptState replaces the state file with something that does not parse.
func corruptState(t *testing.T, e *env) {
	t.Helper()
	require.NoError(t, os.WriteFile(e.app.State.Path, []byte("{nope"), 0o644))
}

// readOnlyStateDir makes saving the state fail (no temp file can be created
// next to it) while loading and locking still work.
func readOnlyStateDir(t *testing.T, e *env) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := filepath.Dir(e.app.State.Path)
	// Lock creates state.lock; make sure it exists before the dir goes read-only.
	unlock, err := e.app.State.Lock()
	require.NoError(t, err)
	unlock()
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestCommandsReportLockAndStateErrors(t *testing.T) {
	ctx := context.Background()
	cmds := map[string]func(a *app.App) error{
		"install": func(a *app.App) error {
			return a.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"})
		},
		"uninstall": func(a *app.App) error {
			return a.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Service: "econumo-beta"})
		},
		"update":   func(a *app.App) error { return a.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}) },
		"rollback": func(a *app.App) error { return a.Rollback(ctx, "econumo", "econumo-beta") },
	}
	for name, run := range cmds {
		t.Run(name+"/lock", func(t *testing.T) {
			e := installedEnv(t)
			breakLock(t, e)
			err := run(e.app)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "open lock")
			assert.Empty(t, e.sys.Calls)
		})
		t.Run(name+"/state", func(t *testing.T) {
			e := installedEnv(t)
			corruptState(t, e)
			err := run(e.app)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "parse state")
			assert.Empty(t, e.sys.Calls)
		})
	}

	readers := map[string]func(a *app.App) error{
		"status":    func(a *app.App) error { return a.Status(ctx, "") },
		"lifecycle": func(a *app.App) error { return a.Lifecycle(ctx, "start", "econumo", "econumo-beta") },
		"logs": func(a *app.App) error {
			_, err := a.LogsArgs("econumo", "econumo-beta", 10, false)
			return err
		},
		"purge-check": func(a *app.App) error {
			return a.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
		},
	}
	for name, run := range readers {
		t.Run(name+"/state", func(t *testing.T) {
			e := installedEnv(t)
			corruptState(t, e)
			err := run(e.app)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "parse state")
			assert.Empty(t, e.sys.Calls)
		})
	}
}

func TestCommandsRejectBadRefs(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"uninstall": e.app.Uninstall(ctx, app.UninstallArgs{Ref: "Bad"}),
		"rollback":  e.app.Rollback(ctx, "econumo", "Bad"),
		"lifecycle": e.app.Lifecycle(ctx, "stop", "a/b/c", ""),
	} {
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "invalid", name)
	}
	_, err := e.app.RunArgs("Bad", "", nil, false)
	assert.Error(t, err)
	assert.Empty(t, e.sys.Calls)
}

func TestInstallErrors(t *testing.T) {
	ctx := context.Background()
	args := app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}

	t.Run("store", func(t *testing.T) {
		e := newEnv(t)
		e.app.NewStore = func() (store.Store, error) { return nil, errors.New("R2 endpoint is not set") }
		err := e.app.Install(ctx, args)
		require.EqualError(t, err, "R2 endpoint is not set")
		assert.Empty(t, e.sys.Calls)
		assert.Empty(t, e.state(t).Services)
	})
	t.Run("temp dir", func(t *testing.T) {
		e := newEnv(t)
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		require.Error(t, e.app.Install(ctx, args))
		assert.Empty(t, e.sys.Calls)
		assert.Empty(t, e.state(t).Services)
	})
	t.Run("host", func(t *testing.T) {
		e := newEnv(t)
		e.sys.EnableErr = errors.New("enable failed")
		err := e.app.Install(ctx, args)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "enable failed")
		assert.Empty(t, e.state(t).Services, "a failed install is not recorded")
	})
	t.Run("save", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
		readOnlyStateDir(t, e)
		err := e.app.Install(ctx, args)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp state file")
		assert.NotContains(t, e.out.String(), "installed econumo")
	})
	t.Run("unparsable ref in state is ignored", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
		s := e.state(t)
		s.Services["twillingate"].Refs["Not/A/Ref"] = s.Services["twillingate"].Refs["twillingate"]
		delete(s.Services["twillingate"].Refs, "twillingate")
		s.Services["twillingate"].Refs["Not/A/Ref"].Unit = "twillingate-old"
		require.NoError(t, e.app.State.Save(s))
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
		refs := e.state(t).Services["twillingate"].Refs
		assert.Contains(t, refs, "Not/A/Ref")
		require.Contains(t, refs, "twillingate")
		assert.Equal(t, "v2", refs["twillingate"].Version)
	})
}

func TestUninstallErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("host", func(t *testing.T) {
		e := installedEnv(t)
		e.sys.ReloadErr = errors.New("reload failed")
		err := e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Service: "econumo-beta"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reload failed")
		assert.Contains(t, e.state(t).Services, "econumo-beta", "state is kept when the host step fails")
		assert.Empty(t, e.out.String())
	})
	t.Run("save", func(t *testing.T) {
		e := installedEnv(t)
		readOnlyStateDir(t, e)
		err := e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Service: "econumo-beta"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp state file")
		assert.NotContains(t, e.out.String(), "uninstalled")
	})
	t.Run("purge", func(t *testing.T) {
		e := zonedEnv(t)
		cf := withCF(t, e)
		cf.reply = `{"success":false}`
		err := e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall of econumo-beta succeeded but the cache purge failed")
		assert.NotContains(t, e.state(t).Services, "econumo-beta")
		assert.Equal(t, []string{"/zones/z1/purge_cache"}, cf.paths)
	})
}

func TestLifecycleRunError(t *testing.T) {
	e := installedEnv(t)
	e.sys.Fail = map[string]error{"systemctl stop econumo-beta": errors.New("stop failed")}
	err := e.app.Lifecycle(context.Background(), "stop", "econumo", "econumo-beta")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop failed")
	assert.Empty(t, e.out.String())
}

func TestStatusUnknownService(t *testing.T) {
	e := installedEnv(t)
	err := e.app.Status(context.Background(), "nosuch")
	require.EqualError(t, err, "service nosuch is not installed")
	assert.Empty(t, e.out.String())
}

func TestUpdateErrors(t *testing.T) {
	ctx := context.Background()
	args := app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}

	t.Run("store", func(t *testing.T) {
		e := installedEnv(t)
		e.app.NewStore = func() (store.Store, error) { return nil, errors.New("no store") }
		require.EqualError(t, e.app.Update(ctx, args), "no store")
		assert.Empty(t, e.sys.Calls)
	})
	t.Run("temp dir", func(t *testing.T) {
		e := installedEnv(t)
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		require.Error(t, e.app.Update(ctx, args))
		assert.Empty(t, e.sys.Calls)
	})
	t.Run("missing channel", func(t *testing.T) {
		e := installedEnv(t)
		err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "v9"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `channel "v9" not found for econumo`)
		assert.Equal(t, "dev", e.state(t).Services["econumo-beta"].Refs["econumo"].Channel)
		assert.Empty(t, e.sys.Calls)
	})
	t.Run("no unit", func(t *testing.T) {
		e := installedEnv(t)
		publish(t, e.store, "econumo", "1.6.0", []string{"dev"}, false, "econumo")
		err := e.app.Update(ctx, args)
		require.ErrorIs(t, err, release.ErrNoUnit)
		assert.Equal(t, "1.4.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
		assert.Empty(t, e.sys.Calls)
	})
	t.Run("save after update", func(t *testing.T) {
		e := installedEnv(t)
		readOnlyStateDir(t, e)
		err := e.app.Update(ctx, args)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp state file")
		assert.Contains(t, e.sys.Calls, "systemctl restart econumo-beta")
		assert.Empty(t, e.out.String())
	})
	t.Run("save when already on", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}))
		e.out.Reset()
		readOnlyStateDir(t, e)
		err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", CFZone: "z2"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp state file")
		assert.Empty(t, e.out.String())
		assert.Empty(t, e.state(t).Services["econumo-beta"].CFZone)
	})
}

func TestRollbackErrors(t *testing.T) {
	ctx := context.Background()
	updated := func(t *testing.T) *env {
		t.Helper()
		e := installedEnv(t)
		require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
		e.out.Reset()
		e.sys.Calls = nil
		return e
	}
	t.Run("host", func(t *testing.T) {
		e := updated(t)
		e.sys.ReloadErr = errors.New("reload failed")
		err := e.app.Rollback(ctx, "econumo", "econumo-beta")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reload failed")
		assert.Equal(t, "1.5.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
		assert.Empty(t, e.out.String())
	})
	t.Run("save", func(t *testing.T) {
		e := updated(t)
		readOnlyStateDir(t, e)
		err := e.app.Rollback(ctx, "econumo", "econumo-beta")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp state file")
		assert.Equal(t, "1.5.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
		assert.Empty(t, e.out.String())
	})
}

func TestPurgeCheckForServiceNotInState(t *testing.T) {
	ctx := context.Background()
	t.Run("no zone falls through to not installed", func(t *testing.T) {
		e := newEnv(t)
		withCF(t, e)
		err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not installed")
	})
	t.Run("zone given still needs a token", func(t *testing.T) {
		e := newEnv(t)
		err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true, CFZone: "z1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no Cloudflare API token")
	})
	t.Run("zone and token given", func(t *testing.T) {
		e := newEnv(t)
		withCF(t, e)
		err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true, CFZone: "z1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not installed")
	})
}
