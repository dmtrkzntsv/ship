package host_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
)

const autoUpdateServiceWant = `[Unit]
Description=ship automatic updates
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
TimeoutStartSec=30min
ExecStart=/usr/local/bin/ship update --auto
`

const autoUpdateTimerWant = `[Unit]
Description=Run ship automatic updates every 5 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min
RandomizedDelaySec=1min

[Install]
WantedBy=timers.target
`

func TestEnsureAutoUpdateTimerWritesUnits(t *testing.T) {
	h, fs := newHost(t)
	require.NoError(t, h.EnsureAutoUpdateTimer(context.Background(), "/usr/local/bin/ship"))

	svc := h.Paths.UnitPath(host.AutoUpdateUnit)
	timer := h.Paths.TimerPath(host.AutoUpdateUnit)
	b, err := os.ReadFile(svc)
	require.NoError(t, err)
	assert.Equal(t, autoUpdateServiceWant, string(b))
	assert.Equal(t, os.FileMode(0o644), mode(t, svc))
	b, err = os.ReadFile(timer)
	require.NoError(t, err)
	assert.Equal(t, autoUpdateTimerWant, string(b))
	assert.Equal(t, os.FileMode(0o644), mode(t, timer))

	n := len(fs.Calls)
	require.GreaterOrEqual(t, n, 2)
	assert.Equal(t, []string{"DaemonReload", "systemctl enable --now ship-auto-update.timer"}, fs.Calls[n-2:])
}

func TestEnsureAutoUpdateTimerIdempotent(t *testing.T) {
	h, fs := newHost(t)
	ctx := context.Background()
	require.NoError(t, h.EnsureAutoUpdateTimer(ctx, "/usr/local/bin/ship"))
	fs.Calls = nil

	require.NoError(t, h.EnsureAutoUpdateTimer(ctx, "/usr/local/bin/ship"))
	assert.Equal(t, []string{"DaemonReload", "systemctl enable --now ship-auto-update.timer"}, fs.Calls,
		"always reloads, so a reload that failed last time is retried")
}

func TestEnsureAutoUpdateTimerRefusesForeignUnits(t *testing.T) {
	for _, c := range []struct {
		name string
		path func(h *host.Host) string
		kind string
	}{
		{"service", func(h *host.Host) string { return h.Paths.UnitPath(host.AutoUpdateUnit) }, ".service"},
		{"timer", func(h *host.Host) string { return h.Paths.TimerPath(host.AutoUpdateUnit) }, ".timer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, fs := newHost(t)
			p := c.path(h)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("[Unit]\nDescription=my own thing\n"), 0o644))
			err := h.EnsureAutoUpdateTimer(context.Background(), "/usr/local/bin/ship")
			require.EqualError(t, err, p+" exists and is not ship's auto-update unit; remove it first")
			assert.Empty(t, fs.Calls)
			assert.Equal(t, "[Unit]\nDescription=my own thing\n", read(t, p))
		})
	}
	// ship's own units from an older ship (another ExecStart) are rewritten.
	h, _ := newHost(t)
	svc := h.Paths.UnitPath(host.AutoUpdateUnit)
	require.NoError(t, os.MkdirAll(filepath.Dir(svc), 0o755))
	require.NoError(t, os.WriteFile(svc, []byte("[Unit]\nDescription=ship automatic updates\n[Service]\nExecStart=/old/ship update --auto\n"), 0o644))
	require.NoError(t, h.EnsureAutoUpdateTimer(context.Background(), "/usr/local/bin/ship"))
	assert.Equal(t, autoUpdateServiceWant, read(t, svc))
}

func TestEnsureAutoUpdateTimerRewritesChangedPath(t *testing.T) {
	h, fs := newHost(t)
	ctx := context.Background()
	require.NoError(t, h.EnsureAutoUpdateTimer(ctx, "/usr/local/bin/ship"))
	fs.Calls = nil

	require.NoError(t, h.EnsureAutoUpdateTimer(ctx, "/opt/ship"))
	b, err := os.ReadFile(h.Paths.UnitPath(host.AutoUpdateUnit))
	require.NoError(t, err)
	assert.Contains(t, string(b), "ExecStart=/opt/ship update --auto\n")
	assert.Equal(t, []string{"DaemonReload", "systemctl enable --now ship-auto-update.timer"}, fs.Calls)
}

func TestEnsureAutoUpdateTimerErrors(t *testing.T) {
	ctx := context.Background()
	boom := assert.AnError

	h, fs := newHost(t)
	fs.ReloadErr = boom
	err := h.EnsureAutoUpdateTimer(ctx, "/usr/local/bin/ship")
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "daemon-reload: ")

	h, fs = newHost(t)
	fs.EnableErr = boom
	err = h.EnsureAutoUpdateTimer(ctx, "/usr/local/bin/ship")
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "enable ship-auto-update.timer: ")
}
