package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func TestInstallRequiresVersionFlag(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"install", "econumo"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"version" not set`)
}

func TestInstallRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"install", "econumo", "--version", "dev"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ship install:")
}

func TestUpdateAndRollbackRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, cmd := range []string{"update", "rollback"} {
		root := NewRoot(io.Discard, io.Discard)
		root.SetArgs([]string{cmd, "econumo", "--service", "econumo-beta"})
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ship "+cmd+":")
	}
}

func TestOpsCommandsRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, cmd := range []string{"uninstall", "start", "stop", "restart", "run"} {
		root := NewRoot(io.Discard, io.Discard)
		root.SetArgs([]string{cmd, "econumo"})
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ship "+cmd+": must run as root")
	}
}

// fakeGuards lets update and install run without root and records what they
// pass to the app.
func fakeGuards(t *testing.T) (updates *[]app.UpdateArgs, installs *[]app.InstallArgs, autoRuns *int) {
	t.Helper()
	useFakeApp(t)
	oldG, oldU, oldA, oldI := hostGuard, runUpdate, runAutoUpdate, runInstall
	t.Cleanup(func() { hostGuard, runUpdate, runAutoUpdate, runInstall = oldG, oldU, oldA, oldI })
	var us []app.UpdateArgs
	var is []app.InstallArgs
	var n int
	hostGuard = func(string) error { return nil }
	runUpdate = func(_ context.Context, _ *app.App, a app.UpdateArgs) error { us = append(us, a); return nil }
	runAutoUpdate = func(context.Context, *app.App) error { n++; return nil }
	runInstall = func(_ context.Context, _ *app.App, a app.InstallArgs) error { is = append(is, a); return nil }
	return &us, &is, &n
}

func TestUpdateAutoRejectsRef(t *testing.T) {
	_, _, autoRuns := fakeGuards(t)
	_, _, err := runCLI(t, "update", "--auto", "econumo")
	assert.EqualError(t, err, "--auto takes no ref and no other flags")
	assert.Zero(t, *autoRuns)
}

func TestUpdateAutoRejectsOtherFlags(t *testing.T) {
	_, _, autoRuns := fakeGuards(t)
	for _, flag := range [][]string{
		{"--service", "x"}, {"--version", "dev"}, {"--cf-zone", "z"}, {"--purge-cache"}, {"--auto-update=false"},
	} {
		_, _, err := runCLI(t, append([]string{"update", "--auto"}, flag...)...)
		assert.EqualError(t, err, "--auto takes no ref and no other flags", "%v", flag)
	}
	assert.Zero(t, *autoRuns)
}

func TestUpdateAutoRunsTheAutoUpdate(t *testing.T) {
	updates, _, autoRuns := fakeGuards(t)
	_, _, err := runCLI(t, "update", "--auto")
	require.NoError(t, err)
	assert.Equal(t, 1, *autoRuns)
	assert.Empty(t, *updates)

	runAutoUpdate = func(context.Context, *app.App) error { return errors.New("boom") }
	_, _, err = runCLI(t, "update", "--auto")
	assert.EqualError(t, err, "boom")
}

func TestUpdateWithoutAutoNeedsOneRef(t *testing.T) {
	fakeGuards(t)
	_, _, err := runCLI(t, "update")
	require.Error(t, err)
	_, _, err = runCLI(t, "update", "a", "b")
	require.Error(t, err)
}

func TestUpdateAutoUpdateFlagUnsetPassesNil(t *testing.T) {
	updates, _, _ := fakeGuards(t)
	_, _, err := runCLI(t, "update", "econumo")
	require.NoError(t, err)
	_, _, err = runCLI(t, "update", "econumo", "--auto-update")
	require.NoError(t, err)
	_, _, err = runCLI(t, "update", "econumo", "--auto-update=false")
	require.NoError(t, err)

	require.Len(t, *updates, 3)
	assert.Nil(t, (*updates)[0].AutoUpdate)
	require.NotNil(t, (*updates)[1].AutoUpdate)
	assert.True(t, *(*updates)[1].AutoUpdate)
	require.NotNil(t, (*updates)[2].AutoUpdate)
	assert.False(t, *(*updates)[2].AutoUpdate)
}

func TestInstallAutoUpdateFlag(t *testing.T) {
	_, installs, _ := fakeGuards(t)
	_, _, err := runCLI(t, "install", "econumo", "--version", "dev")
	require.NoError(t, err)
	_, _, err = runCLI(t, "install", "econumo", "--version", "dev", "--auto-update")
	require.NoError(t, err)

	require.Len(t, *installs, 2)
	assert.False(t, (*installs)[0].AutoUpdate)
	assert.True(t, (*installs)[1].AutoUpdate)
}
