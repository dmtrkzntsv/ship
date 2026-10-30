package host

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

// Start starts the unit and waits for it to stay healthy.
func (h *Host) Start(ctx context.Context, unit string) error {
	if err := h.Sys.Systemctl(ctx, "start", unit); err != nil {
		return fmt.Errorf("start %s: %w", unit, err)
	}
	return h.WaitHealthy(ctx, unit)
}

// Stop stops the unit.
func (h *Host) Stop(ctx context.Context, unit string) error {
	if err := h.Sys.Systemctl(ctx, "stop", unit); err != nil {
		return fmt.Errorf("stop %s: %w", unit, err)
	}
	return nil
}

// Restart restarts the unit and waits for it to stay healthy.
func (h *Host) Restart(ctx context.Context, unit string) error {
	if err := h.Sys.Systemctl(ctx, "restart", unit); err != nil {
		return fmt.Errorf("restart %s: %w", unit, err)
	}
	return h.WaitHealthy(ctx, unit)
}

// LogsArgs is the journalctl argv (including argv[0]) that shows the last n
// lines of unit and, with follow, keeps following. A LogNamespace= in the
// installed unit file adds --namespace.
func (h *Host) LogsArgs(unit string, n int, follow bool) []string {
	args := []string{"journalctl", "-u", unit + ".service"}
	if ns := h.logNamespace(unit); ns != "" {
		args = append(args, "--namespace="+ns)
	}
	args = append(args, "-n", strconv.Itoa(n))
	if follow {
		args = append(args, "-f")
	}
	return args
}

// logNamespace reads LogNamespace= from the installed unit file; a missing
// or unreadable file means no namespace.
func (h *Host) logNamespace(unit string) string {
	f, err := os.Open(h.Paths.UnitPath(unit))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "LogNamespace="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// RunArgs is the systemd-run argv (including argv[0]) that runs the ref's
// binary with args under the service's user, env file and data dir. With
// tty the command gets a pseudo-terminal (--pty); without one (scripts, CI)
// its stdio is passed straight through (--pipe).
func (h *Host) RunArgs(service string, ref layout.Ref, args []string, tty bool) []string {
	hp := Paths{}
	stdio := "--pipe"
	if tty {
		stdio = "--pty"
	}
	argv := []string{
		"systemd-run", stdio, "--quiet", "--wait", "--collect",
		"--property=EnvironmentFile=" + hp.EnvFile(service, ref.Name()),
		"--property=User=" + service,
		"--property=WorkingDirectory=" + hp.DataDir(service),
		"--property=ReadWritePaths=" + hp.DataDir(service),
		hp.Bin(service, ref.Asset()),
	}
	return append(argv, args...)
}
