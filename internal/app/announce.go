package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/dmtrkzntsv/ship/internal/config"
	"github.com/dmtrkzntsv/ship/internal/notify"
)

// notifier returns the configured Notifier, or a no-op when none is wired.
func (a *App) notifier() (Notifier, error) {
	if a.Notifier == nil {
		return noopNotifier{}, nil
	}
	return a.Notifier()
}

// announceAutoUpdate prints and notifies a change of a ref's auto-update
// setting made by an operator command ("on" or "off").
func (a *App) announceAutoUpdate(ctx context.Context, unit, channel, what string) error {
	fmt.Fprintf(a.Out, "%s: auto-update %s\n", unit, what)
	return a.notifyAutoUpdate(ctx, unit, channel, what, "")
}

// notifyAutoUpdate sends "auto-update <what> for <unit> (<channel>)", with
// " after <why>" when why is set. The setting is already saved, so a failed
// send is reported as an error but changes nothing.
func (a *App) notifyAutoUpdate(ctx context.Context, unit, channel, what, why string) error {
	title := fmt.Sprintf("auto-update %s for %s (%s)", what, unit, channel)
	if why != "" {
		title += " after " + why
	}
	n, err := a.notifier()
	if err == nil {
		err = n.Notify(ctx, notify.Message{Title: title})
	}
	if err != nil {
		return fmt.Errorf("notification not sent: %w", err)
	}
	return nil
}

// ErrNoNotifyURL is returned by NotifyTest when no notification url is set.
var ErrNoNotifyURL = errors.New("no notification url is set (config [notify] url in " + config.SystemPath + " or SHIP_NOTIFY_URL)")

// NotifyTest sends a test message through the configured notification url,
// the same way automatic updates do.
func (a *App) NotifyTest(ctx context.Context) error {
	if a.Cfg.Notify.URL == "" {
		return ErrNoNotifyURL
	}
	n, err := a.notifier()
	if err != nil {
		return err
	}
	if err := n.Notify(ctx, notify.Message{Title: "test notification from ship"}); err != nil {
		return fmt.Errorf("test notification not sent: %w", err)
	}
	fmt.Fprintln(a.Out, "test notification sent")
	return nil
}
