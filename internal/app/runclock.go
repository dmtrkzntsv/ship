package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// runClockPath is where ship update --auto keeps the run-level failure clock.
const runClockPath = "/var/lib/ship/auto-update.json"

const (
	failGrace      = time.Hour
	failReminderIn = 24 * time.Hour
)

// runClock tracks failures of the auto-update run itself. Reason may hold
// error text, which is why the file is 0600 and never part of state.json.
type runClock struct {
	FailingSince time.Time `json:"failing_since,omitzero"`
	NotifiedAt   time.Time `json:"notified_at,omitzero"`
	Reason       string    `json:"reason,omitempty"`
}

// loadRunClock reads the clock file; a missing file is the zero clock.
func loadRunClock(path string) (runClock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return runClock{}, nil
	}
	if err != nil {
		return runClock{}, fmt.Errorf("read run clock %s: %w", path, err)
	}
	var c runClock
	if err := json.Unmarshal(data, &c); err != nil {
		return runClock{}, fmt.Errorf("parse run clock %s: %w", path, err)
	}
	return c, nil
}

// save writes the clock atomically with mode 0600.
func (c runClock) save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create run clock dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run clock: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".auto-update-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp run clock: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod run clock %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write run clock %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync run clock %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close run clock %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace run clock %s: %w", path, err)
	}
	return nil
}

// clockEvent is what a failClock observation asks the caller to do.
type clockEvent int

const (
	clockNone         clockEvent = iota
	clockFailing                 // failing past the grace period: send the first notice
	clockStillFailing            // still failing a day after the last notice: remind
	clockRecovered               // recovered after a notice was sent: say so
)

// failClock turns a series of pass/fail observations into notification
// events. Since is when the current failure streak began, Notified when the
// last failure notice went out.
type failClock struct{ Since, Notified time.Time }

func (c *failClock) observe(now time.Time, failing bool) clockEvent {
	if !failing {
		ev := clockNone
		if !c.Notified.IsZero() {
			ev = clockRecovered
		}
		*c = failClock{}
		return ev
	}
	if c.Since.IsZero() {
		c.Since = now
		return clockNone
	}
	if c.Notified.IsZero() {
		if now.Sub(c.Since) >= failGrace {
			c.Notified = now
			return clockFailing
		}
		return clockNone
	}
	if now.Sub(c.Notified) >= failReminderIn {
		c.Notified = now
		return clockStillFailing
	}
	return clockNone
}
