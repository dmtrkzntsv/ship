package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newUpdateCmd() *cobra.Command {
	var service, version, cfZone string
	var purgeCache bool
	cmd := &cobra.Command{
		Use:   "update <ref>",
		Short: "Update an installed ref, restart it and roll back if it does not stay up",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot("update"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return a.Update(cmd.Context(), app.UpdateArgs{Ref: args[0], Service: service, Version: version, CFZone: cfZone, PurgeCache: purgeCache})
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	f.StringVar(&version, "version", "", "channel to update to and remember: a version, latest or dev (default: the remembered channel)")
	f.StringVar(&cfZone, "cf-zone", "", "set or replace the service's Cloudflare zone id (saved when the update succeeds)")
	f.BoolVar(&purgeCache, "purge-cache", false, "purge the service's Cloudflare cache after the version changed (needs SHIP_CF_API_TOKEN)")
	return cmd
}
