package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
)

// withNotifier wires a fake notifier into e.
func withNotifier(e *env) *fakeNotifier {
	n := &fakeNotifier{}
	e.app.Notifier = func() (app.Notifier, error) { return n, nil }
	return n
}

func TestInstallAutoUpdateAnnounces(t *testing.T) {
	e := newEnv(t)
	n := withNotifier(e)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", AutoUpdate: true}))
	assert.Equal(t, []string{"auto-update on for econumo-beta (dev)"}, n.titles())
	assert.Contains(t, e.out.String(), "econumo-beta: auto-update on\n")
}

func TestInstallWithoutAutoUpdateSendsNothing(t *testing.T) {
	e := newEnv(t)
	n := withNotifier(e)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev"}))
	assert.Empty(t, n.got)
}

func TestUpdateToggleAnnouncesOnlyChanges(t *testing.T) {
	e := installedEnv(t)
	n := withNotifier(e)
	ctx := context.Background()
	args := func(on bool) app.UpdateArgs {
		return app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(on)}
	}

	// A real update that also turns auto-update on.
	require.NoError(t, e.app.Update(ctx, args(true)))
	assert.Equal(t, "econumo-beta: 1.4.0 -> 1.5.0\neconumo-beta: auto-update on\n", e.out.String())

	// Already on: nothing is announced.
	e.out.Reset()
	require.NoError(t, e.app.Update(ctx, args(true)))
	assert.Equal(t, "econumo-beta: already on 1.5.0\n", e.out.String())

	// Off on an up-to-date ref.
	e.out.Reset()
	require.NoError(t, e.app.Update(ctx, args(false)))
	assert.Equal(t, "econumo-beta: already on 1.5.0\neconumo-beta: auto-update off\n", e.out.String())

	// A plain update leaves the setting alone and says nothing about it.
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))

	assert.Equal(t, []string{
		"auto-update on for econumo-beta (dev)",
		"auto-update off for econumo-beta (dev)",
	}, n.titles())
}

func TestUpdateTurningStoppedBackOnAnnounces(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	require.NoError(t, e.app.Rollback(ctx, "econumo", "econumo-beta"))
	n := withNotifier(e)
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	assert.Equal(t, []string{"auto-update on for econumo-beta (dev)"}, n.titles())
}

func TestRollbackStopAnnounced(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	n := withNotifier(e)
	require.NoError(t, e.app.Rollback(ctx, "econumo", "econumo-beta"))
	assert.Equal(t, []string{"auto-update stopped for econumo-beta (dev) after a manual rollback"}, n.titles())
}

func TestToggleNotifyFailureKeepsSetting(t *testing.T) {
	e := installedEnv(t)
	n := withNotifier(e)
	n.err = errors.New("chat not found")
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notification not sent: chat not found")
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.True(t, rs.AutoUpdate, "the setting is saved even though the send failed")
	assert.Equal(t, "1.5.0", rs.Version)
}

func TestToggleNotifierErrorReported(t *testing.T) {
	e := installedEnv(t)
	withNotifier(e)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	e.app.Notifier = func() (app.Notifier, error) { return nil, errors.New("bad url") }
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(false)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notification not sent: bad url")
}

func TestNotifyTest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	err := e.app.NotifyTest(ctx)
	assert.ErrorIs(t, err, app.ErrNoNotifyURL)

	e.app.Cfg.Notify.URL = "ntfy://ntfy.sh/topic"
	n := withNotifier(e)
	require.NoError(t, e.app.NotifyTest(ctx))
	assert.Equal(t, []string{"test notification from ship"}, n.titles())
	assert.Equal(t, "test notification sent\n", e.out.String())

	n.err = errors.New("chat not found")
	err = e.app.NotifyTest(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test notification not sent: chat not found")

	e.app.Notifier = func() (app.Notifier, error) { return nil, errors.New("bad url") }
	assert.EqualError(t, e.app.NotifyTest(ctx), "bad url")
}
