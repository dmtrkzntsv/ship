package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newStatusCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show installed services, refs, versions and unit state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return a.Status(cmd.Context(), service)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "show only this service")
	return cmd
}
