package host_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
)

func TestFakeSysFail(t *testing.T) {
	boom := errors.New("boom")
	ctx := context.Background()
	fs := &host.FakeSys{Fail: map[string]error{
		"DaemonReload": boom, "VerifyUnit /u": boom, "systemctl start u": boom,
		"Chown /p root:tw": boom, "Owner /p": boom,
	}}
	assert.ErrorIs(t, fs.DaemonReload(ctx), boom)
	assert.ErrorIs(t, fs.VerifyUnit(ctx, "/u"), boom)
	assert.ErrorIs(t, fs.Systemctl(ctx, "start", "u"), boom)
	assert.False(t, fs.Active["u"], "a failed call has no effect")
	assert.ErrorIs(t, fs.Chown("/p", "root:tw"), boom)
	_, _, err := fs.Owner("/p")
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, fs.Owners)
	assert.Equal(t, []string{"DaemonReload", "VerifyUnit /u", "systemctl start u", "Chown /p root:tw"}, fs.Calls)

	// Calls not listed behave as before.
	assert.NoError(t, fs.Systemctl(ctx, "start", "v"))
	assert.True(t, fs.Active["v"])
}

func TestFakeSysActiveState(t *testing.T) {
	f := &host.FakeSys{Active: map[string]bool{"a": true}}
	st, err := f.ActiveState(context.Background(), "a")
	require.NoError(t, err)
	assert.Equal(t, "active", st)
	st, err = f.ActiveState(context.Background(), "b")
	require.NoError(t, err)
	assert.Equal(t, "inactive", st)
	f.ActiveAfterRestart = func(string) bool { return false }
	st, _ = f.ActiveState(context.Background(), "a")
	assert.Equal(t, "inactive", st)
	f.Fail = map[string]error{"ActiveState a": assert.AnError}
	_, err = f.ActiveState(context.Background(), "a")
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"ActiveState a", "ActiveState b", "ActiveState a", "ActiveState a"}, f.Calls)
}
