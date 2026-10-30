// Package app wires config, store, host state and the host layer into the
// per-command orchestration used by the CLI.
package app

import (
	"fmt"
	"io"
	"net/http"
	"os"
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
		Arch: release.HostArch(),
		Now:  time.Now,
		CF: func(token string) *cloudflare.Client {
			return &cloudflare.Client{Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
		},
	}
	a.NewStore = func() (store.Store, error) {
		if err := a.Cfg.R2.Validate(); err != nil {
			return nil, err
		}
		return store.NewS3(a.Cfg.R2.S3Options())
	}
	return a, nil
}
