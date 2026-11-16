package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dmtrkzntsv/ship/internal/app"
)

// Seams for the tests: what the commands call once the arguments are checked.
var (
	runUpdate     = func(ctx context.Context, a *app.App, args app.UpdateArgs) error { return a.Update(ctx, args) }
	runAutoUpdate = func(ctx context.Context, a *app.App) error { return a.AutoUpdate(ctx) }
)

func newUpdateCmd() *cobra.Command {
	var service, version, cfZone string
	var purgeCache, auto, autoUpdate bool
	cmd := &cobra.Command{
		Use:               "update <ref> | update --auto",
		Short:             "Update an installed ref, restart it and roll back if it does not stay up",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeInstalledRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if auto {
				if len(args) != 0 || anyChanged(f, "service", "version", "cf-zone", "purge-cache", "auto-update") {
					return errors.New("--auto takes no ref and no other flags")
				}
			} else if len(args) != 1 {
				return fmt.Errorf("accepts 1 arg(s), received %d", len(args))
			}
			if err := hostGuard("update"); err != nil {
				return err
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if auto {
				return runAutoUpdate(cmd.Context(), a)
			}
			ua := app.UpdateArgs{Ref: args[0], Service: service, Version: version, CFZone: cfZone, PurgeCache: purgeCache}
			if f.Changed("auto-update") {
				ua.AutoUpdate = &autoUpdate
			}
			if err := runUpdate(cmd.Context(), a, ua); err != nil {
				return err
			}
			refreshAfter(cmd, a)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	f.StringVar(&version, "version", "", "channel to update to and remember: a version or a moving channel (latest, dev, edge, ...) (default: the remembered channel)")
	f.StringVar(&cfZone, "cf-zone", "", "set or replace the service's Cloudflare zone id (saved when the update succeeds)")
	f.BoolVar(&purgeCache, "purge-cache", false, "purge the service's Cloudflare cache after the version changed (needs SHIP_CF_API_TOKEN)")
	f.BoolVar(&autoUpdate, "auto-update", false, "turn automatic updates of this ref on or off (--auto-update=false); it needs a moving channel")
	f.BoolVar(&auto, "auto", false, "update every ref that has auto-update on (run by the ship-auto-update timer; takes no ref and no other flags)")
	registerServiceCompletion(cmd, false)
	registerVersionCompletion(cmd)
	return cmd
}

// anyChanged reports whether any of the named flags was set on the command line.
func anyChanged(f *pflag.FlagSet, names ...string) bool {
	for _, n := range names {
		if f.Changed(n) {
			return true
		}
	}
	return false
}
