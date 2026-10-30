package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newRunCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:   "run <ref> [-- args...]",
		Short: "Run the ref's binary once with its env file, user and data dir",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot("run"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			argv, err := a.RunArgs(args[0], service, args[1:])
			if err != nil {
				return err
			}
			return execArgv(argv)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	cmd.Flags().SetInterspersed(false) // flags after <ref> belong to the binary
	return cmd
}
