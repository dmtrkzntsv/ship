package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newInstallCmd() *cobra.Command {
	var service, version, cfZone string
	cmd := &cobra.Command{
		Use:               "install <ref>",
		Short:             "Install a project ref as a systemd service (enabled, not started)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completePublishedRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot("install"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := a.Install(cmd.Context(), app.InstallArgs{Ref: args[0], Service: service, Version: version, CFZone: cfZone}); err != nil {
				return err
			}
			refreshAfter(cmd, a)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service to install into (default: the ref's project)")
	f.StringVar(&version, "version", "", "channel to install: a version, latest or dev")
	f.StringVar(&cfZone, "cf-zone", "", "Cloudflare zone id to purge for this service")
	_ = cmd.MarkFlagRequired("version")
	registerServiceCompletion(cmd, true)
	registerVersionCompletion(cmd)
	return cmd
}
