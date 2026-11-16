package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/notify"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// isMoving reports whether channel can move to new builds: a built-in
// channel or one the fetched release was published to.
func isMoving(channel string, rel release.Release) bool {
	return layout.BuiltinChannel(channel) || rel.Channel == channel
}

// notMovingError refuses auto-update on a channel that names a fixed version.
func notMovingError(unit, channel, hint string) error {
	return fmt.Errorf("%s follows version %s, which never changes; auto-update needs a moving channel (latest, dev, edge, \u2026)%s", unit, channel, hint)
}

// stopAutoUpdate turns auto-update off after a failed automatic update or a
// manual rollback, and drops the check-failure clock.
func stopAutoUpdate(rs *state.RefState, now time.Time) {
	rs.AutoUpdate = false
	rs.AutoUpdateStoppedAt = now
	rs.CheckFailingSince = time.Time{}
	rs.CheckNotifiedAt = time.Time{}
}

// applyAutoUpdate records an explicit --auto-update setting (nil leaves the
// ref alone). Either way the stop marker goes; turning off also clears the
// check clock.
func applyAutoUpdate(rs *state.RefState, on *bool) {
	if on == nil {
		return
	}
	rs.AutoUpdate = *on
	rs.AutoUpdateStoppedAt = time.Time{}
	if !*on {
		rs.CheckFailingSince = time.Time{}
		rs.CheckNotifiedAt = time.Time{}
	}
}

// checkAutoUpdate validates what turning auto-update on needs, before
// anything is touched: the notification url and a usable ship path.
func (a *App) checkAutoUpdate() (string, error) {
	if a.Cfg.Notify.URL != "" {
		if _, err := notify.Parse(a.Cfg.Notify.URL); err != nil {
			return "", err
		}
	}
	if a.ShipPath == nil {
		return "", errors.New("cannot locate the ship binary for the auto-update timer")
	}
	path, err := a.ShipPath()
	if err != nil {
		return "", fmt.Errorf("locate the ship binary: %w", err)
	}
	if strings.IndexFunc(path, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("ship path %q contains whitespace; move the binary (for example to /usr/local/bin/ship) before turning on auto-update", path)
	}
	return path, nil
}

// enableAutoUpdate installs and starts the auto-update timer.
func (a *App) enableAutoUpdate(ctx context.Context) error {
	path, err := a.checkAutoUpdate()
	if err != nil {
		return err
	}
	return a.Host.EnsureAutoUpdateTimer(ctx, path)
}
