package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/notify"
)

func noWait(context.Context, time.Duration) error { return nil }

type fakeSender struct {
	errs      []error
	got       []notify.Message
	deadlines []time.Time
}

func (f *fakeSender) Send(ctx context.Context, m notify.Message) error {
	f.got = append(f.got, m)
	d, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, d)
	if len(f.got) <= len(f.errs) {
		return f.errs[len(f.got)-1]
	}
	return nil
}

func (f *fakeSender) Service() string { return "fake" }

func TestNotifierPrefixesHost(t *testing.T) {
	s := &fakeSender{}
	n := newNotifier(s, "box", noWait)
	require.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t", Body: "b"}))
	require.NoError(t, n.Notify(context.Background(), notify.Message{Body: "only body"}))
	require.Len(t, s.got, 2)
	assert.Equal(t, notify.Message{Title: "box: t", Body: "b"}, s.got[0])
	assert.Equal(t, notify.Message{Body: "box: only body"}, s.got[1])
}

func TestNotifierRetriesTemporary(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrTemporary, notify.ErrTemporary}}
	var sleeps []time.Duration
	n := newNotifier(s, "h", func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil })
	require.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t"}))
	assert.Len(t, s.got, 3)
	assert.Equal(t, []time.Duration{5 * time.Second, 15 * time.Second}, sleeps)
}

func TestNotifierGivesUpAfterThreeAttempts(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrTemporary, notify.ErrTemporary, notify.ErrTemporary, notify.ErrTemporary}}
	var sleeps []time.Duration
	n := newNotifier(s, "h", func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil })
	err := n.Notify(context.Background(), notify.Message{Title: "t"})
	require.ErrorIs(t, err, notify.ErrTemporary)
	assert.Len(t, s.got, 3)
	assert.Len(t, sleeps, 2)
}

func TestNotifierStopsOnPermanent(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrPermanent}}
	n := newNotifier(s, "h", func(context.Context, time.Duration) error { t.Fatal("must not sleep"); return nil })
	err := n.Notify(context.Background(), notify.Message{Title: "t"})
	require.True(t, errors.Is(err, notify.ErrPermanent))
	assert.Len(t, s.got, 1)
}

func TestNotifierNilSenderNoop(t *testing.T) {
	n := newNotifier(nil, "h", noWait)
	require.NotNil(t, n)
	assert.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t"}))
}

func TestConfigNotifierEmptyURLNoop(t *testing.T) {
	n, err := configNotifier("")
	require.NoError(t, err)
	assert.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t"}))
}

func TestConfigNotifierInvalidURL(t *testing.T) {
	_, err := configNotifier("bogus://x")
	require.ErrorIs(t, err, notify.ErrInvalidURL)
}

func TestNotifierOneDeadlineForAllAttempts(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrTemporary, notify.ErrTemporary}}
	n := newNotifier(s, "h", noWait)
	start := time.Now()
	require.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t"}))
	require.Len(t, s.deadlines, 3)
	assert.WithinDuration(t, start.Add(30*time.Second), s.deadlines[0], time.Second)
	assert.Equal(t, s.deadlines[0], s.deadlines[2], "every attempt shares one deadline")
}

func TestNotifierStopsWaitingAtDeadline(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrTemporary, notify.ErrTemporary, notify.ErrTemporary}}
	n := newNotifier(s, "h", sleepCtx).(*notifier)
	n.timeout = 50 * time.Millisecond
	start := time.Now()
	err := n.Notify(context.Background(), notify.Message{Title: "t"})
	require.ErrorIs(t, err, notify.ErrTemporary)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Len(t, s.got, 1, "no attempt after the deadline")
	assert.Less(t, time.Since(start), 2*time.Second, "the 5s wait is cut short")
}

func TestSleepCtx(t *testing.T) {
	require.NoError(t, sleepCtx(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepCtx(ctx, time.Hour), context.Canceled)
}
