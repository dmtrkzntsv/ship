package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/config"
)

// isRoot and getenv are variables so tests can pretend.
var (
	isRoot = func() bool { return os.Geteuid() == 0 }
	getenv = os.Getenv
)

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Automatic update notifications",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test message through the configured notification url",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if getenv("SHIP_NOTIFY_URL") != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: SHIP_NOTIFY_URL is set in this shell and was tested instead of %s; the auto-update timer reads only that file\n", config.SystemPath)
			}
			err = a.NotifyTest(cmd.Context())
			if !isRoot() {
				// The host config is root-only and silently skipped otherwise;
				// the timer runs as root and reads only that file.
				if errors.Is(err, app.ErrNoNotifyURL) {
					return fmt.Errorf("%w; %s is readable only by root, try: sudo ship notify test", err, config.SystemPath)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "note: not root, so this tested the settings readable as you; the auto-update timer runs as root and reads only %s (try: sudo ship notify test)\n", config.SystemPath)
			}
			return err
		},
	})
	return cmd
}
