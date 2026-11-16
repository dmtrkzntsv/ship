package app

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/dmtrkzntsv/ship/internal/notify"
)

// Notifier delivers a notification for ship update --auto.
type Notifier interface {
	Notify(ctx context.Context, m notify.Message) error
}

// retrySleeps are the waits between the (at most three) send attempts.
var retrySleeps = []time.Duration{5 * time.Second, 15 * time.Second}

type notifier struct {
	sender notify.Sender
	host   string
	sleep  func(time.Duration)
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, notify.Message) error { return nil }

// newNotifier prefixes every message with the host name and retries
// notify.ErrTemporary failures; a nil sender yields a no-op Notifier.
func newNotifier(sender notify.Sender, host string, sleep func(time.Duration)) Notifier {
	if sender == nil {
		return noopNotifier{}
	}
	return &notifier{sender: sender, host: host, sleep: sleep}
}

func (n *notifier) Notify(ctx context.Context, m notify.Message) error {
	prefix := n.host + ": "
	if m.Title != "" {
		m.Title = prefix + m.Title
	} else {
		m.Body = prefix + m.Body
	}
	var err error
	for attempt := 0; ; attempt++ {
		err = n.sender.Send(ctx, m)
		if err == nil || !errors.Is(err, notify.ErrTemporary) || attempt >= len(retrySleeps) {
			return err
		}
		n.sleep(retrySleeps[attempt])
	}
}

// configNotifier builds the production Notifier for a notification URL; an
// empty URL yields a no-op.
func configNotifier(url string) (Notifier, error) {
	if url == "" {
		return noopNotifier{}, nil
	}
	sender, err := notify.Parse(url)
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return newNotifier(sender, host, time.Sleep), nil
}
