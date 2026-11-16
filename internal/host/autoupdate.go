package host

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// AutoUpdateUnit names the systemd service and timer that run
// `ship update --auto` on a schedule.
const AutoUpdateUnit = "ship-auto-update"

const autoUpdateTimer = `[Unit]
Description=Run ship automatic updates every 5 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min
RandomizedDelaySec=1min

[Install]
WantedBy=timers.target
`

func autoUpdateService(shipPath string) []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=ship automatic updates
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
TimeoutStartSec=30min
ExecStart=%s update --auto
`, shipPath))
}

// EnsureAutoUpdateTimer writes the auto-update service and timer units for
// the ship binary at shipPath, then daemon-reloads (only when a unit changed)
// and enables and starts the timer. Running it again with the same content
// does no reload.
func (h *Host) EnsureAutoUpdateTimer(ctx context.Context, shipPath string) error {
	p := h.Paths
	units := []struct {
		path string
		data []byte
	}{
		{p.UnitPath(AutoUpdateUnit), autoUpdateService(shipPath)},
		{p.TimerPath(AutoUpdateUnit), []byte(autoUpdateTimer)},
	}
	changed := false
	for _, u := range units {
		if cur, err := os.ReadFile(u.path); err == nil && bytes.Equal(cur, u.data) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(u.path), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(u.path, u.data, 0o644); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if err := h.Sys.DaemonReload(ctx); err != nil {
			return fmt.Errorf("daemon-reload: %w", err)
		}
	}
	timer := AutoUpdateUnit + ".timer"
	if err := h.Sys.Systemctl(ctx, "enable", "--now", timer); err != nil {
		return fmt.Errorf("enable %s: %w", timer, err)
	}
	return nil
}
