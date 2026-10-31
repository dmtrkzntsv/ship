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
	if err := checkNewServiceName(service); err != nil {
		return err
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
	existing := s.Services[service]
	if existing != nil {
		if err := checkRefCollisions(existing, ref, service); err != nil {
			return err
		}
	}
	unit, err := layout.UnitName(service, ref.Name(), s.Units(service))
	if err != nil {
		return err
	}
	if existing == nil {
		if a.Host.LegacyLayout(service, ref.Project) {
			return fmt.Errorf("%s.service was installed by the old deploy script; take it over with: ship adopt %s --service %s --version <version>", service, ref, service)
		}
		if err := a.Host.CheckUnclaimed(service); err != nil {
			return err
		}
	}
	if a.Host.ForeignUnit(unit) {
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

	createsUser := !a.Host.Sys.UserExists(service)
	if err := a.Host.Install(ctx, host.Target{Service: service, Ref: ref, Unit: unit, Fetched: f}); err != nil {
		return err
	}

	svc := s.Service(service)
	if existing == nil {
		svc.CreatedUser = createsUser
	}
	if args.CFZone != "" {
		svc.CFZone = args.CFZone
	}
	svc.Refs[ref.String()] = &state.RefState{
		Unit: unit, Channel: args.Version, Version: f.Release.Version,
		PublishedAt: f.Release.PublishedAt, InstalledAt: a.Now().UTC(),
	}
	if err := a.State.Save(s); err != nil {
		return err
	}

	ph := a.Host.Paths.Placeholders(service, ref, unit)
	fmt.Fprintf(a.Out, "installed %s %s into service %s as %s.service (enabled, not started)\n", ref, f.Release.Version, service, unit)
	fmt.Fprintf(a.Out, "next: edit %s, then run: ship start %s --service %s\n", ph.EnvFile, ref, service)
	return nil
}

// checkNewServiceName validates a service name given to install or adopt.
func checkNewServiceName(service string) error {
	if !layout.ValidName(service) {
		return fmt.Errorf("invalid service name %q", service)
	}
	if layout.ReservedService(service) {
		return fmt.Errorf("service name %q is reserved (a system user or directory); pick another --service", service)
	}
	return nil
}

// checkRefCollisions refuses ref when it is already installed in svc, or
// when its binary (/opt/S/<asset>) or env file (/etc/S/<name>.env) would be
// the same file as another ref's.
func checkRefCollisions(svc *state.Service, ref layout.Ref, service string) error {
	if svc.Refs[ref.String()] != nil {
		return fmt.Errorf("%s is already installed in service %s (use ship update)", ref, service)
	}
	hp := host.Paths{}
	for key := range svc.Refs {
		other, err := layout.ParseRef(key)
		if err != nil {
			continue
		}
		if other.Asset() == ref.Asset() {
			return fmt.Errorf("%s would use binary %s, which belongs to %s in service %s; install it into another --service", ref, hp.Bin(service, ref.Asset()), other, service)
		}
		if other.Name() == ref.Name() {
			return fmt.Errorf("%s would use env file %s, which belongs to %s in service %s; install it into another --service", ref, hp.EnvFile(service, ref.Name()), other, service)
		}
	}
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
	return layout.ValidateChannel(ch)
}
