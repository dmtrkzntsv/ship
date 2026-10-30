package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// InstallArgs are the inputs of `ship install`. Service defaults to the
// ref's project; Version is the channel to install (a version or latest/dev).
type InstallArgs struct{ Ref, Service, Version, CFZone string }

// Install downloads and verifies a release, lays it out on the host as a new
// ref of the service (enabled, not started) and records it in state.
func (a *App) Install(ctx context.Context, args InstallArgs) error {
	ref, err := layout.ParseRef(args.Ref)
	if err != nil {
		return err
	}
	service := args.Service
	if service == "" {
		service = ref.DefaultService()
	}
	if !layout.ValidName(service) {
		return fmt.Errorf("invalid service name %q", service)
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
	if svc := s.Services[service]; svc != nil && svc.Refs[ref.String()] != nil {
		return fmt.Errorf("%s is already installed in service %s (use ship update)", ref, service)
	}
	unit, err := layout.UnitName(service, ref.Name(), s.Units(service))
	if err != nil {
		return err
	}
	if a.Host.ForeignUnit(unit) {
		if a.Host.LegacyLayout(service, ref.Project) {
			return fmt.Errorf("%s.service was installed by the old deploy script; take it over with: ship adopt %s --service %s --version <version>", unit, ref, service)
		}
		return fmt.Errorf("%s exists and is not managed by ship; remove it first", host.Paths{}.UnitPath(unit))
	}

	st, err := a.NewStore()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "ship-install-")
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

	if err := a.Host.Install(ctx, host.Target{Service: service, Ref: ref, Unit: unit, Fetched: f}); err != nil {
		return err
	}

	svc := s.Service(service)
	if args.CFZone != "" {
		svc.CFZone = args.CFZone
	}
	svc.Refs[ref.String()] = &state.RefState{
		Unit: unit, Channel: args.Version, Version: f.Release.Version, InstalledAt: a.Now().UTC(),
	}
	if err := a.State.Save(s); err != nil {
		return err
	}

	ph := a.Host.Paths.Placeholders(service, ref, unit)
	fmt.Fprintf(a.Out, "installed %s %s into service %s as %s.service (enabled, not started)\n", ref, f.Release.Version, service, unit)
	fmt.Fprintf(a.Out, "next: edit %s, then run: ship start %s --service %s\n", ph.EnvFile, ref, service)
	return nil
}

// noUnitError reports a tool project (no unit template); it wraps release.ErrNoUnit.
type noUnitError struct{ ref layout.Ref }

func (e noUnitError) Error() string {
	return e.ref.String() + " publishes no systemd unit; it cannot be installed as a service"
}

func (e noUnitError) Unwrap() error { return release.ErrNoUnit }

// validateChannel accepts a moving channel (latest, dev) or a valid version.
func validateChannel(ch string) error {
	if ch == "" {
		return errors.New("--version is required (a version, latest or dev)")
	}
	if layout.IsMoving(ch) {
		return nil
	}
	return layout.ValidateVersion(ch)
}
