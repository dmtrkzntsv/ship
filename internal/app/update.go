package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// UpdateArgs are the inputs of `ship update`. Service defaults to the ref's
// project; Version, when set, becomes the ref's remembered channel.
// CFZone, when set, replaces the service's stored Cloudflare zone once the
// update succeeds; PurgeCache purges the effective zone after an update that
// changed the version.
type UpdateArgs struct {
	Ref, Service, Version, CFZone string
	PurgeCache                    bool
}

// Update fetches the ref's channel and swaps it in on the host with a
// health check; the host restores the previous pair on failure, and state
// only changes after a successful update.
func (a *App) Update(ctx context.Context, args UpdateArgs) error {
	ref, service, err := refService(args.Ref, args.Service)
	if err != nil {
		return err
	}
	if args.Version != "" {
		if err := validateChannel(args.Version); err != nil {
			return err
		}
	}

	if args.PurgeCache {
		// Fail before touching anything; checked again under the lock.
		if err := a.checkPurge(service, args.CFZone); err != nil {
			return err
		}
	}

	unlock, err := a.State.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	s, err := a.State.Load()
	if err != nil {
		return err
	}
	rs, err := installedRef(s, ref, service, args.Version)
	if err != nil {
		return err
	}
	if args.PurgeCache {
		if err := a.checkPurgeIn(s, service, args.CFZone); err != nil {
			return err
		}
	}
	channel := args.Version
	if channel == "" {
		channel = rs.Channel
	}

	st, err := a.NewStore()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "ship-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f, err := release.Fetch(ctx, st, ref, channel, a.Arch, dir)
	if err != nil {
		return err
	}
	if f.Unit == "" {
		return noUnitError{ref}
	}

	// Re-installing the running release would only restart the unit and
	// overwrite the rollback target with the same release. A channel-only
	// publish keeps the channel name as its version, so the publish time
	// must match too.
	if f.Release.Version == rs.Version && f.Release.PublishedAt.Equal(rs.PublishedAt) {
		svc := s.Services[service]
		if rs.Channel != channel || (args.CFZone != "" && svc.CFZone != args.CFZone) {
			rs.Channel = channel
			if args.CFZone != "" {
				svc.CFZone = args.CFZone
			}
			if err := a.State.Save(s); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.Out, "%s: already on %s\n", rs.Unit, rs.Version)
		return nil
	}

	if err := a.Host.Update(ctx, host.Target{Service: service, Ref: ref, Unit: rs.Unit, Fetched: f}); err != nil {
		return err
	}

	old := rs.Version
	rs.Channel, rs.PrevVersion, rs.Version = channel, old, f.Release.Version
	rs.PrevPublishedAt, rs.PublishedAt = rs.PublishedAt, f.Release.PublishedAt
	rs.InstalledAt = a.Now().UTC()
	zone := s.Services[service].CFZone
	if args.CFZone != "" {
		s.Services[service].CFZone, zone = args.CFZone, args.CFZone
	}
	if err := a.State.Save(s); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s: %s -> %s%s\n", rs.Unit, old, rs.Version, republished(old, rs))
	if args.PurgeCache {
		if err := a.purgeCache(ctx, zone); err != nil {
			return fmt.Errorf("update of %s succeeded (not rolled back) but the cache purge failed: %w", rs.Unit, err)
		}
	}
	return nil
}

// Rollback swaps the ref back to the binary and unit it ran before its last
// update (or rollback) and records that version in state.
func (a *App) Rollback(ctx context.Context, refArg, serviceArg string) error {
	ref, service, err := refService(refArg, serviceArg)
	if err != nil {
		return err
	}

	unlock, err := a.State.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	s, err := a.State.Load()
	if err != nil {
		return err
	}
	rs, err := installedRef(s, ref, service, "")
	if err != nil {
		return err
	}
	if rs.PrevVersion == "" {
		return fmt.Errorf("nothing to roll back for %s", rs.Unit)
	}

	if err := a.Host.Rollback(ctx, service, ref, rs.Unit); err != nil {
		return err
	}

	old := rs.Version
	rs.Version, rs.PrevVersion = rs.PrevVersion, old
	rs.PublishedAt, rs.PrevPublishedAt = rs.PrevPublishedAt, rs.PublishedAt
	rs.InstalledAt = a.Now().UTC()
	if err := a.State.Save(s); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s: %s -> %s%s\n", rs.Unit, old, rs.Version, republished(old, rs))
	return nil
}

// republished explains a change between two releases with the same version
// name (channel-only publishes) by the new one's publish time.
func republished(old string, rs *state.RefState) string {
	if old != rs.Version || rs.PublishedAt.IsZero() {
		return ""
	}
	return " (published " + rs.PublishedAt.UTC().Format(time.RFC3339) + ")"
}

// refService parses ref and resolves the service (default: the ref's project).
func refService(refArg, service string) (layout.Ref, string, error) {
	ref, err := layout.ParseRef(refArg)
	if err != nil {
		return layout.Ref{}, "", err
	}
	if service == "" {
		service = ref.DefaultService()
	}
	if !layout.ValidName(service) {
		return layout.Ref{}, "", fmt.Errorf("invalid service name %q", service)
	}
	return ref, service, nil
}

// installedRef returns the state of ref in service, or an error pointing at
// ship install; channel fills the suggested --version when known.
func installedRef(s *state.State, ref layout.Ref, service, channel string) (*state.RefState, error) {
	if svc := s.Services[service]; svc != nil {
		if rs := svc.Refs[ref.String()]; rs != nil {
			return rs, nil
		}
	}
	if channel == "" {
		channel = "<channel>"
	}
	return nil, fmt.Errorf("%s is not installed in service %s; install it with: ship install %s --service %s --version %s", ref, service, ref, service, channel)
}
