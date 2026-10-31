package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newRollbackCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:               "rollback <ref>",
		Short:             "Swap an installed ref back to its previous binary and unit",
		ValidArgsFunction: completeInstalledRefs,
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot("rollback"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return a.Rollback(cmd.Context(), args[0], service)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	registerServiceCompletion(cmd, false)
	return cmd
}
