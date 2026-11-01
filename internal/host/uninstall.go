package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

// Uninstall stops, disables and removes the ref's unit, binary (with its
// .prev) and unit backup; its env file only with purge. When last is set it
// also removes /opt/S, the runtime dir /run/S and its tmpfiles entry, and
// with purge /etc/S and the data dir: both hold what an operator wrote by
// hand (config, secrets, data), so a plain uninstall keeps them for a later
// install. The user goes only when deleteUser is set (ship created it) and
// the data dir is gone. It tolerates a half-installed ref: stop and disable
// errors and missing files are ignored.
func (h *Host) Uninstall(ctx context.Context, service string, ref layout.Ref, unit string, last, purge, deleteUser bool) error {
	p := h.Paths
	in := h.installed(service, ref, unit)

	_ = h.Sys.Systemctl(ctx, "stop", unit)
	_ = h.Sys.Systemctl(ctx, "disable", unit)

	files := []string{in.unitFile, in.unitBackup, in.bin, in.binPrev}
	if purge {
		files = append(files, p.EnvFile(service, ref.Name()))
	}
	for _, f := range files {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	if err := h.Sys.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	_ = h.Sys.Systemctl(ctx, "reset-failed", unit)

	if !last {
		return nil
	}
	for _, d := range []string{p.BinDir(service), p.RunDir(service)} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	if err := removeIfExists(p.Tmpfiles(service)); err != nil {
		return err
	}
	if purge {
		for _, d := range []string{p.EnvDir(service), p.DataDir(service)} {
			if err := os.RemoveAll(d); err != nil {
				return err
			}
		}
	} else if exists(p.EnvDir(service)) {
		// ship v0.1.0 left /etc/S root:root; give the kept dir the group
		// CheckUnclaimed looks for, while the service is still known as ours.
		if err := h.Sys.Chown(p.EnvDir(service), "root:"+service); err != nil {
			return err
		}
	}
	var kept []string
	for _, d := range []struct{ path, shown string }{
		{p.EnvDir(service), Paths{}.EnvDir(service)},
		{p.DataDir(service), Paths{}.DataDir(service)},
	} {
		if exists(d.path) {
			kept = append(kept, d.shown)
		}
	}
	if len(kept) > 0 {
		user := ""
		if deleteUser {
			user = fmt.Sprintf(" (user %s kept)", service)
		}
		fmt.Fprintf(h.Out, "Kept %s%s; remove with --purge\n", strings.Join(kept, " and "), user)
		return nil
	}
	if deleteUser && h.Sys.UserExists(service) {
		if err := h.Sys.DeleteUser(ctx, service); err != nil {
			return fmt.Errorf("delete user %s: %w", service, err)
		}
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
