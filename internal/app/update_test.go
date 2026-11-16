package app_test

import (
	"context"
	"os"
	"path/filepath"
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
	assert.Equal(t, &state.RefState{
		Unit: "econumo-beta", Channel: "dev", Version: "1.5.0", PrevVersion: "1.4.0",
		PublishedAt: fixedNow, PrevPublishedAt: fixedNow, InstalledAt: later,
	}, rs)
	assert.Equal(t, "econumo-beta: 1.4.0 -> 1.5.0\n", e.out.String())
	assert.Contains(t, e.sys.Calls, "systemctl restart econumo-beta")
	assert.Equal(t, "econumo-amd64 1.5.0 "+fixedNow.Format(time.RFC3339), readFile(t, e.app.Host.Paths.Bin("econumo-beta", "econumo")))
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

// TestAppUpdateInterruptedDuringHealthCheck: a signal (ctx cancel) while the
// new pair is being health-checked restores and restarts the old pair.
func TestAppUpdateInterruptedDuringHealthCheck(t *testing.T) {
	e := installedEnv(t)
	before := e.state(t)
	p := e.app.Host.Paths
	unitBefore := readFile(t, p.UnitPath("econumo-beta"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.sys.ActiveAfterRestart = func(string) bool {
		cancel() // the signal arrives while the new binary is up
		return true
	}

	err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "previous version restored")
	assert.Equal(t, before, e.state(t))
	assert.Equal(t, "econumo-amd64 1.4.0 "+fixedNow.Format(time.RFC3339), readFile(t, p.Bin("econumo-beta", "econumo")))
	assert.Equal(t, unitBefore, readFile(t, p.UnitPath("econumo-beta")))
	var restarts int
	for _, c := range e.sys.Calls {
		if c == "systemctl restart econumo-beta" {
			restarts++
		}
	}
	assert.Equal(t, 2, restarts, "the new pair, then the restored one: %v", e.sys.Calls)
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

// TestAppUpdateRepublishedChannel: a channel-only publish names the channel
// as its version, so only the publish time tells two builds apart.
func TestAppUpdateRepublishedChannel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, second := fixedNow.Add(time.Minute), fixedNow.Add(2*time.Minute)
	publishAt(t, e.store, first, "devonly", "", []string{"dev"}, true, "devonly")
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "devonly", Version: "dev"}))
	rs := e.state(t).Services["devonly"].Refs["devonly"]
	assert.Equal(t, "dev", rs.Version)
	assert.Equal(t, first, rs.PublishedAt)

	publishAt(t, e.store, second, "devonly", "", []string{"dev"}, true, "devonly")
	e.sys.Calls = nil
	e.out.Reset()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "devonly"}))
	assert.Contains(t, e.sys.Calls, "systemctl restart devonly")
	assert.NotContains(t, e.out.String(), "already on")
	rs = e.state(t).Services["devonly"].Refs["devonly"]
	assert.Equal(t, second, rs.PublishedAt)
	assert.Equal(t, "dev", rs.PrevVersion)
	assert.Equal(t, first, rs.PrevPublishedAt)

	// Nothing new: no restart.
	e.sys.Calls = nil
	e.out.Reset()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "devonly"}))
	assert.NotContains(t, e.sys.Calls, "systemctl restart devonly")
	assert.Contains(t, e.out.String(), "already on dev")

	// Rollback swaps the publish times along with the versions.
	require.NoError(t, e.app.Rollback(ctx, "devonly", ""))
	rs = e.state(t).Services["devonly"].Refs["devonly"]
	assert.Equal(t, first, rs.PublishedAt)
	assert.Equal(t, second, rs.PrevPublishedAt)
}

// A custom moving channel works like dev: install remembers it and every
// update fetches its newest build.
func TestAppInstallUpdateCustomChannel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, second := fixedNow.Add(time.Minute), fixedNow.Add(2*time.Minute)
	publishAt(t, e.store, first, "econumo", "", []string{"edge"}, true, "econumo")
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-edge", Version: "edge"}))
	rs := e.state(t).Services["econumo-edge"].Refs["econumo"]
	assert.Equal(t, "edge", rs.Channel)
	assert.Equal(t, first, rs.PublishedAt)

	publishAt(t, e.store, second, "econumo", "", []string{"edge"}, true, "econumo")
	e.sys.Calls = nil
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-edge"}))
	assert.Contains(t, e.sys.Calls, "systemctl restart econumo-edge")
	rs = e.state(t).Services["econumo-edge"].Refs["econumo"]
	assert.Equal(t, "edge", rs.Channel)
	assert.Equal(t, second, rs.PublishedAt)

	// A release that also moves edge is picked up with its real version.
	publishAt(t, e.store, second.Add(time.Minute), "econumo", "v2.0.0", []string{"latest", "edge"}, true, "econumo")
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-edge"}))
	rs = e.state(t).Services["econumo-edge"].Refs["econumo"]
	assert.Equal(t, "edge", rs.Channel)
	assert.Equal(t, "v2.0.0", rs.Version)

	// Switching a ref installed from dev to edge remembers it.
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}))
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "edge"}))
	rs = e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "edge", rs.Channel)
	assert.Equal(t, "v2.0.0", rs.Version)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestAppUpdateRecordsBuildAlreadyLive(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	readOnlyStateDir(t, e)
	require.Error(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	assert.Contains(t, readFile(t, e.app.RunClockPath), `"pending"`)
	bin := e.app.Host.Paths.Bin("econumo-beta", "econumo")
	prev := readFile(t, bin+".prev")

	// Still unsaveable: recorded in memory only, so nothing is claimed.
	e.out.Reset()
	e.sys.Calls = nil
	require.Error(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	assert.NotContains(t, e.out.String(), "already installed")
	assert.NotContains(t, e.sys.Calls, "systemctl restart econumo-beta")

	require.NoError(t, os.Chmod(filepath.Dir(e.app.State.Path), 0o755))
	e.out.Reset()
	e.sys.Calls = nil
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	assert.Equal(t, "econumo-beta: already installed 1.5.0, state recorded\n", e.out.String())
	assert.NotContains(t, e.sys.Calls, "systemctl restart econumo-beta")
	assert.Equal(t, prev, readFile(t, bin+".prev"))
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "1.5.0", rs.Version)
	assert.Equal(t, "1.4.0", rs.PrevVersion)
	assert.NotContains(t, readFile(t, e.app.RunClockPath), `"pending"`)
}

func TestAppUpdateLiveFilesUnreadable(t *testing.T) {
	e := installedEnv(t)
	// A pending record for 1.5.0 makes the update compare the live files.
	pending := `{"pending":{"service":"econumo-beta","ref":"econumo","version":"1.5.0","published_at":"` + fixedNow.Format(time.RFC3339) + `"}}`
	require.NoError(t, os.WriteFile(e.app.RunClockPath, []byte(pending), 0o600))
	bin := e.app.Host.Paths.Bin("econumo-beta", "econumo")
	require.NoError(t, os.Remove(bin))
	require.NoError(t, os.Mkdir(bin, 0o755)) // a directory cannot be hashed
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"})
	require.Error(t, err)
	assert.Empty(t, e.sys.Calls)
	assert.Equal(t, "1.4.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
}

func TestAppUpdatePendingRecordForOtherFilesInstalls(t *testing.T) {
	e := installedEnv(t)
	// A pending record for 1.5.0, but the host still runs 1.4.0 (a revert
	// put it back): the record does not count, the update installs.
	pending := `{"pending":{"service":"econumo-beta","ref":"econumo","version":"1.5.0","published_at":"` + fixedNow.Format(time.RFC3339) + `"}}`
	require.NoError(t, os.WriteFile(e.app.RunClockPath, []byte(pending), 0o600))
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	assert.Contains(t, e.sys.Calls, "systemctl restart econumo-beta")
	assert.Equal(t, "econumo-beta: 1.4.0 -> 1.5.0\n", e.out.String())
	assert.NotContains(t, readFile(t, e.app.RunClockPath), `"pending"`)
}

func TestAppRollbackStateNotSavedSaysSo(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	readOnlyStateDir(t, e)
	err := e.app.Rollback(ctx, "econumo", "econumo-beta")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback of econumo-beta succeeded but the state could not be saved (auto-update not stopped; run: ship update econumo --service econumo-beta --auto-update=false): ")
	assert.Contains(t, err.Error(), "create temp state file")
}
