package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/selfupdate"
)

func newSelfUpdateCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Replace this binary with the one published under ship/<channel> in R2",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "linux" {
				return fmt.Errorf("ship self-update: R2 only holds Linux builds; use: brew upgrade ship")
			}
			exe, err := executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			a, err := newApp(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if version != "" {
				if err := layout.ValidateChannel(version); err != nil {
					return err
				}
			}
			// On a host (root on Linux, where the auto-update timer may run)
			// the lock is held from before the binary is replaced through
			// the state save. Elsewhere only remembering a channel locks.
			locked := runningAsRoot()
			if locked {
				unlock, err := a.LockState(cmd.Context())
				if err != nil {
					return err
				}
				defer unlock()
			}
			channel := version
			if channel == "" {
				channel = "latest"
				st, err := a.State.Load()
				switch {
				case err != nil:
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; using %s\n", err, channel)
				case st.SelfChannel != "":
					channel = st.SelfChannel
				}
			}
			store, err := a.NewStore()
			if err != nil {
				return err
			}
			if _, err := selfupdate.Update(cmd.Context(), store, channel, exe, Version, cmd.OutOrStdout()); err != nil {
				return err
			}
			if version == "" {
				return nil
			}
			if !locked {
				unlock, err := a.LockState(cmd.Context())
				if err != nil {
					return fmt.Errorf("updated, but could not remember channel %q: %w", channel, err)
				}
				defer unlock()
			}
			st, err := a.State.Load()
			if err != nil {
				return err
			}
			st.SelfChannel = channel
			if err := a.State.Save(st); err != nil {
				return fmt.Errorf("updated, but could not remember channel %q: %w", channel, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "channel to update to and remember: a version or a moving channel (latest, dev, edge, ...) (default: the remembered channel, else latest)")
	return cmd
}

// runningAsRoot reports whether ship runs as root; tests replace it. The
// command is Linux only, so root means a host ship manages.
var runningAsRoot = func() bool { return os.Geteuid() == 0 }

// executable is the path of the running binary; tests replace it so
// self-update rewrites a temp file instead of the test binary.
var executable = os.Executable
