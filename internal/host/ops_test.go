package host_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

func TestLogsArgsNamespace(t *testing.T) {
	h, _ := newHost(t)
	unit := h.Paths.UnitPath("wf")
	require.NoError(t, os.MkdirAll(filepath.Dir(unit), 0o755))
	require.NoError(t, os.WriteFile(unit, []byte("[Service]\nLogNamespace=winterflow\n"), 0o644))
	assert.Equal(t, []string{"journalctl", "-u", "wf.service", "--namespace=winterflow", "-n", "100", "-f"}, h.LogsArgs("wf", 100, true))
	assert.Equal(t, []string{"journalctl", "-u", "wf.service", "--namespace=winterflow", "-n", "5"}, h.LogsArgs("wf", 5, false))
}

func TestLogsArgsNoUnitFile(t *testing.T) {
	h, _ := newHost(t)
	assert.Equal(t, []string{"journalctl", "-u", "x.service", "-n", "100", "-f"}, h.LogsArgs("x", 100, true))
}

func TestRunArgs(t *testing.T) {
	h, _ := newHost(t)
	got := h.RunArgs("S", layout.Ref{Project: "P", Component: "N"}, []string{"currency:update-rates", "--x"}, true)
	assert.Equal(t, []string{
		"systemd-run", "--pty", "--quiet", "--wait", "--collect",
		"--property=EnvironmentFile=/etc/S/N.env", "--property=User=S",
		"--property=WorkingDirectory=/var/lib/S", "--property=ReadWritePaths=/var/lib/S",
		"/opt/S/P-N", "currency:update-rates", "--x",
	}, got)
}

func TestRunArgsWithoutTerminal(t *testing.T) {
	h, _ := newHost(t)
	got := h.RunArgs("S", layout.Ref{Project: "P"}, []string{"echo", "hi"}, false)
	assert.Equal(t, []string{
		"systemd-run", "--pipe", "--quiet", "--wait", "--collect",
		"--property=EnvironmentFile=/etc/S/P.env", "--property=User=S",
		"--property=WorkingDirectory=/var/lib/S", "--property=ReadWritePaths=/var/lib/S",
		"/opt/S/P", "echo", "hi",
	}, got)
}

func TestStartRestartStop(t *testing.T) {
	h, fs := installed(t)
	h.HealthWait, h.HealthTick = 20*time.Millisecond, 5*time.Millisecond
	ctx := context.Background()
	require.NoError(t, h.Start(ctx, "twillingate-sql"))
	assert.Contains(t, fs.Calls, "systemctl start twillingate-sql")
	require.NoError(t, h.Restart(ctx, "twillingate-sql"))
	assert.Contains(t, fs.Calls, "systemctl restart twillingate-sql")
	require.NoError(t, h.Stop(ctx, "twillingate-sql"))
	assert.False(t, fs.Active["twillingate-sql"])

	fs.ActiveAfterRestart = func(string) bool { return false }
	fs.Journal = "boom"
	err := h.Start(ctx, "twillingate-sql")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
