// Package app wires config, store, host state and the host layer into the
// per-command orchestration used by the CLI.
package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dmtrkzntsv/ship/internal/cloudflare"
	"github.com/dmtrkzntsv/ship/internal/config"
	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// App holds the loaded configuration, the host it acts on and the writers
// commands report to.
type App struct {
	Cfg      config.Config
	Out, Err io.Writer
	// NewStore opens the object store; it validates the R2 config when called.
	NewStore func() (store.Store, error)
	State    state.File
	Host     *host.Host
	// CF builds the Cloudflare client for an API token.
	CF   func(token string) *cloudflare.Client
	Arch string // linux arch of the binaries to install
	Now  func() time.Time
	// ShipPath returns the absolute path of the running ship binary, which
	// the auto-update service unit executes.
	ShipPath func() (string, error)
	// Notifier builds the notifier for ship update --auto; with no notify
	// URL configured it is a no-op.
	Notifier func() (Notifier, error)
	// RunClockPath is the file holding the auto-update run failure clock.
	RunClockPath string
	// Environ lists the environment ("KEY=value"); turning auto-update on
	// warns about SHIP_* settings the timer will not see. Nil: none.
	Environ func() []string
	// LockWait is how long interactive commands wait for another ship run
	// (the auto-update timer, say) to release the state lock; zero fails at once.
	LockWait time.Duration
}

// interactiveLockWait is how long operator commands wait for the state lock.
const interactiveLockWait = 2 * time.Minute

// LockState takes the state lock for an interactive command, waiting up to
// LockWait for another ship run to finish (saying so once on Err).
// ship update --auto never waits: it uses State.Lock.
// The returned unlock may be called more than once, so a command can release
// the lock early (before a notification) and still defer it.
func (a *App) LockState(ctx context.Context) (unlock func(), err error) {
	unlock, err = a.State.LockWait(ctx, a.LockWait, func() {
		fmt.Fprintln(a.Err, "waiting for another ship run to finish\u2026")
	})
	if err != nil {
		return nil, err
	}
	return sync.OnceFunc(unlock), nil
}

// locateShip is the ship path the auto-update unit runs: the "ship" found on
// PATH when it is the running binary (a stable path such as
// /usr/local/bin/ship, even when it links to a versioned install), else the
// running executable as the kernel reports it. Symlinks are not resolved, so
// the unit does not pin one version's directory.
func locateShip(lookPath func(string) (string, error), executable func() (string, error)) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	p, err := lookPath("ship")
	if err != nil {
		return exe, nil
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	pi, perr := os.Stat(p)
	ei, eerr := os.Stat(exe)
	if perr != nil || eerr != nil || !os.SameFile(pi, ei) {
		return exe, nil
	}
	return p, nil
}

// New loads the configuration (a missing file is fine; env-only is valid),
// prints config warnings to errOut and returns an App acting on this host.
func New(out, errOut io.Writer) (*App, error) {
	home, _ := os.UserHomeDir()
	cfg, _, warnings, err := config.Load(os.Getenv, config.DefaultPaths(home, os.Geteuid() == 0))
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintln(errOut, "warning:", w)
	}
	a := &App{
		Cfg: cfg, Out: out, Err: errOut,
		State: state.File{Path: state.DefaultPath},
		Host: &host.Host{
			Paths: host.Paths{}, Sys: host.NewSys(), Out: out,
			HealthWait: 10 * time.Second, HealthTick: time.Second,
		},
		Arch:         release.HostArch(),
		Now:          time.Now,
		RunClockPath: runClockPath,
		LockWait:     interactiveLockWait,
		Environ:      os.Environ,
		ShipPath:     func() (string, error) { return locateShip(exec.LookPath, os.Executable) },
		CF: func(token string) *cloudflare.Client {
			return &cloudflare.Client{Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
		},
	}
	a.Notifier = func() (Notifier, error) { return configNotifier(a.Cfg.Notify.URL) }
	a.NewStore = func() (store.Store, error) {
		if err := a.Cfg.R2.Validate(); err != nil {
			return nil, err
		}
		return store.NewS3(a.Cfg.R2.S3Options())
	}
	return a, nil
}
