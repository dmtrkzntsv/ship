package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// runClockPath is where ship update --auto keeps the run-level failure clock.
const runClockPath = "/var/lib/ship/auto-update.json"

const (
	failGrace      = time.Hour
	failReminderIn = 24 * time.Hour
)

// runClock tracks failures of the auto-update run itself. Reason may hold
// error text, which is why the file is 0600 and never part of state.json.
// Pending is an update whose host swap worked but whose state save failed.
type runClock struct {
	FailingSince time.Time     `json:"failing_since,omitzero"`
	NotifiedAt   time.Time     `json:"notified_at,omitzero"`
	Reason       string        `json:"reason,omitempty"`
	Pending      pendingRecord `json:"pending,omitzero"`
}

// pendingRecord is what an update that swapped a ref's build on the host
// but could not save the state would have recorded. Only a matching record
// lets a later update record a live build without installing it again.
type pendingRecord struct {
	Service         string    `json:"service"`
	Ref             string    `json:"ref"`
	Version         string    `json:"version"`
	PublishedAt     time.Time `json:"published_at,omitzero"`
	PrevVersion     string    `json:"prev_version,omitempty"`
	PrevPublishedAt time.Time `json:"prev_published_at,omitzero"`
}

// pendingFor is the record of rs's just-swapped build.
func pendingFor(service, refKey string, rs *state.RefState) pendingRecord {
	return pendingRecord{
		Service: service, Ref: refKey,
		Version: rs.Version, PublishedAt: rs.PublishedAt,
		PrevVersion: rs.PrevVersion, PrevPublishedAt: rs.PrevPublishedAt,
	}
}

// matches reports whether p is the pending swap of rel for refKey in service.
func (p pendingRecord) matches(service, refKey string, rel release.Release) bool {
	return p.Service == service && p.Ref == refKey &&
		p.Version == rel.Version && p.PublishedAt.Equal(rel.PublishedAt)
}

// pending returns the pending record in the run clock file; a missing or
// unreadable file holds none.
func (a *App) pending() pendingRecord {
	if a.RunClockPath == "" {
		return pendingRecord{}
	}
	c, err := loadRunClock(a.RunClockPath)
	if err != nil {
		return pendingRecord{}
	}
	return c.Pending
}

// setPending stores p in the run clock file; written reports whether the
// file did not already hold it. An unreadable file starts over.
func (a *App) setPending(p pendingRecord) (written bool, err error) {
	if a.RunClockPath == "" {
		return false, errors.New("no run clock file to remember the update in")
	}
	c, err := loadRunClock(a.RunClockPath)
	if err != nil {
		c = runClock{}
	}
	if c.Pending == p {
		return false, nil
	}
	c.Pending = p
	if err := c.save(a.RunClockPath); err != nil {
		return false, err
	}
	return true, nil
}

// clearPending drops the pending record of refKey in service, if there is
// one; a failure is only a warning (the record no longer matches anything
// once the state names that build).
func (a *App) clearPending(service, refKey string) {
	if a.RunClockPath == "" {
		return
	}
	c, err := loadRunClock(a.RunClockPath)
	if err != nil || c.Pending.Service != service || c.Pending.Ref != refKey {
		return
	}
	c.Pending = pendingRecord{}
	if err := c.save(a.RunClockPath); err != nil {
		fmt.Fprintf(a.Err, "warning: %v\n", err)
	}
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
