// Package app wires config, store and (in later tasks) host state into the
// per-command orchestration used by the CLI.
package app

import (
	"fmt"
	"io"
	"os"

	"github.com/dmtrkzntsv/ship/internal/config"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// App holds the loaded configuration and the writers commands report to.
type App struct {
	Cfg      config.Config
	Out, Err io.Writer
	// NewStore opens the object store; it validates the R2 config when called.
	NewStore func() (store.Store, error)
}

// New loads the configuration (a missing file is fine; env-only is valid),
// prints config warnings to errOut and returns an App.
func New(out, errOut io.Writer) (*App, error) {
	home, _ := os.UserHomeDir()
	cfg, _, warnings, err := config.Load(os.Getenv, config.DefaultPaths(home, os.Geteuid() == 0))
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintln(errOut, "warning:", w)
	}
	a := &App{Cfg: cfg, Out: out, Err: errOut}
	a.NewStore = func() (store.Store, error) {
		if err := a.Cfg.R2.Validate(); err != nil {
			return nil, err
		}
		return store.NewS3(a.Cfg.R2.S3Options())
	}
	return a, nil
}
