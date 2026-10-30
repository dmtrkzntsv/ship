package app

import (
	"context"
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// UninstallArgs are the inputs of `ship uninstall`. PurgeCache purges the
// service's Cloudflare zone after a successful uninstall.
type UninstallArgs struct {
	Ref, Service      string
	Purge, PurgeCache bool
}

// Uninstall removes an installed ref from the host and from state. When it
// is the service's last ref the service goes too; Purge then also deletes
// the data dir.
func (a *App) Uninstall(ctx context.Context, args UninstallArgs) error {
	ref, service, err := refService(args.Ref, args.Service)
	if err != nil {
		return err
	}
	if args.PurgeCache {
		// Fail before touching anything; checked again under the lock.
		if err := a.checkPurge(service, ""); err != nil {
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
	rs, err := installedRef(s, ref, service, "")
	if err != nil {
		return err
	}
	svc := s.Services[service]
	if args.PurgeCache {
		if err := a.checkPurgeIn(s, service, ""); err != nil {
			return err
		}
	}
	zone := svc.CFZone // read before the service may be dropped from state
	last := len(svc.Refs) == 1

	if err := a.Host.Uninstall(ctx, service, ref, rs.Unit, last, args.Purge); err != nil {
		return err
	}
	unit := rs.Unit
	delete(svc.Refs, ref.String())
	if last {
		delete(s.Services, service)
	}
	if err := a.State.Save(s); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "uninstalled %s (%s.service)\n", ref, unit)
	if args.PurgeCache {
		if err := a.purgeCache(ctx, zone); err != nil {
			return fmt.Errorf("uninstall of %s succeeded but the cache purge failed: %w", unit, err)
		}
	}
	return nil
}

// Lifecycle runs action ("start", "stop" or "restart") on an installed
// ref's unit; start and restart wait for the unit to stay up.
func (a *App) Lifecycle(ctx context.Context, action, refArg, serviceArg string) error {
	var run func(context.Context, string) error
	switch action {
	case "start":
		run = a.Host.Start
	case "stop":
		run = a.Host.Stop
	case "restart":
		run = a.Host.Restart
	default:
		return fmt.Errorf("unknown action %q: want start, stop or restart", action)
	}
	rs, _, _, err := a.lookup(refArg, serviceArg)
	if err != nil {
		return err
	}
	if err := run(ctx, rs.Unit); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s: %s ok\n", rs.Unit, action)
	return nil
}

// LogsArgs is the journalctl argv for the installed ref's unit.
func (a *App) LogsArgs(refArg, serviceArg string, n int, follow bool) ([]string, error) {
	rs, _, _, err := a.lookup(refArg, serviceArg)
	if err != nil {
		return nil, err
	}
	return a.Host.LogsArgs(rs.Unit, n, follow), nil
}

// RunArgs is the systemd-run argv that runs the installed ref's binary
// with args; tty says whether the caller has a terminal to attach.
func (a *App) RunArgs(refArg, serviceArg string, args []string, tty bool) ([]string, error) {
	_, ref, service, err := a.lookup(refArg, serviceArg)
	if err != nil {
		return nil, err
	}
	return a.Host.RunArgs(service, ref, args, tty), nil
}

// Status prints a table of installed refs (all services, or just service)
// with their channel, version and unit state.
func (a *App) Status(ctx context.Context, service string) error {
	s, err := a.State.Load()
	if err != nil {
		return err
	}
	var names []string
	if service != "" {
		if s.Services[service] == nil {
			return fmt.Errorf("service %s is not installed", service)
		}
		names = []string{service}
	} else {
		for n := range s.Services {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		fmt.Fprintln(a.Out, "no services installed")
		return nil
	}

	tw := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tREF\tUNIT\tCHANNEL\tVERSION\tSTATE")
	for _, n := range names {
		refs := s.Services[n].Refs
		keys := make([]string, 0, len(refs))
		for k := range refs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return refs[keys[i]].Unit < refs[keys[j]].Unit })
		for _, k := range keys {
			rs := refs[k]
			st := "inactive"
			if a.Host.Sys.IsActive(ctx, rs.Unit) {
				st = "active"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n, k, rs.Unit, rs.Channel, rs.Version, st)
		}
	}
	return tw.Flush()
}

// lookup parses the ref/service arguments and finds the installed ref.
func (a *App) lookup(refArg, serviceArg string) (*state.RefState, layout.Ref, string, error) {
	ref, service, err := refService(refArg, serviceArg)
	if err != nil {
		return nil, layout.Ref{}, "", err
	}
	s, err := a.State.Load()
	if err != nil {
		return nil, layout.Ref{}, "", err
	}
	rs, err := installedRef(s, ref, service, "")
	if err != nil {
		return nil, layout.Ref{}, "", err
	}
	return rs, ref, service, nil
}
