package app_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/state"
)

var later = fixedNow.Add(time.Hour)

// installedEnv has econumo 1.4.0 from dev installed in econumo-beta and
// 1.5.0 published to dev afterwards.
func installedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}))
	publish(t, e.store, "econumo", "1.5.0", []string{"dev"}, true, "econumo")
	e.out.Reset()
	e.sys.Calls = nil
	e.app.Now = func() time.Time { return later }
	return e
}

func TestAppUpdateUsesRememberedChannel(t *testing.T) {
	e := installedEnv(t)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))

	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, &state.RefState{Unit: "econumo-beta", Channel: "dev", Version: "1.5.0", PrevVersion: "1.4.0", InstalledAt: later}, rs)
	assert.Equal(t, "econumo-beta: 1.4.0 -> 1.5.0\n", e.out.String())
	assert.Contains(t, e.sys.Calls, "systemctl restart econumo-beta")
	assert.Equal(t, "econumo-amd64", readFile(t, e.app.Host.Paths.Bin("econumo-beta", "econumo")))
}

func TestAppUpdateVersionIsRemembered(t *testing.T) {
	e := installedEnv(t)
	publish(t, e.store, "econumo", "2.0.0", []string{"latest"}, true, "econumo")
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "latest"}))

	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "latest", rs.Channel)
	assert.Equal(t, "2.0.0", rs.Version)
	assert.Equal(t, "1.4.0", rs.PrevVersion)
}

func TestAppUpdateDefaultService(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev"}))
	publish(t, e.store, "econumo", "1.5.0", []string{"dev"}, true, "econumo")
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo"}))
	assert.Equal(t, "1.5.0", e.state(t).Services["econumo"].Refs["econumo"].Version)
}

func TestAppUpdateNotInstalled(t *testing.T) {
	e := newEnv(t)
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ship install econumo --service econumo-beta --version <channel>")
	assert.Empty(t, e.sys.Calls)
}

func TestAppUpdateInvalidArgs(t *testing.T) {
	e := installedEnv(t)
	for _, args := range []app.UpdateArgs{
		{Ref: "Bad"},
		{Ref: "econumo", Service: "Bad"},
		{Ref: "econumo", Service: "econumo-beta", Version: "../x"},
	} {
		assert.Error(t, e.app.Update(context.Background(), args), "%+v", args)
	}
	assert.Empty(t, e.sys.Calls)
}

func TestAppUpdateFailureKeepsState(t *testing.T) {
	e := installedEnv(t)
	before := e.state(t)
	e.sys.ActiveAfterRestart = func(string) bool { return false }
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "1.5.0"})
	require.Error(t, err)
	assert.Equal(t, before, e.state(t))
	assert.Empty(t, e.out.String())
}

func TestAppFailedUpdateThenRollback(t *testing.T) {
	e := installedEnv(t)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	publish(t, e.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	e.sys.ActiveAfterRestart = func(string) bool { return false }
	require.Error(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	e.sys.ActiveAfterRestart = nil
	e.out.Reset()

	require.NoError(t, e.app.Rollback(context.Background(), "econumo", "econumo-beta"))
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "1.4.0", rs.Version)
	assert.Equal(t, "1.5.0", rs.PrevVersion)
	assert.Equal(t, "econumo-beta: 1.5.0 -> 1.4.0\n", e.out.String())
}

func TestAppUpdateAlreadyOnVersion(t *testing.T) {
	e := installedEnv(t)
	before := e.state(t).Services["econumo-beta"].Refs["econumo"]
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "1.4.0"}))

	assert.Equal(t, "econumo-beta: already on 1.4.0\n", e.out.String())
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "1.4.0", rs.Channel, "a newly given --version is remembered")
	assert.Equal(t, "1.4.0", rs.Version)
	assert.Empty(t, rs.PrevVersion)
	assert.Equal(t, before.InstalledAt, rs.InstalledAt)
	assert.Empty(t, e.sys.Calls)
	assert.NoFileExists(t, e.app.Host.Paths.Bin("econumo-beta", "econumo")+".prev")
}

func TestAppRollbackState(t *testing.T) {
	e := installedEnv(t)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	e.out.Reset()

	require.NoError(t, e.app.Rollback(context.Background(), "econumo", "econumo-beta"))
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "1.4.0", rs.Version)
	assert.Equal(t, "1.5.0", rs.PrevVersion)
	assert.Equal(t, "dev", rs.Channel)
	assert.Equal(t, "econumo-beta: 1.5.0 -> 1.4.0\n", e.out.String())
}

func TestAppRollbackNothingToRollBack(t *testing.T) {
	e := installedEnv(t)
	before := e.state(t)
	err := e.app.Rollback(context.Background(), "econumo", "econumo-beta")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to roll back for econumo-beta")
	assert.Equal(t, before, e.state(t))
}

func TestAppRollbackNotInstalled(t *testing.T) {
	e := newEnv(t)
	err := e.app.Rollback(context.Background(), "econumo", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed in service econumo")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
