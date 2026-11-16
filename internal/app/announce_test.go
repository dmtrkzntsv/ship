package app_test

import (
	"bytes"
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

func TestToggleNotifyFailureOnlyWarns(t *testing.T) {
	e := installedEnv(t)
	n := withNotifier(e)
	n.err = errors.New("chat not found")
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	assert.Contains(t, e.app.Err.(*bytes.Buffer).String(), "warning: notification not sent: chat not found")
	rs := e.state(t).Services["econumo-beta"].Refs["econumo"]
	assert.True(t, rs.AutoUpdate, "the setting is saved even though the send failed")
	assert.Equal(t, "1.5.0", rs.Version)
}

func TestToggleNotifierErrorOnlyWarns(t *testing.T) {
	e := installedEnv(t)
	withNotifier(e)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	e.app.Notifier = func() (app.Notifier, error) { return nil, errors.New("bad url") }
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(false)}))
	assert.Contains(t, e.app.Err.(*bytes.Buffer).String(), "warning: notification not sent: bad url")
}

// A rollback whose announcement fails still succeeds: a retry would roll
// forward again.
func TestRollbackNotifyFailureStillSucceeds(t *testing.T) {
	e := installedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	n := withNotifier(e)
	n.err = errors.New("timeout")
	require.NoError(t, e.app.Rollback(ctx, "econumo", "econumo-beta"))
	assert.Equal(t, "1.4.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
	assert.Contains(t, e.app.Err.(*bytes.Buffer).String(), "warning: notification not sent: timeout")
}

// Once the change is saved, an interrupt does not stop its announcement.
func TestToggleAnnouncedAfterInterrupt(t *testing.T) {
	e := installedEnv(t)
	n := withNotifier(e)
	ctx, cancel := context.WithCancel(context.Background())
	e.app.Notifier = func() (app.Notifier, error) { cancel(); return n, nil }
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	require.Len(t, n.got, 1)
	assert.Empty(t, e.app.Err.(*bytes.Buffer).String(), "the send used a context that the interrupt did not cancel")
}

// The announcement is sent after the state lock is released.
func TestToggleSentWithoutStateLock(t *testing.T) {
	e := installedEnv(t)
	locked := true
	e.app.Notifier = func() (app.Notifier, error) {
		unlock, err := e.app.State.Lock()
		if err == nil {
			locked = false
			unlock()
		}
		return &fakeNotifier{}, nil
	}
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	assert.False(t, locked, "the state lock was still held while notifying")
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

// A failed timer start leaves the setting saved as on; the change is still
// announced, because the rerun the error asks for finds it already on.
func TestToggleAnnouncedWhenTimerFails(t *testing.T) {
	ctx := context.Background()
	on := func() app.UpdateArgs {
		return app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}
	}

	t.Run("swapped", func(t *testing.T) {
		e := installedEnv(t)
		n := withNotifier(e)
		e.sys.EnableErr = assert.AnError
		require.ErrorIs(t, e.app.Update(ctx, on()), assert.AnError)
		assert.Equal(t, []string{"auto-update on for econumo-beta (dev)"}, n.titles())

		// The rerun brings the timer up and announces nothing new.
		e.sys.EnableErr = nil
		require.NoError(t, e.app.Update(ctx, on()))
		assert.Len(t, n.got, 1)
	})

	t.Run("up to date", func(t *testing.T) {
		e := installedEnv(t)
		require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
		n := withNotifier(e)
		e.sys.EnableErr = assert.AnError
		err := e.app.Update(ctx, on())
		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, []string{"auto-update on for econumo-beta (dev)"}, n.titles())
		assert.Contains(t, e.out.String(), "econumo-beta: auto-update on\n")
	})

	t.Run("install", func(t *testing.T) {
		e := newEnv(t)
		n := withNotifier(e)
		// Fail only the timer's enable: the ref's own unit is enabled first.
		e.sys.Fail = map[string]error{"systemctl enable --now ship-auto-update.timer": assert.AnError}
		err := e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", AutoUpdate: true})
		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, []string{"auto-update on for econumo-beta (dev)"}, n.titles())
	})
}
