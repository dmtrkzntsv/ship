package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Adopt takes over a service installed by the old deploy script (see
// LegacyLayout) for the plain project ref t.Ref, keeping its user, data dir
// and unit name t.Unit. The new unit is rendered and verified, and the new
// env path checked free, before anything changes. It then makes sure the
// tmpfiles entry for /run/S exists, moves /etc/S/env to /etc/S/<project>.env
// and swaps in the fetched binary and unit exactly like Update: restart,
// health check, and the script's binary and unit become the .prev pair.
// Any failure after the env move restores the binary and unit, moves the
// env file back, removes a tmpfiles entry Adopt created, and restarts the
// old unit.
func (h *Host) Adopt(ctx context.Context, t Target) error {
	s, p := t.Service, h.Paths
	if !h.LegacyLayout(s, t.Ref.Project) {
		return fmt.Errorf("%s does not have the old deploy script layout (%s, %s, %s)", s,
			Paths{}.UnitPath(s), Paths{}.Bin(s, t.Ref.Project), filepath.Join(Paths{}.EnvDir(s), "env"))
	}
	oldEnv, newEnv := filepath.Join(p.EnvDir(s), "env"), p.EnvFile(s, t.Ref.Name())
	if exists(newEnv) {
		return fmt.Errorf("%s already exists; move it away before adopting %s", Paths{}.EnvFile(s, t.Ref.Name()), s)
	}
	in := h.installed(s, t.Ref, t.Unit)
	rendered, err := h.renderVerified(ctx, t)
	if err != nil {
		return err
	}
	b, err := in.backup()
	if err != nil {
		return err
	}
	defer b.discard() // no-op after commit

	createdConf, err := h.ensureTmpfiles(ctx, s)
	if err != nil {
		return err
	}
	if err := os.Rename(oldEnv, newEnv); err != nil {
		if createdConf {
			_ = os.Remove(p.Tmpfiles(s))
		}
		return fmt.Errorf("move %s: %w", oldEnv, err)
	}
	undo := func() error {
		errs := []error{in.restore(b)}
		if err := os.Rename(newEnv, oldEnv); err != nil {
			errs = append(errs, fmt.Errorf("move env file back: %w", err))
		}
		return errors.Join(errs...)
	}
	// fail restores the script's files and restarts the old unit; only then
	// is a tmpfiles entry adopt created removed, best effort, so a failed
	// cleanup never keeps the old service down.
	fail := func(cause error) error {
		err := h.revert(ctx, in, undo, "adopt", cause)
		if createdConf {
			if rerr := removeIfExists(p.Tmpfiles(s)); rerr != nil {
				err = fmt.Errorf("%w\nwarning: removing tmpfiles entry %s failed: %v", err, Paths{}.Tmpfiles(s), rerr)
			}
		}
		return err
	}

	if err := copyFileAtomic(t.Fetched.Binary, in.bin, binMode); err != nil {
		return fail(err)
	}
	if err := writeFileAtomic(in.unitFile, rendered, unitMode); err != nil {
		return fail(err)
	}
	if err := h.activate(ctx, in.unit); err != nil {
		return fail(err)
	}
	if err := in.commit(b); err != nil {
		fmt.Fprintf(h.Out, "warning: %v; rollback of %s is unavailable until the next update\n", err, in.unit)
	}
	return nil
}

// ensureTmpfiles writes the tmpfiles entry for /run/S unless one exists and
// has systemd create the directory. It reports whether it wrote the entry;
// if creating the directory fails, an entry it wrote is removed again.
func (h *Host) ensureTmpfiles(ctx context.Context, s string) (created bool, err error) {
	conf := h.Paths.Tmpfiles(s)
	if !exists(conf) {
		if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
			return false, err
		}
		if err := writeFileAtomic(conf, tmpfilesEntry(s), 0o644); err != nil {
			return false, err
		}
		created = true
	}
	if err := h.Sys.CreateTmpfiles(ctx, conf); err != nil {
		if created {
			_ = os.Remove(conf)
		}
		return false, fmt.Errorf("create %s: %w", Paths{}.RunDir(s), err)
	}
	return created, nil
}
