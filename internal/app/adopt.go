package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// AdoptArgs are the inputs of `ship adopt`. Ref is a plain project; Service
// defaults to it; Version is the channel to install and remember.
type AdoptArgs struct{ Ref, Service, Version, CFZone string }

// Adopt takes over a service installed by the old deploy script: it swaps in
// the channel's binary and unit under the existing unit name (the service
// name), health-checks it and records the service in state. The host
// restores the script's files on failure, and state only changes after a
// successful adopt.
func (a *App) Adopt(ctx context.Context, args AdoptArgs) error {
	ref, service, err := refService(args.Ref, args.Service)
	if err != nil {
		return err
	}
	if ref.Component != "" {
		return errors.New("adopt supports plain projects only (the old deploy script installed whole projects)")
	}
	if err := validateChannel(args.Version); err != nil {
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
	if s.Services[service] != nil {
		return fmt.Errorf("service %s is already managed by ship; nothing to adopt", service)
	}
	if !a.Host.LegacyLayout(service, ref.Project) {
		return fmt.Errorf("service %s was not installed by the old deploy script (expected %s, %s and /etc/%s/env)",
			service, host.Paths{}.UnitPath(service), host.Paths{}.Bin(service, ref.Project), service)
	}

	st, err := a.NewStore()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "ship-adopt-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f, err := release.Fetch(ctx, st, ref, args.Version, a.Arch, dir)
	if err != nil {
		return err
	}
	if f.Unit == "" {
		return noUnitError{ref}
	}

	unit := service
	if err := a.Host.Adopt(ctx, host.Target{Service: service, Ref: ref, Unit: unit, Fetched: f}); err != nil {
		return err
	}

	svc := s.Service(service)
	svc.CFZone = args.CFZone
	svc.Refs[ref.String()] = &state.RefState{
		Unit: unit, Channel: args.Version, Version: f.Release.Version, InstalledAt: a.Now().UTC(),
	}
	if err := a.State.Save(s); err != nil {
		return fmt.Errorf("%s.service was adopted and is running %s, but saving state failed: %w", unit, f.Release.Version, err)
	}
	ph := a.Host.Paths.Placeholders(service, ref, unit)
	fmt.Fprintf(a.Out, "adopted %s.service: %s %s is running; env moved to %s\n", unit, ref, f.Release.Version, ph.EnvFile)
	return nil
}
