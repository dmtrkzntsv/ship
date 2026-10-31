package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/dmtrkzntsv/ship/internal/state"
)

// checkPurge validates --purge-cache against the saved state, without the
// lock, so a missing zone or token fails before any work starts. A service
// not in state is left to the caller's "not installed" error.
func (a *App) checkPurge(service, zoneArg string) error {
	s, err := a.State.Load()
	if err != nil {
		return err
	}
	if s.Services[service] == nil {
		if zoneArg == "" {
			return nil
		}
		return a.checkToken()
	}
	return a.checkPurgeIn(s, service, zoneArg)
}

// checkPurgeIn requires an effective zone (zoneArg, else the service's
// stored one) and an API token.
func (a *App) checkPurgeIn(s *state.State, service, zoneArg string) error {
	zone := zoneArg
	if zone == "" {
		if svc := s.Services[service]; svc != nil {
			zone = svc.CFZone
		}
	}
	if zone == "" {
		return fmt.Errorf("--purge-cache: service %s has no Cloudflare zone; pass --cf-zone", service)
	}
	return a.checkToken()
}

func (a *App) checkToken() error {
	if a.Cfg.Cloudflare.APIToken == "" {
		return errors.New("--purge-cache: no Cloudflare API token; set SHIP_CF_API_TOKEN or api_token in the [cloudflare] section of config.toml")
	}
	return nil
}

// purgeCache purges everything cached for zone.
func (a *App) purgeCache(ctx context.Context, zone string) error {
	if err := a.CF(a.Cfg.Cloudflare.APIToken).PurgeEverything(ctx, zone); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "Cloudflare cache purged")
	return nil
}
