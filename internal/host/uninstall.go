package host

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

// Uninstall stops, disables and removes the ref's unit, binary (with its
// .prev), unit backup and env file. When last is set it also removes /opt/S,
// /etc/S, the runtime dir /run/S and its tmpfiles entry, and with purge the
// data dir; the user goes only when deleteUser is set (ship created it) and
// the data dir is gone. It tolerates a
// half-installed ref: stop and disable errors and missing files are ignored.
func (h *Host) Uninstall(ctx context.Context, service string, ref layout.Ref, unit string, last, purge, deleteUser bool) error {
	p := h.Paths
	in := h.installed(service, ref, unit)

	_ = h.Sys.Systemctl(ctx, "stop", unit)
	_ = h.Sys.Systemctl(ctx, "disable", unit)

	for _, f := range []string{in.unitFile, in.unitBackup, in.bin, in.binPrev, p.EnvFile(service, ref.Name())} {
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
	for _, d := range []string{p.BinDir(service), p.EnvDir(service), p.RunDir(service)} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	if err := removeIfExists(p.Tmpfiles(service)); err != nil {
		return err
	}
	if purge {
		if err := os.RemoveAll(p.DataDir(service)); err != nil {
			return err
		}
	}
	if exists(p.DataDir(service)) {
		if deleteUser {
			fmt.Fprintf(h.Out, "Data kept at %s (user %s kept); remove it with --purge\n", Paths{}.DataDir(service), service)
		} else {
			fmt.Fprintf(h.Out, "Data kept at %s; remove it with --purge\n", Paths{}.DataDir(service))
		}
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
