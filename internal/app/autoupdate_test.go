package app_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/notify"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
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

// fakeNotifier records what ship update --auto sends; err fails every send.
type fakeNotifier struct {
	got []notify.Message
	err error
}

func (f *fakeNotifier) Notify(ctx context.Context, m notify.Message) error {
	f.got = append(f.got, m)
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.err
}

func (f *fakeNotifier) titles() []string {
	var out []string
	for _, m := range f.got {
		out = append(out, m.Title)
	}
	return out
}

// flakyStore is a Mem whose reads fail with err while it is set.
type flakyStore struct {
	*storetest.Mem
	err error
}

func (f *flakyStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.Mem.Get(ctx, key)
}

type autoFixture struct {
	*env
	n      *fakeNotifier
	st     *flakyStore
	errOut *bytes.Buffer
	now    time.Time
}

// autoEnv has econumo 1.4.0 from dev installed and running in econumo-beta
// with auto-update on, the same in econumo-prod with it off, and 1.5.0
// published to dev afterwards. The clock is at later and moves with f.at.
func autoEnv(t *testing.T) *autoFixture {
	t.Helper()
	e := newEnv(t)
	f := &autoFixture{env: e, n: &fakeNotifier{}, st: &flakyStore{Mem: e.store}, errOut: &bytes.Buffer{}}
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", AutoUpdate: true}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-prod", Version: "dev"}))
	publish(t, e.store, "econumo", "1.5.0", []string{"dev"}, true, "econumo")
	e.sys.Active = map[string]bool{"econumo-beta": true, "econumo-prod": true}
	e.app.NewStore = func() (store.Store, error) { return f.st, nil }
	e.app.Notifier = func() (app.Notifier, error) { return f.n, nil }
	e.app.RunClockPath = filepath.Join(t.TempDir(), "auto-update.json")
	e.app.Err = f.errOut
	f.now = later
	e.app.Now = func() time.Time { return f.now }
	e.out.Reset()
	e.sys.Calls = nil
	return f
}

func (f *autoFixture) at(d time.Duration) { f.now = later.Add(d) }

func (f *autoFixture) ref(t *testing.T, service string) *state.RefState {
	t.Helper()
	return f.state(t).Services[service].Refs["econumo"]
}

// hm is how messages show a time: the host's local clock.
func hm(t time.Time) string { return t.Local().Format("15:04") }

func restarts(calls []string, unit string) int {
	n := 0
	for _, c := range calls {
		if c == "systemctl restart "+unit {
			n++
		}
	}
	return n
}

func TestAutoUpdateUpdatesOptedInOnly(t *testing.T) {
	f := autoEnv(t)
	require.NoError(t, f.app.AutoUpdate(context.Background()))

	assert.Equal(t, "1.5.0", f.ref(t, "econumo-beta").Version)
	assert.True(t, f.ref(t, "econumo-beta").AutoUpdate)
	assert.Equal(t, "1.4.0", f.ref(t, "econumo-prod").Version)
	assert.Equal(t, "econumo-beta: 1.4.0 -> 1.5.0\n", f.out.String())
	assert.Equal(t, []notify.Message{{Title: "econumo-beta (dev) 1.4.0 → 1.5.0"}}, f.n.got)
	assert.Empty(t, f.errOut.String())
	assert.NoFileExists(t, f.app.RunClockPath, "a healthy run writes no run clock")
}

func TestAutoUpdateRepublishedChannelNamesPublishTime(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	first, second := fixedNow.Add(time.Minute), fixedNow.Add(2*time.Minute)
	publishAt(t, f.store, first, "devonly", "", []string{"dev"}, true, "devonly")
	require.NoError(t, f.app.Install(ctx, app.InstallArgs{Ref: "devonly", Version: "dev", AutoUpdate: true}))
	f.sys.Active["devonly"] = true
	publishAt(t, f.store, second, "devonly", "", []string{"dev"}, true, "devonly")
	f.out.Reset()

	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Contains(t, f.n.titles(), "devonly (dev) dev → dev (published "+second.Format(time.RFC3339)+")")
}

func TestAutoUpdateQuietWhenNothingChanged(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	require.NoError(t, f.app.AutoUpdate(ctx))
	before, err := os.ReadFile(f.app.State.Path)
	require.NoError(t, err)
	f.out.Reset()
	f.n.got = nil
	f.sys.Calls = nil
	f.at(5 * time.Minute)

	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Empty(t, f.out.String())
	assert.Empty(t, f.errOut.String())
	assert.Empty(t, f.n.got)
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))
	after, err := os.ReadFile(f.app.State.Path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestAutoUpdateBrokenBuildStops(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	key := "econumo/dev/econumo-linux-amd64"
	obj := f.store.Objects[key]
	obj.Data = []byte("tampered")
	f.store.Objects[key] = obj

	err := f.app.AutoUpdate(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 1 refs failed")
	require.Len(t, f.n.got, 1)
	assert.Equal(t, "econumo-beta (dev) build 1.5.0 failed, still on 1.4.0; auto-update stopped", f.n.got[0].Title)
	assert.Contains(t, f.n.got[0].Body, "checksum mismatch")
	assert.Contains(t, f.errOut.String(), "econumo-beta: ")
	assert.Contains(t, f.errOut.String(), "checksum mismatch")
	rs := f.ref(t, "econumo-beta")
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.Equal(later))
	assert.Equal(t, "1.4.0", rs.Version)
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))

	// A fixed build follows: the stopped ref is left alone, silently.
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	f.n.got = nil
	f.at(5 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Empty(t, f.n.got)
	assert.Equal(t, "1.4.0", f.ref(t, "econumo-beta").Version)
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))
}

func TestAutoUpdateBrokenBuildUnknownVersion(t *testing.T) {
	f := autoEnv(t)
	key := "econumo/dev/SHA256SUMS"
	obj := f.store.Objects[key]
	obj.Data = []byte("not a sums file")
	f.store.Objects[key] = obj

	require.Error(t, f.app.AutoUpdate(context.Background()))
	assert.Equal(t, []string{"econumo-beta (dev) build failed, still on 1.4.0; auto-update stopped"}, f.n.titles())
	assert.False(t, f.ref(t, "econumo-beta").AutoUpdate)
}

func TestAutoUpdateNoUnitStops(t *testing.T) {
	f := autoEnv(t)
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, false, "econumo")

	require.Error(t, f.app.AutoUpdate(context.Background()))
	assert.Equal(t, []string{"econumo-beta (dev) build 1.6.0 failed, still on 1.4.0; auto-update stopped"}, f.n.titles())
	assert.False(t, f.ref(t, "econumo-beta").AutoUpdate)
}

func TestAutoUpdateHostStageFailureStops(t *testing.T) {
	f := autoEnv(t)
	// The live binary is gone: the host cannot back it up.
	require.NoError(t, os.Remove(f.app.Host.Paths.Bin("econumo-beta", "econumo")))

	require.Error(t, f.app.AutoUpdate(context.Background()))
	require.Len(t, f.n.got, 1)
	assert.Equal(t, "econumo-beta (dev) build 1.5.0 failed, still on 1.4.0; auto-update stopped", f.n.got[0].Title)
	assert.Contains(t, f.n.got[0].Body, "is missing")
	assert.False(t, f.ref(t, "econumo-beta").AutoUpdate)
}

func TestAutoUpdateHealthFailStops(t *testing.T) {
	f := autoEnv(t)
	// The new build fails its health check; the restored one comes up.
	f.sys.ActiveAfterRestart = func(u string) bool { return restarts(f.sys.Calls, u) != 1 }
	f.sys.Journal = "panic: boom"

	require.Error(t, f.app.AutoUpdate(context.Background()))
	require.Len(t, f.n.got, 1)
	assert.Equal(t, "econumo-beta (dev) build 1.5.0 failed, still on 1.4.0; auto-update stopped", f.n.got[0].Title)
	assert.Contains(t, f.n.got[0].Body, "previous version restored")
	assert.Contains(t, f.n.got[0].Body, "panic: boom")
	rs := f.ref(t, "econumo-beta")
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.Equal(later))
	assert.Equal(t, "1.4.0", rs.Version)
}

// runningUntilRestart: the unit runs until it is restarted, then never again.
func runningUntilRestart(f *autoFixture) func(string) bool {
	return func(u string) bool { return restarts(f.sys.Calls, u) == 0 }
}

func TestAutoUpdateRevertFailedSendsDown(t *testing.T) {
	f := autoEnv(t)
	f.sys.ActiveAfterRestart = runningUntilRestart(f)

	require.Error(t, f.app.AutoUpdate(context.Background()))
	require.Len(t, f.n.got, 1)
	assert.Equal(t, "econumo-beta is DOWN — update to 1.5.0 failed and rollback to 1.4.0 failed; auto-update stopped", f.n.got[0].Title)
	assert.Contains(t, f.n.got[0].Body, "did not come back up")
	rs := f.ref(t, "econumo-beta")
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.Equal(later))
}

func TestAutoUpdateContinuesAfterFailure(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	require.NoError(t, f.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-a", Version: "dev", AutoUpdate: true}))
	f.sys.Active["econumo-a"] = true
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	f.out.Reset()
	f.sys.ActiveAfterRestart = func(u string) bool { return u != "econumo-a" || restarts(f.sys.Calls, u) == 0 }

	err := f.app.AutoUpdate(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 2 refs failed")
	assert.Equal(t, []string{
		"econumo-a is DOWN — update to 1.6.0 failed and rollback to 1.5.0 failed; auto-update stopped",
		"econumo-beta (dev) 1.4.0 → 1.6.0",
	}, f.n.titles())
	assert.Equal(t, "1.6.0", f.ref(t, "econumo-beta").Version)
	assert.True(t, f.ref(t, "econumo-beta").AutoUpdate)
	assert.False(t, f.ref(t, "econumo-a").AutoUpdate)
}

func TestAutoUpdateInterruptKeepsAutoUpdate(t *testing.T) {
	f := autoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, f.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-z", Version: "dev", AutoUpdate: true}))
	f.sys.Active["econumo-z"] = true
	// econumo-z installed 1.5.0; publish 1.6.0 so both refs have work.
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	f.sys.ActiveAfterRestart = func(u string) bool {
		if restarts(f.sys.Calls, u) > 0 {
			cancel()
		}
		return true
	}

	err := f.app.AutoUpdate(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, f.n.got)
	assert.Contains(t, f.errOut.String(), "econumo-beta: ")
	rs := f.ref(t, "econumo-beta")
	assert.True(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.IsZero())
	assert.Equal(t, "1.4.0", rs.Version)
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-z").Version, "the run stops at the interrupt")
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-z"))
}

func TestAutoUpdateInterruptedRevertFailedSendsDown(t *testing.T) {
	f := autoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, f.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-z", Version: "dev", AutoUpdate: true}))
	f.sys.Active["econumo-z"] = true
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	f.errOut.Reset()
	// SIGTERM during the health check, and the restored pair does not come up.
	f.sys.ActiveAfterRestart = func(u string) bool {
		if restarts(f.sys.Calls, u) == 0 {
			return true
		}
		cancel()
		return false
	}

	err := f.app.AutoUpdate(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, f.n.got, 1, "the Down message is sent on an interrupted run")
	assert.Equal(t, "econumo-beta is DOWN \u2014 update to 1.6.0 failed and rollback to 1.4.0 failed; auto-update stopped", f.n.got[0].Title)
	rs := f.ref(t, "econumo-beta")
	assert.False(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.Equal(later))
	assert.True(t, f.ref(t, "econumo-z").AutoUpdate, "the run ends after the interrupt")
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-z").Version)
	assert.Contains(t, f.errOut.String(), "econumo-beta: ")
}

func TestAutoUpdateStopNotSavedIsNotClaimed(t *testing.T) {
	f := autoEnv(t)
	f.sys.ActiveAfterRestart = runningUntilRestart(f)
	readOnlyStateDir(t, f.env)

	require.Error(t, f.app.AutoUpdate(context.Background()))
	require.Len(t, f.n.got, 1)
	assert.Equal(t, "econumo-beta is DOWN \u2014 update to 1.5.0 failed and rollback to 1.4.0 failed", f.n.got[0].Title)
	assert.Contains(t, f.n.got[0].Body, "auto-update could not be stopped: saving the state failed")
	assert.Contains(t, f.n.got[0].Body, "did not come back up")
}

func TestAutoUpdateCantCheckAfterOneHour(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	f.st.err = errors.New("dial tcp: connection refused")
	since := hm(later)

	for _, d := range []time.Duration{0, 30 * time.Minute} {
		f.at(d)
		err := f.app.AutoUpdate(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "1 of 1 refs failed")
		assert.Empty(t, f.n.got, "at %v", d)
	}
	rs := f.ref(t, "econumo-beta")
	assert.True(t, rs.AutoUpdate)
	assert.True(t, rs.CheckFailingSince.Equal(later))
	assert.Contains(t, f.errOut.String(), "econumo-beta: dial tcp: connection refused")

	f.at(time.Hour)
	require.Error(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, []notify.Message{{
		Title: "can't check econumo-beta (dev) for updates since " + since,
		Body:  "dial tcp: connection refused",
	}}, f.n.got)

	f.n.got = nil
	f.at(24 * time.Hour)
	require.Error(t, f.app.AutoUpdate(ctx))
	assert.Empty(t, f.n.got)

	f.at(84 * time.Hour)
	require.Error(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, []notify.Message{{
		Title: "still can't check econumo-beta (dev) for updates since " + since + " (84h)",
		Body:  "dial tcp: connection refused",
	}}, f.n.got)

	f.n.got = nil
	f.st.err = nil
	f.at(85*time.Hour + 12*time.Minute + 20*time.Second)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, []string{
		"econumo-beta (dev) reachable again, was failing since " + since + " (85h12m)",
		"econumo-beta (dev) 1.4.0 → 1.5.0",
	}, f.n.titles())
	rs = f.ref(t, "econumo-beta")
	assert.True(t, rs.CheckFailingSince.IsZero())
	assert.True(t, rs.CheckNotifiedAt.IsZero())
}

func TestAutoUpdateChannelNotFoundIsCantCheck(t *testing.T) {
	f := autoEnv(t)
	for k := range f.store.Objects {
		if strings.HasPrefix(k, "econumo/dev/") {
			delete(f.store.Objects, k)
		}
	}
	err := f.app.AutoUpdate(context.Background())
	require.Error(t, err)
	assert.Empty(t, f.n.got)
	rs := f.ref(t, "econumo-beta")
	assert.True(t, rs.AutoUpdate)
	assert.True(t, rs.CheckFailingSince.Equal(later))
	assert.Contains(t, f.errOut.String(), `econumo-beta: channel "dev" not found for econumo`)
}

func TestAutoUpdateLockHeld(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	unlock, err := f.app.State.Lock()
	require.NoError(t, err)

	for _, d := range []time.Duration{0, 30 * time.Minute} {
		f.at(d)
		f.out.Reset()
		require.NoError(t, f.app.AutoUpdate(ctx))
		assert.Equal(t, "another ship run holds the state lock; trying again next run\n", f.out.String())
		assert.Empty(t, f.n.got)
	}
	f.at(61 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, []notify.Message{{
		Title: "automatic updates can't run since " + hm(later),
		Body:  "another ship run has held the state lock",
	}}, f.n.got)
	info, err := os.Stat(f.app.RunClockPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	unlock()
	f.n.got = nil
	f.at(62 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, []string{
		"automatic updates running again, were failing since " + hm(later) + " (1h2m)",
		"econumo-beta (dev) 1.4.0 → 1.5.0",
	}, f.n.titles())
}

func TestAutoUpdateLockHeldAfterSuccessResetsClock(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	unlock, err := f.app.State.Lock()
	require.NoError(t, err)
	require.NoError(t, f.app.AutoUpdate(ctx))
	unlock()
	f.at(30 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx)) // gets the lock: the clock resets
	unlock, err = f.app.State.Lock()
	require.NoError(t, err)
	defer unlock()
	f.n.got = nil
	f.at(70 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Empty(t, f.n.got, "only consecutive locked runs count")
}

func TestAutoUpdateCorruptRunClock(t *testing.T) {
	f := autoEnv(t)
	require.NoError(t, os.WriteFile(f.app.RunClockPath, []byte("{nope"), 0o600))
	unlock, err := f.app.State.Lock()
	require.NoError(t, err)
	defer unlock()

	require.NoError(t, f.app.AutoUpdate(context.Background()))
	assert.Contains(t, f.errOut.String(), "warning: ")
	assert.Contains(t, f.errOut.String(), "parse run clock")
	data, err := os.ReadFile(f.app.RunClockPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "another ship run has held the state lock")
}

func TestAutoUpdateStateUnreadable(t *testing.T) {
	t.Run("state", func(t *testing.T) {
		f := autoEnv(t)
		corruptState(t, f.env)
		err := f.app.AutoUpdate(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse state")
		assert.Empty(t, f.n.got)
		data, err := os.ReadFile(f.app.RunClockPath)
		require.NoError(t, err)
		assert.Contains(t, string(data), "parse state")

		f.at(time.Hour)
		require.Error(t, f.app.AutoUpdate(context.Background()))
		require.Len(t, f.n.got, 1)
		assert.Equal(t, "automatic updates can't run since "+hm(later), f.n.got[0].Title)
		assert.Contains(t, f.n.got[0].Body, "parse state")

		f.n.got = nil
		f.at(25 * time.Hour)
		require.Error(t, f.app.AutoUpdate(context.Background()))
		require.Len(t, f.n.got, 1)
		assert.Equal(t, "automatic updates still can't run since "+hm(later)+" (25h)", f.n.got[0].Title)
		assert.Contains(t, f.n.got[0].Body, "parse state")
	})
	t.Run("store", func(t *testing.T) {
		f := autoEnv(t)
		f.app.NewStore = func() (store.Store, error) { return nil, errors.New("R2 endpoint is not set") }
		require.EqualError(t, f.app.AutoUpdate(context.Background()), "R2 endpoint is not set")
		f.at(time.Hour)
		require.Error(t, f.app.AutoUpdate(context.Background()))
		assert.Equal(t, []notify.Message{{Title: "automatic updates can't run since " + hm(later), Body: "R2 endpoint is not set"}}, f.n.got)
		assert.Equal(t, "1.4.0", f.ref(t, "econumo-beta").Version)
	})
	t.Run("lock", func(t *testing.T) {
		f := autoEnv(t)
		breakLock(t, f.env)
		err := f.app.AutoUpdate(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "open lock")
	})
}

func TestAutoUpdatePurgesWithZoneAndToken(t *testing.T) {
	f := autoEnv(t)
	s := f.state(t)
	s.Services["econumo-beta"].CFZone = "z1"
	require.NoError(t, f.app.State.Save(s))
	cf := withCF(t, f.env)

	require.NoError(t, f.app.AutoUpdate(context.Background()))
	assert.Equal(t, []string{"/zones/z1/purge_cache"}, cf.paths)
	assert.Contains(t, f.out.String(), "Cloudflare cache purged")

	// A failed purge keeps the update and fails the run.
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	cf.reply = `{"success":false}`
	err := f.app.AutoUpdate(context.Background())
	require.Error(t, err)
	assert.Equal(t, "1.6.0", f.ref(t, "econumo-beta").Version)
	assert.Contains(t, f.errOut.String(), "econumo-beta: cache purge failed")
}

func TestAutoUpdateNoTokenSkipsPurge(t *testing.T) {
	f := autoEnv(t)
	s := f.state(t)
	s.Services["econumo-beta"].CFZone = "z1"
	require.NoError(t, f.app.State.Save(s))

	require.NoError(t, f.app.AutoUpdate(context.Background()))
	assert.Contains(t, f.errOut.String(), "econumo-beta: cache not purged: no Cloudflare token")
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-beta").Version)
}

func TestAutoUpdateNotifyFailureExitsNonZero(t *testing.T) {
	f := autoEnv(t)
	f.n.err = errors.New("send failed")
	err := f.app.AutoUpdate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 notification not sent")
	assert.Contains(t, f.errOut.String(), "send failed")
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-beta").Version)
}

func TestAutoUpdateWithoutNotifyURL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads only the system config")
	}
	shipEnv(t, "", 0)
	t.Setenv("SHIP_NOTIFY_URL", "")
	real, err := app.New(&bytes.Buffer{}, &bytes.Buffer{})
	require.NoError(t, err)
	f := autoEnv(t)
	f.app.Notifier = real.Notifier

	require.NoError(t, f.app.AutoUpdate(context.Background()))
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-beta").Version)

	// An invalid url fails the run before anything is touched.
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	real.Cfg.Notify.URL = "bogus://x"
	err = f.app.AutoUpdate(context.Background())
	require.ErrorIs(t, err, notify.ErrInvalidURL)
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-beta").Version)
}

func TestAutoUpdateStateHoldsNoErrorText(t *testing.T) {
	f := autoEnv(t)
	f.st.err = errors.New("secret-error-text-xyz")
	for _, d := range []time.Duration{0, time.Hour} {
		f.at(d)
		require.Error(t, f.app.AutoUpdate(context.Background()))
	}
	require.Len(t, f.n.got, 1)
	data, err := os.ReadFile(f.app.State.Path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "secret-error-text-xyz")
	assert.Contains(t, string(data), "check_failing_since")
}

func TestAutoUpdateStateNotSavedAfterUpdateHalts(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	require.NoError(t, f.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-z", Version: "dev", AutoUpdate: true}))
	f.sys.Active["econumo-z"] = true
	publish(t, f.store, "econumo", "1.6.0", []string{"dev"}, true, "econumo")
	readOnlyStateDir(t, f.env)
	bin := f.app.Host.Paths.Bin("econumo-beta", "econumo")
	f.out.Reset()

	err := f.app.AutoUpdate(ctx)
	require.Error(t, err)
	require.Len(t, f.n.got, 1, "no Updated message, and the run stops")
	assert.Equal(t, "econumo-beta (dev) updated to 1.6.0 but the state could not be saved; automatic updates halted", f.n.got[0].Title)
	assert.Contains(t, f.n.got[0].Body, "create temp state file")
	assert.Equal(t, 1, restarts(f.sys.Calls, "econumo-beta"))
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-z"), "later refs are not processed")
	assert.Equal(t, "1.4.0", f.ref(t, "econumo-beta").Version)
	prev := readFile(t, bin+".prev")
	assert.Contains(t, prev, "1.4.0")
	clock := readFile(t, f.app.RunClockPath)
	assert.Contains(t, clock, `"pending"`, "the swap is remembered for the next run")
	assert.Contains(t, clock, `"prev_version": "1.4.0"`)

	// The state still cannot be saved: the pending build is recorded again,
	// without a restart and without another halt message.
	f.n.got = nil
	f.sys.Calls = nil
	f.at(5 * time.Minute)
	require.Error(t, f.app.AutoUpdate(ctx))
	assert.Empty(t, f.n.got)
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))
	assert.Contains(t, f.errOut.String(), "create temp state file")

	// Once the state saves, the next run records it and goes on.
	require.NoError(t, os.Chmod(filepath.Dir(f.app.State.Path), 0o755))
	f.sys.Calls = nil
	f.out.Reset()
	f.at(10 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))
	assert.Equal(t, prev, readFile(t, bin+".prev"))
	assert.Equal(t, []string{"econumo-z (dev) 1.5.0 → 1.6.0"}, f.n.titles())
	assert.Contains(t, f.out.String(), "econumo-beta: already installed 1.6.0, state recorded\n")
	rs := f.ref(t, "econumo-beta")
	assert.Equal(t, "1.6.0", rs.Version)
	assert.Equal(t, "1.4.0", rs.PrevVersion)
	assert.True(t, rs.AutoUpdate)
	assert.NotContains(t, readFile(t, f.app.RunClockPath), `"pending"`, "the marker is cleared")
}

func TestAutoUpdateLiveBuildWithoutMarkerIsUpdated(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	require.NoError(t, f.app.AutoUpdate(ctx))
	// The state says 1.4.0 while 1.5.0 is live, with no pending marker (it
	// was not ship that left it so): go through the full update.
	s := f.state(t)
	rs := s.Services["econumo-beta"].Refs["econumo"]
	rs.Version, rs.PrevVersion = "1.4.0", ""
	require.NoError(t, f.app.State.Save(s))
	f.sys.Calls = nil
	f.out.Reset()

	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, 1, restarts(f.sys.Calls, "econumo-beta"), "restarted and health-checked")
	assert.NotContains(t, f.out.String(), "already installed")
	assert.Equal(t, "1.5.0", f.ref(t, "econumo-beta").Version)
}

func TestAutoUpdateSkipsRefThatIsNotRunning(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	f.sys.Active["econumo-beta"] = false
	f.st.err = errors.New("dial tcp: connection refused")
	s := f.state(t)
	s.Services["econumo-beta"].Refs["econumo"].CheckFailingSince = fixedNow
	require.NoError(t, f.app.State.Save(s))

	f.at(2 * time.Hour)
	require.NoError(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, "econumo-beta: not running, skipped\n", f.out.String())
	assert.Empty(t, f.n.got)
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))
	assert.NotContains(t, f.sys.Calls, "systemctl start econumo-beta")
	rs := f.ref(t, "econumo-beta")
	assert.True(t, rs.CheckFailingSince.IsZero(), "a skipped ref's check clock starts over")
	assert.Equal(t, "1.4.0", rs.Version)
	assert.True(t, rs.AutoUpdate)

	// Started again, a single failed check does not alert with an old "since".
	f.sys.Active["econumo-beta"] = true
	require.Error(t, f.app.AutoUpdate(ctx))
	assert.Empty(t, f.n.got)
}

func TestAutoUpdateRechecksRunningBeforeSwap(t *testing.T) {
	f := autoEnv(t)
	// ship stop lands while the release is fetched: running at the first
	// check, stopped at the second.
	f.sys.ActiveAfterRestart = func(u string) bool {
		n := 0
		for _, c := range f.sys.Calls {
			if c == "ActiveState "+u {
				n++
			}
		}
		return n < 2
	}
	require.NoError(t, f.app.AutoUpdate(context.Background()))
	assert.Equal(t, "econumo-beta: not running, skipped\n", f.out.String())
	assert.Equal(t, 0, restarts(f.sys.Calls, "econumo-beta"))
	assert.Empty(t, f.n.got)
	assert.Equal(t, "1.4.0", f.ref(t, "econumo-beta").Version)
}

func TestAutoUpdateActiveStateErrorIsCantCheck(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	f.sys.Fail = map[string]error{"ActiveState econumo-beta": errors.New("Failed to connect to bus")}

	err := f.app.AutoUpdate(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 1 refs failed")
	assert.NotContains(t, f.out.String(), "skipped")
	assert.Contains(t, f.errOut.String(), "econumo-beta: Failed to connect to bus")
	rs := f.ref(t, "econumo-beta")
	assert.True(t, rs.CheckFailingSince.Equal(later))
	assert.True(t, rs.AutoUpdate)
	assert.Empty(t, f.n.got)

	f.at(time.Hour)
	require.Error(t, f.app.AutoUpdate(ctx))
	assert.Equal(t, []string{"can't check econumo-beta (dev) for updates since " + hm(later)}, f.n.titles())
}

func TestAutoUpdateInterruptedCommandKeepsAutoUpdate(t *testing.T) {
	f := autoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	// SIGTERM kills systemd-analyze: its error does not say context.Canceled.
	f.sys.VerifyErr = errors.New("signal: killed")
	cancel()

	err := f.app.AutoUpdate(ctx)
	require.Error(t, err)
	assert.Empty(t, f.n.got)
	rs := f.ref(t, "econumo-beta")
	assert.True(t, rs.AutoUpdate)
	assert.True(t, rs.AutoUpdateStoppedAt.IsZero())
	assert.Equal(t, "1.4.0", rs.Version)
}

func TestAutoUpdateNothingOptedInTouchesNothing(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	require.NoError(t, f.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", AutoUpdate: ptr(false)}))
	f.n.got = nil
	f.app.NewStore = func() (store.Store, error) {
		t.Fatal("no store is opened with nothing to update")
		return nil, nil
	}
	for _, d := range []time.Duration{0, 2 * time.Hour} {
		f.at(d)
		require.NoError(t, f.app.AutoUpdate(ctx))
	}
	assert.Empty(t, f.n.got)
	assert.NoFileExists(t, f.app.RunClockPath)

	// A failing clock from earlier runs starts over, silently (no recovery
	// message); a pending marker is kept.
	clock := `{"failing_since":"2026-10-29T12:00:00Z","notified_at":"2026-10-29T13:00:00Z","reason":"R2 endpoint is not set",` +
		`"pending":{"service":"econumo-beta","ref":"econumo","version":"1.5.0"}}`
	require.NoError(t, os.WriteFile(f.app.RunClockPath, []byte(clock), 0o600))
	require.NoError(t, f.app.AutoUpdate(ctx))
	got := readFile(t, f.app.RunClockPath)
	assert.NotContains(t, got, "failing_since")
	assert.NotContains(t, got, "R2 endpoint")
	assert.Contains(t, got, `"version": "1.5.0"`)
	assert.Empty(t, f.n.got)
}

func TestAutoUpdateLockHeldExitsZero(t *testing.T) {
	f := autoEnv(t)
	ctx := context.Background()
	unlock, err := f.app.State.Lock()
	require.NoError(t, err)
	defer unlock()
	f.n.err = errors.New("send failed")
	require.NoError(t, f.app.AutoUpdate(ctx))
	f.at(61 * time.Minute)
	require.NoError(t, f.app.AutoUpdate(ctx), "an unsent notification does not fail a lock-held run")
	assert.Contains(t, f.errOut.String(), "send failed")

	f.app.RunClockPath = t.TempDir() // a directory: the clock cannot be saved
	require.NoError(t, f.app.AutoUpdate(ctx), "an unsaved clock does not fail a lock-held run")
	assert.Contains(t, f.errOut.String(), "warning: ")
}

func TestAutoUpdateInterruptedBeforeRunningCheck(t *testing.T) {
	f := autoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.sys.Active["econumo-beta"] = false // what systemctl reports once killed
	cancel()
	err := f.app.AutoUpdate(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, f.out.String())
	assert.Empty(t, f.n.got)
}

func TestTurningAutoUpdateOnWarnsAboutShellConfig(t *testing.T) {
	e := newEnv(t)
	errOut := &bytes.Buffer{}
	e.app.Err = errOut
	e.app.Environ = func() []string {
		return []string{"SHIP_R2_SECRET_ACCESS_KEY=s", "PATH=/bin", "SHIP_NOTIFY_URL=ntfy://x", "SHIP_R2_BUCKET="}
	}
	ctx := context.Background()
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", AutoUpdate: true}))
	want := "warning: SHIP_NOTIFY_URL is set in this shell; the auto-update timer reads only /etc/ship/config.toml\n" +
		"warning: SHIP_R2_SECRET_ACCESS_KEY is set in this shell; the auto-update timer reads only /etc/ship/config.toml\n"
	assert.Equal(t, want, errOut.String())

	errOut.Reset()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(true)}))
	assert.Equal(t, want, errOut.String())

	// Turning it off, or a plain install, warns about nothing.
	errOut.Reset()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", AutoUpdate: ptr(false)}))
	require.NoError(t, e.app.Install(ctx, app.InstallArgs{Ref: "twillingate", Version: "v2"}))
	assert.Empty(t, errOut.String())
}
