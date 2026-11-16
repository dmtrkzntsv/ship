package app

import (
	"context"
	"errors"
	"fmt"
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

// sendTimeout bounds one Notify, all attempts and waits included.
const sendTimeout = 30 * time.Second

type notifier struct {
	sender  notify.Sender
	host    string
	wait    func(context.Context, time.Duration) error
	timeout time.Duration
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, notify.Message) error { return nil }

// newNotifier prefixes every message with the host name and retries
// notify.ErrTemporary failures, waiting with wait, all within sendTimeout;
// a nil sender yields a no-op Notifier.
func newNotifier(sender notify.Sender, host string, wait func(context.Context, time.Duration) error) Notifier {
	if sender == nil {
		return noopNotifier{}
	}
	return &notifier{sender: sender, host: host, wait: wait, timeout: sendTimeout}
}

func (n *notifier) Notify(ctx context.Context, m notify.Message) error {
	prefix := n.host + ": "
	if m.Title != "" {
		m.Title = prefix + m.Title
	} else {
		m.Body = prefix + m.Body
	}
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		err := n.sender.Send(ctx, m)
		if err == nil || !errors.Is(err, notify.ErrTemporary) || attempt >= len(retrySleeps) {
			return err
		}
		if werr := n.wait(ctx, retrySleeps[attempt]); werr != nil {
			return fmt.Errorf("%w; not retried: %w", err, werr)
		}
	}
}

// sleepCtx waits for d, or until ctx ends (returning its error).
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
	return newNotifier(sender, host, sleepCtx), nil
}
