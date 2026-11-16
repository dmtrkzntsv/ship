package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailClockSequence(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	var c failClock
	steps := []struct {
		name    string
		at      time.Duration
		failing bool
		want    clockEvent
	}{
		{"first failure starts the clock", 0, true, clockNone},
		{"59m is within grace", 59 * time.Minute, true, clockNone},
		{"60m notifies", 60 * time.Minute, true, clockFailing},
		{"+23h is within reminder window", 60*time.Minute + 23*time.Hour, true, clockNone},
		{"+24h reminds", 60*time.Minute + 24*time.Hour, true, clockStillFailing},
		{"success after notice recovers", 61*time.Minute + 24*time.Hour, false, clockRecovered},
		{"success again is quiet", 62*time.Minute + 24*time.Hour, false, clockNone},
	}
	for _, s := range steps {
		assert.Equal(t, s.want, c.observe(t0.Add(s.at), s.failing), s.name)
	}
	assert.True(t, c.Since.IsZero())
	assert.True(t, c.Notified.IsZero())
}

func TestFailClockSuccessBeforeNoticeIsQuiet(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	var c failClock
	assert.Equal(t, clockNone, c.observe(t0, true))
	assert.Equal(t, clockNone, c.observe(t0.Add(time.Minute), false))
	assert.True(t, c.Since.IsZero())
}

func TestRunClockRoundTripMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "auto-update.json")
	in := runClock{
		FailingSince: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
		NotifiedAt:   time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC),
		Reason:       "boom",
	}
	require.NoError(t, in.save(path))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	out, err := loadRunClock(path)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestRunClockMissingIsZero(t *testing.T) {
	c, err := loadRunClock(filepath.Join(t.TempDir(), "none.json"))
	require.NoError(t, err)
	assert.Equal(t, runClock{}, c)
}

func TestRunClockCorruptIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	require.NoError(t, os.WriteFile(path, []byte("{nope"), 0o600))
	_, err := loadRunClock(path)
	assert.Error(t, err)
}

func TestRunClockLockSerializesChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto-update.json")
	a := &App{RunClockPath: path, Err: io.Discard}
	unlock, err := lockRunClock(path)
	require.NoError(t, err)
	info, err := os.Stat(path + ".lock")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	time.AfterFunc(100*time.Millisecond, unlock)
	start := time.Now()
	written, err := a.setPending(pendingRecord{Service: "s", Ref: "r", Version: "1"})
	require.NoError(t, err)
	assert.True(t, written)
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "waited for the holder")
	assert.Equal(t, "1", a.pending().Version)
}

func TestRunClockLockTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto-update.json")
	unlock, err := lockRunClock(path)
	require.NoError(t, err)
	defer unlock()
	old := runClockLockWait
	runClockLockWait = 50 * time.Millisecond
	t.Cleanup(func() { runClockLockWait = old })
	_, err = lockRunClock(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run clock")

	a := &App{RunClockPath: path, Err: io.Discard}
	_, err = a.setPending(pendingRecord{Service: "s"})
	require.Error(t, err)
}
