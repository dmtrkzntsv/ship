package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/config"
)

// isRoot reports whether ship runs as root; a variable so tests can pretend.
var isRoot = func() bool { return os.Geteuid() == 0 }

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
			err = a.NotifyTest(cmd.Context())
			if !isRoot() {
				// The host config is root-only and silently skipped otherwise;
				// the timer runs as root and reads only that file.
				if errors.Is(err, app.ErrNoNotifyURL) {
					return fmt.Errorf("%w; %s is readable only by root, try: sudo ship notify test", err, config.SystemPath)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "note: not root, so this tested your own settings; the auto-update timer reads only %s (try: sudo ship notify test)\n", config.SystemPath)
			}
			return err
		},
	})
	return cmd
}
