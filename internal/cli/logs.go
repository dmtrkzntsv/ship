package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newLogsCmd() *cobra.Command {
	var service string
	var noFollow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs <ref>",
		Short: "Show and follow the journal of an installed ref",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinux("logs"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			argv, err := a.LogsArgs(args[0], service, lines, !noFollow)
			if err != nil {
				return err
			}
			return execArgv(argv)
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	f.BoolVar(&noFollow, "no-follow", false, "print the last lines and exit")
	f.IntVarP(&lines, "lines", "n", 100, "number of lines to show")
	return cmd
}
