package cli

import (
	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
)

func newUninstallCmd() *cobra.Command {
	var service string
	var purge, purgeCache bool
	cmd := &cobra.Command{
		Use:   "uninstall <ref>",
		Short: "Remove an installed ref; the last one also removes the service's directories",
		Long: "Stops, disables and removes the ref's unit and binary. When it is the service's last\n" +
			"ref, /opt/S, /run/S and the tmpfiles entry go too. The env file, /etc/S, the data dir\n" +
			"and the service user are kept unless --purge is given, so a later install picks them\n" +
			"up again; a user ship did not create is always kept.",
		ValidArgsFunction: completeInstalledRefs,
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireLinuxRoot("uninstall"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return a.Uninstall(cmd.Context(), app.UninstallArgs{Ref: args[0], Service: service, Purge: purge, PurgeCache: purgeCache})
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	f.BoolVar(&purge, "purge", false, "also delete the ref's env file; with the last ref, /etc/S, the data dir and the service user (if ship created it)")
	f.BoolVar(&purgeCache, "purge-cache", false, "purge the service's Cloudflare cache after uninstalling (needs SHIP_CF_API_TOKEN)")
	registerServiceCompletion(cmd, false)
	return cmd
}
