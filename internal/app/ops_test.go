package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func twoServices(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.app.Host.HealthWait, e.app.Host.HealthTick = 20*time.Millisecond, 5*time.Millisecond
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate-sql", Service: "twillingate", Version: "v5"}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}))
	e.out.Reset()
	e.sys.Calls = nil
	return e
}

func TestAppUninstallOneThenLast(t *testing.T) {
	e := twoServices(t)
	ctx := context.Background()
	require.NoError(t, e.app.Uninstall(ctx, app.UninstallArgs{Ref: "twillingate-sql", Service: "twillingate"}))
	s := e.state(t)
	assert.Equal(t, []string{"twillingate"}, s.Units("twillingate"))
	assert.DirExists(t, e.app.Host.Paths.BinDir("twillingate"))

	require.NoError(t, e.app.Uninstall(ctx, app.UninstallArgs{Ref: "twillingate", Purge: true}))
	assert.NotContains(t, e.state(t).Services, "twillingate")
	assert.Contains(t, e.state(t).Services, "econumo-beta")
	assert.NoDirExists(t, e.app.Host.Paths.DataDir("twillingate"))
	assert.Contains(t, e.sys.Calls, "DeleteUser twillingate")
}

func TestAppUninstallNotInstalled(t *testing.T) {
	e := twoServices(t)
	for _, a := range []app.UninstallArgs{
		{Ref: "econumo"},
		{Ref: "twillingate/nope", Service: "twillingate"},
		{Ref: "twillingate", Service: "nosuch"},
	} {
		err := e.app.Uninstall(context.Background(), a)
		require.Error(t, err, "%+v", a)
		assert.Contains(t, err.Error(), "not installed")
	}
	assert.Empty(t, e.sys.Calls)
	assert.Len(t, e.state(t).Services, 2)
}

func TestAppLifecycle(t *testing.T) {
	e := twoServices(t)
	ctx := context.Background()
	require.NoError(t, e.app.Lifecycle(ctx, "start", "twillingate-sql", "twillingate"))
	require.NoError(t, e.app.Lifecycle(ctx, "restart", "twillingate-sql", "twillingate"))
	require.NoError(t, e.app.Lifecycle(ctx, "stop", "twillingate-sql", "twillingate"))
	assert.Contains(t, e.sys.Calls, "systemctl start twillingate-sql")
	assert.Contains(t, e.sys.Calls, "systemctl restart twillingate-sql")
	assert.Contains(t, e.sys.Calls, "systemctl stop twillingate-sql")

	e.sys.Calls = nil
	require.Error(t, e.app.Lifecycle(ctx, "reload", "twillingate", ""))
	require.Error(t, e.app.Lifecycle(ctx, "start", "econumo", ""))
	assert.Empty(t, e.sys.Calls)
}

func TestAppLogsAndRunArgs(t *testing.T) {
	e := twoServices(t)
	argv, err := e.app.LogsArgs("twillingate-sql", "twillingate", 50, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"journalctl", "-u", "twillingate-sql.service", "-n", "50"}, argv)

	argv, err = e.app.RunArgs("twillingate-sql", "twillingate", []string{"a"}, true)
	require.NoError(t, err)
	assert.Equal(t, "/opt/twillingate/twillingate-sql", argv[len(argv)-2])
	assert.Equal(t, "a", argv[len(argv)-1])

	_, err = e.app.LogsArgs("econumo", "", 1, false)
	require.Error(t, err)
	_, err = e.app.RunArgs("econumo", "", nil, false)
	require.Error(t, err)
}

func TestStatusTable(t *testing.T) {
	e := twoServices(t)
	e.sys.Active = map[string]bool{"twillingate-sql": true}
	require.NoError(t, e.app.Status(context.Background(), ""))
	assert.Equal(t, `SERVICE       REF              UNIT             CHANNEL  VERSION  STATE
econumo-beta  econumo          econumo-beta     dev      1.4.0    inactive
twillingate   twillingate      twillingate      v2       v2       inactive
twillingate   twillingate-sql  twillingate-sql  v5       v5       active
`, e.out.String())

	e.out.Reset()
	require.NoError(t, e.app.Status(context.Background(), "econumo-beta"))
	assert.NotContains(t, e.out.String(), "twillingate")
	assert.Error(t, e.app.Status(context.Background(), "nosuch"))
}

func TestStatusEmpty(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.app.Status(context.Background(), ""))
	assert.Equal(t, "no services installed\n", e.out.String())
}
