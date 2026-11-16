package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

// runInstall is a seam for the tests.
var runInstall = func(ctx context.Context, a *app.App, args app.InstallArgs) error { return a.Install(ctx, args) }

func newInstallCmd() *cobra.Command {
	var service, version, cfZone string
	var autoUpdate bool
	cmd := &cobra.Command{
		Use:               "install <ref>",
		Short:             "Install a project ref as a systemd service (enabled, not started)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completePublishedRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := hostGuard("install"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := runInstall(cmd.Context(), a, app.InstallArgs{Ref: args[0], Service: service, Version: version, CFZone: cfZone, AutoUpdate: autoUpdate}); err != nil {
				return err
			}
			refreshAfter(cmd, a)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service to install into (default: the ref's project)")
	f.StringVar(&version, "version", "", "channel to install: a version or a moving channel (latest, dev, edge, ...)")
	f.StringVar(&cfZone, "cf-zone", "", "Cloudflare zone id to purge for this service")
	f.BoolVar(&autoUpdate, "auto-update", false, "update this ref automatically when its channel moves (needs a moving channel)")
	_ = cmd.MarkFlagRequired("version")
	registerServiceCompletion(cmd, true)
	registerVersionCompletion(cmd)
	return cmd
}
