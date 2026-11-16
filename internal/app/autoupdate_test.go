package app_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/host"
)

func ptr(b bool) *bool { return &b }

func TestInstallAutoUpdate(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", AutoUpdate: true}))
	assert.True(t, e.state(t).Services["econumo-beta"].Refs["econumo"].AutoUpdate)
	assert.Contains(t, e.sys.Calls, "systemctl enable --now ship-auto-update.timer")
	assert.FileExists(t, e.app.Host.Paths.TimerPath(host.AutoUpdateUnit))
}

func TestInstallAutoUpdateOnVersionRefused(t *testing.T) {
	e := newEnv(t)
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "twillingate", Version: "v2", AutoUpdate: true})
	require.EqualError(t, err, "twillingate follows version v2, which never changes; auto-update needs a moving channel (latest, dev, edge, \u2026)")
	assert.Empty(t, e.state(t).Services)
	assert.Empty(t, files(t, e.root))
}

func TestInstallAutoUpdateInvalidNotifyURL(t *testing.T) {
	e := newEnv(t)
	e.app.Cfg.Notify.URL = "bogus://x"
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev", AutoUpdate: true})
	require.ErrorContains(t, err, "invalid notification url")
	assert.Empty(t, e.state(t).Services)
	assert.Empty(t, files(t, e.root))
}

func TestInstallAutoUpdateShipPathProblems(t *testing.T) {
	e := newEnv(t)
	e.app.ShipPath = func() (string, error) { return "/opt/my ship/ship", nil }
	err := e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev", AutoUpdate: true})
	require.ErrorContains(t, err, "contains whitespace")
	assert.Empty(t, files(t, e.root))

	e.app.ShipPath = func() (string, error) { return "", assert.AnError }
	require.ErrorIs(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev", AutoUpdate: true}), assert.AnError)
	assert.Empty(t, files(t, e.root))

	e.app.ShipPath = nil
	require.Error(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Version: "dev", AutoUpdate: true}))
	assert.Empty(t, files(t, e.root))
}

func TestUpdateAutoUpdateTimerFailureKeepsFlag(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}))
	e.sys.EnableErr = assert.AnError
	err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)})
	require.ErrorIs(t, err, assert.AnError)
	assert.True(t, e.state(t).Services["econumo-beta"].Refs["econumo"].AutoUpdate)
}

func TestUpdateAutoUpdateTimerFailureAfterRealUpdate(t *testing.T) {
	e := installedEnv(t)
	e.sys.EnableErr = assert.AnError
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)})
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "update of econumo-beta succeeded but turning on auto-update failed (rerun with --auto-update)")
	assert.Equal(t, "econumo-beta: 1.4.0 -> 1.5.0\n", e.out.String())
	assert.Equal(t, "1.5.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
}

func TestUpdateTurnsAutoUpdateOnAndOff(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	a := app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}

	// turning on during a real update
	a.AutoUpdate = ptr(true)
	require.NoError(t, e.app.Update(ctx, a))
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.True(t, rs.AutoUpdate)
	assert.Equal(t, "1.5.0", rs.Version)
	assert.Contains(t, e.sys.Calls, "systemctl enable --now ship-auto-update.timer")

	// off, on an already-current ref; clears the check clock
	rs.CheckFailingSince = fixedNow
	rs.CheckNotifiedAt = fixedNow
	s := e.state(t)
	s.Services["econumo-beta"].Refs["econumo"] = rs
	require.NoError(t, e.app.State.Save(s))
	a.AutoUpdate = ptr(false)
	require.NoError(t, e.app.Update(ctx, a))
	rs = e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.CheckFailingSince.IsZero())
	assert.True(t, rs.CheckNotifiedAt.IsZero())

	// on again, on an already-current ref
	e.sys.Calls = nil
	a.AutoUpdate = ptr(true)
	require.NoError(t, e.app.Update(ctx, a))
	assert.True(t, e.state(t).Services["econumo-beta"].Refs["econumo"].AutoUpdate)
	assert.Contains(t, e.sys.Calls, "systemctl enable --now ship-auto-update.timer")
}

func TestUpdateAutoUpdateOnVersionRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
	err := e.app.Update(ctx, app.UpdateArgs{Ref: "twillingate", AutoUpdate: ptr(true)})
	require.EqualError(t, err, "twillingate follows version v2, which never changes; auto-update needs a moving channel (latest, dev, edge, \u2026)")
	assert.False(t, e.state(t).Services["twillingate"].Refs["twillingate"].AutoUpdate)
}

func TestUpdatePinWithAutoUpdateRefused(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	err := e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "1.4.0"})
	require.EqualError(t, err, "econumo-beta follows version 1.4.0, which never changes; auto-update needs a moving channel (latest, dev, edge, \u2026); pass --auto-update=false to pin it")
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "dev", rs.Channel)
	assert.True(t, rs.AutoUpdate)

	// pinning with the flag off is fine
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "1.4.0", AutoUpdate: ptr(false)}))
	rs = e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.Equal(t, "1.4.0", rs.Channel)
	assert.False(t, rs.AutoUpdate)
}

func TestUpdateInvalidNotifyURLBeforeAnything(t *testing.T) {
	e := installedEnv(t)
	e.app.Cfg.Notify.URL = "bogus://x"
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)})
	require.ErrorContains(t, err, "invalid notification url")
	assert.Empty(t, e.sys.Calls)
	assert.Equal(t, "1.4.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
}

func TestManualUpdateKeepsStopped(t *testing.T) {
	e := installedEnv(t)
	s := e.state(t)
	rs := s.Services["econumo-beta"].Refs["econumo"]
	rs.AutoUpdateStoppedAt = fixedNow
	rs.CheckFailingSince = fixedNow
	require.NoError(t, e.app.State.Save(s))

	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	rs = e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.Equal(fixedNow))
	assert.True(t, rs.CheckFailingSince.Equal(fixedNow))

	// setting the flag clears the stop marker
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	rs = e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.True(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.IsZero())
}

func TestRollbackStopsAutoUpdate(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	s := e.state(t)
	s.Services["econumo-beta"].Refs["econumo"].CheckFailingSince = fixedNow
	require.NoError(t, e.app.State.Save(s))
	e.out.Reset()

	require.NoError(t, e.app.Rollback(ctx, "econumo", "econumo-beta"))
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.Equal(later))
	assert.True(t, rs.CheckFailingSince.IsZero())
	assert.Equal(t, "econumo-beta: 1.5.0 -> 1.4.0\n"+
		"econumo-beta: auto-update stopped; turn it back on with: ship update econumo --service econumo-beta --auto-update\n", e.out.String())
}

func TestRollbackManualRefUnchanged(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	e.out.Reset()
	require.NoError(t, e.app.Rollback(ctx, "econumo", "econumo-beta"))
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.True(t, rs.AutoUpdateStoppedAt.IsZero())
	assert.Equal(t, "econumo-beta: 1.5.0 -> 1.4.0\n", e.out.String())
}
