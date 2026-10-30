package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newLifecycleCmd(action, short string) *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:   action + " <ref>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot(action); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return a.Lifecycle(cmd.Context(), action, args[0], service)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	return cmd
}
