package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newAdoptCmd() *cobra.Command {
	var service, version, cfZone string
	cmd := &cobra.Command{
		Use:               "adopt <project>",
		Short:             "Take over a service installed by the old deploy script, rolling back if it does not stay up",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProjects,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot("adopt"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return a.Adopt(cmd.Context(), app.AdoptArgs{Ref: args[0], Service: service, Version: version, CFZone: cfZone})
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service the old script installed (default: the project)")
	f.StringVar(&version, "version", "", "channel to install and remember: a version, latest or dev")
	f.StringVar(&cfZone, "cf-zone", "", "Cloudflare zone id to purge for this service")
	_ = cmd.MarkFlagRequired("version")
	registerServiceCompletion(cmd, true)
	registerVersionCompletion(cmd)
	return cmd
}
