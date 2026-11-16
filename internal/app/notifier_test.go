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

type fakeSender struct {
	errs []error
	got  []notify.Message
}

func (f *fakeSender) Send(_ context.Context, m notify.Message) error {
	f.got = append(f.got, m)
	if len(f.got) <= len(f.errs) {
		return f.errs[len(f.got)-1]
	}
	return nil
}

func (f *fakeSender) Service() string { return "fake" }

func TestNotifierPrefixesHost(t *testing.T) {
	s := &fakeSender{}
	n := newNotifier(s, "box", func(time.Duration) {})
	require.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t", Body: "b"}))
	require.NoError(t, n.Notify(context.Background(), notify.Message{Body: "only body"}))
	require.Len(t, s.got, 2)
	assert.Equal(t, notify.Message{Title: "box: t", Body: "b"}, s.got[0])
	assert.Equal(t, notify.Message{Body: "box: only body"}, s.got[1])
}

func TestNotifierRetriesTemporary(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrTemporary, notify.ErrTemporary}}
	var sleeps []time.Duration
	n := newNotifier(s, "h", func(d time.Duration) { sleeps = append(sleeps, d) })
	require.NoError(t, n.Notify(context.Background(), notify.Message{Title: "t"}))
	assert.Len(t, s.got, 3)
	assert.Equal(t, []time.Duration{5 * time.Second, 15 * time.Second}, sleeps)
}

func TestNotifierGivesUpAfterThreeAttempts(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrTemporary, notify.ErrTemporary, notify.ErrTemporary, notify.ErrTemporary}}
	var sleeps []time.Duration
	n := newNotifier(s, "h", func(d time.Duration) { sleeps = append(sleeps, d) })
	err := n.Notify(context.Background(), notify.Message{Title: "t"})
	require.ErrorIs(t, err, notify.ErrTemporary)
	assert.Len(t, s.got, 3)
	assert.Len(t, sleeps, 2)
}

func TestNotifierStopsOnPermanent(t *testing.T) {
	s := &fakeSender{errs: []error{notify.ErrPermanent}}
	n := newNotifier(s, "h", func(time.Duration) { t.Fatal("must not sleep") })
	err := n.Notify(context.Background(), notify.Message{Title: "t"})
	require.True(t, errors.Is(err, notify.ErrPermanent))
	assert.Len(t, s.got, 1)
}

func TestNotifierNilSenderNoop(t *testing.T) {
	n := newNotifier(nil, "h", func(time.Duration) {})
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
