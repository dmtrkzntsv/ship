package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func TestNotifyTest(t *testing.T) {
	env := useFakeApp(t)
	oldRoot, oldEnv := isRoot, getenv
	t.Cleanup(func() { isRoot, getenv = oldRoot, oldEnv })
	getenv = func(string) string { return "" }
	oldUnreadable := systemConfigUnreadable
	t.Cleanup(func() { systemConfigUnreadable = oldUnreadable })
	systemConfigUnreadable = func() bool { return true }

	isRoot = func() bool { return true }
	_, _, err := runCLI(t, "notify", "test")
	assert.EqualError(t, err, "no notification url is set (config [notify] url in /etc/ship/config.toml or SHIP_NOTIFY_URL)")

	isRoot = func() bool { return false }
	_, _, err = runCLI(t, "notify", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "try: sudo ship notify test")

	// A missing or readable host config is not blamed on permissions.
	systemConfigUnreadable = func() bool { return false }
	_, _, err = runCLI(t, "notify", "test")
	assert.ErrorIs(t, err, app.ErrNoNotifyURL)
	assert.NotContains(t, err.Error(), "readable only by root")

	env.cfg.Notify.URL = "ntfy://ntfy.sh/topic"
	out, errOut, err := runCLI(t, "notify", "test")
	require.NoError(t, err)
	assert.Equal(t, "test notification sent\n", out)
	assert.Contains(t, errOut, "not root, so this tested the settings readable as you")

	isRoot = func() bool { return true }
	out, errOut, err = runCLI(t, "notify", "test")
	require.NoError(t, err)
	assert.Equal(t, "test notification sent\n", out)
	assert.Empty(t, errOut)

	_, _, err = runCLI(t, "notify", "test", "extra")
	assert.Error(t, err)

	getenv = func(k string) string {
		if k == "SHIP_NOTIFY_URL" {
			return "ntfy://ntfy.sh/other"
		}
		return ""
	}
	_, errOut, err = runCLI(t, "notify", "test")
	require.NoError(t, err)
	assert.Contains(t, errOut, "warning: SHIP_NOTIFY_URL is set in this shell")
}
