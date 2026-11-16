package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

// checkOwnUnit refuses path unless it is absent or a regular file holding
// mark: a symlink (dangling or not), a directory or an unreadable entry is
// someone else's.
func checkOwnUnit(path, mark string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	notOurs := fmt.Errorf("%s exists and is not ship's auto-update unit; remove it first", path)
	if err != nil || !fi.Mode().IsRegular() {
		return notOurs
	}
	cur, err := os.ReadFile(path)
	if err != nil || !hasLine(cur, mark) {
		return notOurs
	}
	return nil
}

// hasLine reports whether data holds line as one whole line.
func hasLine(data []byte, line string) bool {
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// AutoUpdateUnit names the systemd service and timer that run
// `ship update --auto` on a schedule.
const AutoUpdateUnit = layout.AutoUpdateUnit

// The Description lines mark the units as ship's: a file at either path
// without its line belongs to someone else and is never overwritten.
const (
	autoUpdateServiceMark = "Description=ship automatic updates"
	autoUpdateTimerMark   = "Description=Run ship automatic updates every 5 minutes"
)

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
// the ship binary at shipPath, then daemon-reloads and enables and starts
// the timer. It always reloads, so a reload that failed on an earlier call
// is retried even when the files are already in place. A file at either
// path that is not ship's is refused before anything is written.
func (h *Host) EnsureAutoUpdateTimer(ctx context.Context, shipPath string) error {
	p := h.Paths
	units := []struct {
		path, mark string
		data       []byte
	}{
		{p.UnitPath(AutoUpdateUnit), autoUpdateServiceMark, autoUpdateService(shipPath)},
		{p.TimerPath(AutoUpdateUnit), autoUpdateTimerMark, []byte(autoUpdateTimer)},
	}
	for _, u := range units {
		if err := checkOwnUnit(u.path, u.mark); err != nil {
			return err
		}
	}
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
	}
	if err := h.Sys.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	timer := AutoUpdateUnit + ".timer"
	if err := h.Sys.Systemctl(ctx, "enable", "--now", timer); err != nil {
		return fmt.Errorf("enable %s: %w", timer, err)
	}
	return nil
}
