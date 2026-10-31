package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func TestInstallRefusesExistingPaths(t *testing.T) {
	for name, tc := range map[string]struct {
		path func(e *env) string
		want string
	}{
		"etc":      {func(e *env) string { return e.app.Host.Paths.EnvDir("econumo") }, "/etc/econumo"},
		"opt":      {func(e *env) string { return e.app.Host.Paths.BinDir("econumo") }, "/opt/econumo"},
		"data dir": {func(e *env) string { return e.app.Host.Paths.DataDir("econumo") }, "/var/lib/econumo"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			require.NoError(t, os.MkdirAll(tc.path(e), 0o755))
			before := files(t, e.root)

			err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want+" already exists")
			assert.Contains(t, err.Error(), "ship won't take over existing paths")
			assert.NotContains(t, err.Error(), e.root)
			assert.Equal(t, before, files(t, e.root))
			assert.Empty(t, e.sys.Calls)
			assert.Empty(t, e.state(t).Services)
		})
	}
}

func TestInstallKeepsDataDirOwnedByServiceUser(t *testing.T) {
	// Left behind by `ship uninstall` without --purge: the data dir and
	// the user stay, and a new install picks them up again.
	e := newEnv(t)
	data := e.app.Host.Paths.DataDir("econumo")
	require.NoError(t, os.MkdirAll(data, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(data, "db"), []byte("keep"), 0o640))
	e.sys.Users = map[string]bool{"econumo": true}
	e.sys.Owners = map[string]string{data: "econumo:econumo"}

	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"}))
	assert.Equal(t, "keep", readFile(t, filepath.Join(data, "db")))
	assert.False(t, e.state(t).Services["econumo"].CreatedUser)
}

func TestInstallKeepsEnvDirOfServiceGroup(t *testing.T) {
	// Left behind by `ship uninstall` without --purge: /etc/S (root:S) with
	// the env file and any hand-placed secrets is picked up again.
	e := newEnv(t)
	etc := e.app.Host.Paths.EnvDir("econumo")
	env := e.app.Host.Paths.EnvFile("econumo", "econumo")
	require.NoError(t, os.MkdirAll(etc, 0o750))
	require.NoError(t, os.WriteFile(env, []byte("SECRET=1\n"), 0o640))
	e.sys.Users = map[string]bool{"econumo": true}
	e.sys.Owners = map[string]string{etc: "root:econumo", env: "root:econumo"}

	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"}))
	assert.Equal(t, "SECRET=1\n", readFile(t, env))
}

func TestInstallRefusesEnvDirOfOtherGroup(t *testing.T) {
	e := newEnv(t)
	etc := e.app.Host.Paths.EnvDir("econumo")
	require.NoError(t, os.MkdirAll(etc, 0o750))
	e.sys.Users = map[string]bool{"econumo": true}
	e.sys.Owners = map[string]string{etc: "root:root"}
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/etc/econumo already exists")
}

func TestUninstallThenReinstallKeepsConfig(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Version: "dev"}))
	env := e.app.Host.Paths.EnvFile("econumo", "econumo")
	require.NoError(t, os.WriteFile(env, []byte("SECRET=1\n"), 0o640))
	require.NoError(t, e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo"}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Version: "dev"}))
	assert.Equal(t, "SECRET=1\n", readFile(t, env))

	require.NoError(t, e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Purge: true}))
	assert.NoDirExists(t, e.app.Host.Paths.EnvDir("econumo"))
}

func TestInstallLegacyLayoutPointsToAdoptFirst(t *testing.T) {
	e := newEnv(t)
	legacyFixture(t, e, "econumo", "econumo")
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ship adopt econumo --service econumo")
}

func TestInstallAndAdoptRefuseReservedServiceNames(t *testing.T) {
	for _, name := range []string{"root", "ship", "systemd", "ssh", "sshd", "nobody", "etc", "opt", "var", "usr", "tmp", "run", "lib"} {
		e := newEnv(t)
		err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: name, Version: "dev"})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "reserved", name)

		args := adoptArgs()
		args.Service = name
		err = e.app.Adopt(context.Background(), args)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "reserved", name)
		assert.Empty(t, e.sys.Calls)
		assert.Empty(t, files(t, e.root))
	}
}

func TestInstallRecordsCreatedUser(t *testing.T) {
	ctx := context.Background()
	t.Run("created user is deleted by purge", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Version: "dev"}))
		assert.True(t, e.state(t).Services["econumo"].CreatedUser)
		require.NoError(t, e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Purge: true}))
		assert.Contains(t, e.sys.Calls, "DeleteUser econumo")
	})
	t.Run("pre-existing user is kept by purge", func(t *testing.T) {
		e := newEnv(t)
		e.sys.Users = map[string]bool{"econumo": true}
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Version: "dev"}))
		assert.False(t, e.state(t).Services["econumo"].CreatedUser)
		require.NoError(t, e.app.Uninstall(ctx, app.UninstallArgs{Ref: "econumo", Purge: true}))
		assert.NotContains(t, e.sys.Calls, "DeleteUser econumo")
		assert.True(t, e.sys.UserExists("econumo"))
		assert.NoDirExists(t, e.app.Host.Paths.DataDir("econumo"))
	})
	t.Run("second ref keeps the flag", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate-sql", Service: "twillingate", Version: "v5"}))
		assert.True(t, e.state(t).Services["twillingate"].CreatedUser)
	})
}

func TestAdoptRecordsCreatedUser(t *testing.T) {
	e := newEnv(t)
	legacyFixture(t, e, "econumo-beta", "econumo")
	require.NoError(t, e.app.Adopt(context.Background(), adoptArgs()))
	assert.True(t, e.state(t).Services["econumo-beta"].CreatedUser)
}

func TestInstallRefusesNameCollisions(t *testing.T) {
	ctx := context.Background()
	t.Run("same binary name", func(t *testing.T) {
		e := newEnv(t)
		// twillingate/sql has asset twillingate-sql, like the project twillingate-sql.
		publish(t, e.store, "twillingate", "v3", nil, true, "twillingate-sql")
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate-sql", Service: "tw", Version: "v5"}))
		calls := len(e.sys.Calls)
		err := e.app.Install(ctx, app.InstallArgs{Ref: "twillingate/sql", Service: "tw", Version: "v3"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/opt/tw/twillingate-sql")
		assert.Contains(t, err.Error(), "twillingate-sql")
		assert.Len(t, e.sys.Calls, calls)
		assert.Len(t, e.state(t).Services["tw"].Refs, 1)
	})
	t.Run("same env file name", func(t *testing.T) {
		e := newEnv(t)
		publish(t, e.store, "other", "v1", nil, true, "other-agent")
		require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "winterflow/agent", Service: "wf", Version: "v3"}))
		calls := len(e.sys.Calls)
		err := e.app.Install(ctx, app.InstallArgs{Ref: "other/agent", Service: "wf", Version: "v1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "/etc/wf/agent.env")
		assert.Contains(t, err.Error(), "winterflow/agent")
		assert.Len(t, e.sys.Calls, calls)
		assert.Len(t, e.state(t).Services["wf"].Refs, 1)
	})
}
