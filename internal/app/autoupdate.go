package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/dmtrkzntsv/ship/internal/config"
	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/notify"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
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

// enableAutoUpdate installs and starts the auto-update timer, warning about
// SHIP_* settings in this shell that the timer's runs will not have.
func (a *App) enableAutoUpdate(ctx context.Context) error {
	path, err := a.checkAutoUpdate()
	if err != nil {
		return err
	}
	for _, name := range a.shellSettings() {
		fmt.Fprintf(a.Err, "warning: %s is set in this shell; the auto-update timer reads only %s\n", name, config.SystemPath)
	}
	return a.Host.EnsureAutoUpdateTimer(ctx, path)
}

// shellSettings names the non-empty SHIP_* variables in the environment, sorted.
func (a *App) shellSettings() []string {
	if a.Environ == nil {
		return nil
	}
	var names []string
	for _, kv := range a.Environ() {
		name, val, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SHIP_") && val != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// lockHeldReason is the run clock's reason while other ship runs hold the lock.
const lockHeldReason = "another ship run has held the state lock"

// AutoUpdate is ship update --auto: it updates every running ref with
// auto-update on through the normal update path and notifies about what
// happened. A ref whose unit is not running is skipped. A failed update stops
// auto-update on its ref; a ref whose release cannot be fetched keeps it and
// is announced only after an hour. One ref failing does not stop the others;
// the returned error counts the failed refs and unsent notifications. A held
// state lock is not an error: the next run retries. With no ref opted in the
// run does nothing at all, not even open the store.
func (a *App) AutoUpdate(ctx context.Context) error {
	n := Notifier(noopNotifier{})
	if a.Notifier != nil {
		var err error
		if n, err = a.Notifier(); err != nil {
			return err
		}
	}
	r := &autoRun{a: a, ctx: ctx, n: n, now: a.Now().UTC()}

	unlock, err := a.State.Lock()
	if errors.Is(err, state.ErrLocked) {
		// Exits 0 whatever happens: the clock and notify errors are on stderr.
		r.observeRun(lockHeldReason)
		fmt.Fprintln(a.Out, "another ship run holds the state lock; trying again next run")
		return nil
	}
	if err != nil {
		r.observeRun(err.Error())
		return err
	}
	defer unlock()
	s, err := a.State.Load()
	if err != nil {
		r.observeRun(err.Error())
		return err
	}
	targets := autoTargets(s)
	if len(targets) == 0 {
		return nil
	}
	st, err := a.NewStore()
	if err != nil {
		r.observeRun(err.Error())
		return err
	}
	r.observeRun("")

	for _, t := range targets {
		// A stopped unit (never started after install, or ship stop) is left
		// alone: updating would start it.
		if !a.Host.Sys.IsActive(ctx, t.rs.Unit) {
			if err := ctx.Err(); err != nil {
				return errors.Join(err, r.err())
			}
			fmt.Fprintf(a.Out, "%s: not running, skipped\n", t.rs.Unit)
			continue
		}
		if err := r.update(s, st, t); err != nil {
			return errors.Join(err, r.err())
		}
	}
	return r.err()
}

// autoTarget is one ref with auto-update on.
type autoTarget struct {
	service, key string
	rs           *state.RefState
}

// autoTargets lists the refs with auto-update on, by unit.
func autoTargets(s *state.State) []autoTarget {
	var ts []autoTarget
	for service, svc := range s.Services {
		for key, rs := range svc.Refs {
			if rs.AutoUpdate {
				ts = append(ts, autoTarget{service, key, rs})
			}
		}
	}
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].rs.Unit != ts[j].rs.Unit {
			return ts[i].rs.Unit < ts[j].rs.Unit
		}
		return ts[i].service < ts[j].service
	})
	return ts
}

// autoRun is the bookkeeping of one AutoUpdate run.
type autoRun struct {
	a   *App
	ctx context.Context
	n   Notifier
	now time.Time

	refs, failed, unsent int
	clockUnsaved         bool
}

// update runs one ref's update, classifies the result, saves the state and
// sends what it calls for. An interrupt is returned: it ends the run. So is
// a state that cannot be saved after the host was changed: carrying on
// would act on a state that no longer matches the host.
func (r *autoRun) update(s *state.State, st store.Store, t autoTarget) error {
	a, rs := r.a, t.rs
	r.refs++
	before, old := *rs, rs.Version
	var (
		rel     release.Release
		outcome updateOutcome
	)
	ref, err := layout.ParseRef(t.key)
	if err == nil {
		rel, outcome, err = a.updateRef(r.ctx, st, ref, t.service, rs, rs.Channel, nil)
	}
	// A command killed by the interrupt fails with its own error (signal:
	// killed), not context.Canceled.
	interrupted := err != nil && (errors.Is(err, context.Canceled) || r.ctx.Err() != nil)
	what := fmt.Sprintf("%s (%s)", rs.Unit, rs.Channel)
	var (
		msgs    []notify.Message
		updated *notify.Message
	)
	failed, purge := false, false
	stopped := "" // a failed update's title, without the auto-update note
	switch {
	case err == nil:
		msgs = r.checkClock(rs, what, nil)
		if outcome == swapped {
			fmt.Fprintf(a.Out, "%s: %s -> %s%s\n", rs.Unit, old, rs.Version, republished(old, rs))
			updated = &notify.Message{Title: fmt.Sprintf("%s %s \u2192 %s%s", what, old, rs.Version, republished(old, rs))}
		}
		purge = outcome != upToDate
	case errors.Is(err, host.ErrRevertFailed):
		// Checked before an interrupt: the service is down whatever cut the
		// run short, and this is the one message that must not be lost.
		failed = true
		stopAutoUpdate(rs, r.now)
		stopped = fmt.Sprintf("%s is DOWN \u2014 update to %s failed and rollback to %s failed", rs.Unit, rel.Version, old)
	case interrupted:
		// The host restored the previous pair: auto-update stays on, quietly.
		fmt.Fprintf(a.Err, "%s: %v\n", rs.Unit, err)
		return err
	case failedUpdate(err):
		failed = true
		stopAutoUpdate(rs, r.now)
		build := "build"
		if rel.Version != "" {
			build += " " + rel.Version
		}
		stopped = fmt.Sprintf("%s %s failed, still on %s", what, build, old)
	default: // nothing was installed: the release could not be checked
		failed = true
		msgs = r.checkClock(rs, what, err)
	}
	if err != nil {
		fmt.Fprintf(a.Err, "%s: %v\n", rs.Unit, err)
	}

	var saveErr error
	if *rs != before {
		if saveErr = a.State.Save(s); saveErr != nil {
			fmt.Fprintf(a.Err, "%s: %v\n", rs.Unit, saveErr)
			failed = true
		}
	}
	if saveErr != nil && err == nil && outcome != upToDate {
		// The host runs the new build but state still names the old one.
		// The next run finds it live and only records it; until then the
		// halt message replaces Updated (and the clock messages, which the
		// unsaved state repeats next run).
		r.failed++
		r.notify(notify.Message{
			Title: fmt.Sprintf("%s updated to %s but the state could not be saved; automatic updates halted", what, rs.Version),
			Body:  saveErr.Error(),
		})
		return fmt.Errorf("%s: updated to %s but the state could not be saved: %w", rs.Unit, rs.Version, saveErr)
	}
	if updated != nil {
		msgs = append(msgs, *updated)
	}
	if stopped != "" {
		m := notify.Message{Title: stopped + "; auto-update stopped", Body: err.Error()}
		if saveErr != nil {
			m = notify.Message{
				Title: stopped,
				Body:  fmt.Sprintf("auto-update could not be stopped: saving the state failed: %v\n\n%v", saveErr, err),
			}
		}
		msgs = append(msgs, m)
	}
	for _, m := range msgs {
		r.notify(m)
	}
	if purge && !r.purge(s.Services[t.service], rs.Unit) {
		failed = true
	}
	if interrupted {
		return err // reached only after a Down: the run still ends here
	}
	if failed {
		r.failed++
	}
	return nil
}

// failedUpdate reports whether err is a failed update rather than a failed
// check: the build is broken, or installing it failed (the host may have
// been touched).
func failedUpdate(err error) bool {
	var he hostError
	return errors.Is(err, release.ErrBadBuild) || errors.Is(err, release.ErrNoUnit) ||
		errors.Is(err, host.ErrBadBuild) || errors.Is(err, host.ErrRolledBack) ||
		errors.As(err, &he)
}

// checkClock feeds the ref's "can't check" clock (checkErr nil: the check
// worked) and returns the message it calls for, if any.
func (r *autoRun) checkClock(rs *state.RefState, what string, checkErr error) []notify.Message {
	c := failClock{Since: rs.CheckFailingSince, Notified: rs.CheckNotifiedAt}
	since := c.Since
	ev := c.observe(r.now, checkErr != nil)
	rs.CheckFailingSince, rs.CheckNotifiedAt = c.Since, c.Notified
	switch ev {
	case clockFailing:
		return []notify.Message{{Title: fmt.Sprintf("can't check %s for updates since %s", what, clockTime(c.Since)), Body: checkErr.Error()}}
	case clockStillFailing:
		return []notify.Message{{Title: fmt.Sprintf("still can't check %s for updates since %s (%s)", what, clockTime(c.Since), minutes(r.now.Sub(c.Since))), Body: checkErr.Error()}}
	case clockRecovered:
		return []notify.Message{{Title: fmt.Sprintf("%s reachable again, was failing since %s (%s)", what, clockTime(since), minutes(r.now.Sub(since)))}}
	}
	return nil
}

// observeRun feeds the run clock (reason "": the run got going) and sends
// what it calls for. A clock file that cannot be read starts over, so it
// never wedges the run.
func (r *autoRun) observeRun(reason string) {
	path := r.a.RunClockPath
	prev, err := loadRunClock(path)
	broken := err != nil
	if broken {
		fmt.Fprintf(r.a.Err, "warning: %v; starting it over\n", err)
		prev = runClock{}
	}
	c := failClock{Since: prev.FailingSince, Notified: prev.NotifiedAt}
	since := c.Since
	ev := c.observe(r.now, reason != "")
	next := runClock{FailingSince: c.Since, NotifiedAt: c.Notified, Reason: reason}
	if next != prev || broken {
		if err := next.save(path); err != nil {
			fmt.Fprintf(r.a.Err, "warning: %v\n", err)
			r.clockUnsaved = true
		}
	}
	switch ev {
	case clockFailing:
		r.notify(notify.Message{Title: "automatic updates can't run since " + clockTime(c.Since), Body: reason})
	case clockStillFailing:
		r.notify(notify.Message{Title: fmt.Sprintf("automatic updates still can't run since %s (%s)", clockTime(c.Since), minutes(r.now.Sub(c.Since))), Body: reason})
	case clockRecovered:
		r.notify(notify.Message{Title: fmt.Sprintf("automatic updates running again, were failing since %s (%s)", clockTime(since), minutes(r.now.Sub(since)))})
	}
}

// purge purges the service's Cloudflare cache after an automatic update when
// it has a zone and a token is configured; it reports false on a failed purge.
func (r *autoRun) purge(svc *state.Service, unit string) bool {
	if svc.CFZone == "" {
		return true
	}
	if r.a.checkToken() != nil {
		fmt.Fprintf(r.a.Err, "%s: cache not purged: no Cloudflare token\n", unit)
		return true
	}
	if err := r.a.purgeCache(r.ctx, svc.CFZone); err != nil {
		fmt.Fprintf(r.a.Err, "%s: cache purge failed: %v\n", unit, err)
		return false
	}
	return true
}

// notifyTimeout bounds a send made after the run was interrupted.
const notifyTimeout = 30 * time.Second

// notify sends m; a send that fails is logged and counted, never retried
// on a later run. An interrupted run still sends, within notifyTimeout.
func (r *autoRun) notify(m notify.Message) {
	ctx := r.ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
		defer cancel()
	}
	if err := r.n.Notify(ctx, m); err != nil {
		fmt.Fprintf(r.a.Err, "notification %q not sent: %v\n", m.Title, err)
		r.unsent++
	}
}

// err summarizes what went wrong in the run, or returns nil.
func (r *autoRun) err() error {
	var parts []string
	if r.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d refs failed", r.failed, r.refs))
	}
	switch {
	case r.unsent == 1:
		parts = append(parts, "1 notification not sent")
	case r.unsent > 1:
		parts = append(parts, fmt.Sprintf("%d notifications not sent", r.unsent))
	}
	if r.clockUnsaved {
		parts = append(parts, "run clock not saved")
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New("automatic update: " + strings.Join(parts, "; "))
}

// clockTime is how messages show a time: hours and minutes on the host's clock.
func clockTime(t time.Time) string { return t.Local().Format("15:04") }

// minutes formats d rounded to minutes: "45m", "3h12m", "84h".
func minutes(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "0m"
	}
	s := strings.TrimSuffix(d.String(), "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
