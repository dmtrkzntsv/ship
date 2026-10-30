package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

// installed is the live binary/unit pair of one ref and its .prev backup.
type installed struct {
	unit                 string
	bin, binPrev         string
	unitFile, unitBackup string
}

func (h *Host) installed(service string, ref layout.Ref, unit string) installed {
	p := h.Paths
	bin := p.Bin(service, ref.Asset())
	return installed{
		unit: unit,
		bin:  bin, binPrev: bin + ".prev",
		unitFile: p.UnitPath(unit), unitBackup: p.UnitBackup(service, unit),
	}
}

const (
	binMode  os.FileMode = 0o755
	unitMode os.FileMode = 0o644
)

// Update replaces the ref's binary and unit with the fetched release and
// restarts its unit (never a sibling's), then waits for it to stay healthy.
// The new unit is rendered and verified before anything on the host
// changes. The live pair is staged as a backup beside .prev; only after the
// health check passes does it replace the .prev pair, so a failed update
// keeps the existing rollback target. If the swap, daemon-reload, restart
// or health check fails, both files are restored from the staged backup and
// the unit is reloaded and restarted on the old pair.
func (h *Host) Update(ctx context.Context, t Target) error {
	in := h.installed(t.Service, t.Ref, t.Unit)
	rendered, err := h.renderVerified(ctx, t)
	if err != nil {
		return err
	}
	b, err := in.backup()
	if err != nil {
		return err
	}
	defer b.discard() // no-op after commit
	if err := copyFileAtomic(t.Fetched.Binary, in.bin, binMode); err != nil {
		return err // the atomic copy left the live binary as it was
	}
	if err := writeFileAtomic(in.unitFile, rendered, unitMode); err != nil {
		if rerr := copyFileAtomic(b.bin, in.bin, binMode); rerr != nil {
			return fmt.Errorf("%w; restoring %s also failed: %v", err, in.bin, rerr)
		}
		return err
	}
	if err := h.activate(ctx, in.unit); err != nil {
		return h.revert(ctx, in, func() error { return in.restore(b) }, "update", err)
	}
	if err := in.commit(b); err != nil {
		// The new pair is running and healthy; only the rollback target is
		// gone, so report it without failing the update.
		fmt.Fprintf(h.Out, "warning: %v; rollback of %s is unavailable until the next update\n", err, in.unit)
	}
	return nil
}

// Rollback swaps the ref's binary and unit with their .prev copies, so the
// pair it rolls away from becomes the new .prev and a second rollback goes
// forward again. It restarts the unit and waits for health; on failure it
// swaps back and restarts the pair that was running before.
func (h *Host) Rollback(ctx context.Context, service string, ref layout.Ref, unit string) error {
	in := h.installed(service, ref, unit)
	if !exists(in.binPrev) || !exists(in.unitBackup) {
		return fmt.Errorf("nothing to roll back for %s", unit)
	}
	if err := in.swap(); err != nil {
		return err
	}
	if err := h.activate(ctx, in.unit); err != nil {
		return h.revert(ctx, in, in.swap, "rollback", err)
	}
	return nil
}

// renderVerified renders t's unit template and has systemd verify it from a
// scratch copy; it touches nothing on the host.
func (h *Host) renderVerified(ctx context.Context, t Target) ([]byte, error) {
	tmpl, err := os.ReadFile(t.Fetched.Unit)
	if err != nil {
		return nil, fmt.Errorf("read unit template: %w", err)
	}
	rendered, err := layout.RenderUnit(string(tmpl), h.Paths.Placeholders(t.Service, t.Ref, t.Unit))
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "ship-unit-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	staged := filepath.Join(scratch, t.Unit+".service")
	if err := os.WriteFile(staged, []byte(rendered), unitMode); err != nil {
		return nil, err
	}
	if err := h.Sys.VerifyUnit(ctx, staged); err != nil {
		return nil, fmt.Errorf("unit %s.service does not verify: %w", t.Unit, err)
	}
	return []byte(rendered), nil
}

// activate reloads systemd, restarts unit and waits for it to stay up. A
// failed restart carries the unit's last log lines like a failed health check.
func (h *Host) activate(ctx context.Context, unit string) error {
	if err := h.Sys.DaemonReload(ctx); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := h.Sys.Systemctl(ctx, "restart", unit); err != nil {
		return fmt.Errorf("restart %s: %w%s", unit, err, h.logTail(ctx, unit))
	}
	return h.WaitHealthy(ctx, unit)
}

// revert undoes a failed update or rollback: undo puts the previous pair
// back, then systemd is reloaded and the unit restarted on it. It runs even
// if ctx was cancelled, so an interrupted run does not leave the new pair.
func (h *Host) revert(ctx context.Context, in installed, undo func() error, op string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if err := undo(); err != nil {
		return fmt.Errorf("%s failed and restoring the previous binary and unit failed too (%v): %w", op, err, cause)
	}
	if err := h.Sys.DaemonReload(ctx); err != nil {
		return fmt.Errorf("%s failed; previous binary and unit restored but daemon-reload failed (%v): %w", op, err, cause)
	}
	if err := h.Sys.Systemctl(ctx, "restart", in.unit); err != nil {
		return fmt.Errorf("%s failed; previous binary and unit restored but restarting %s failed (%v): %w", op, in.unit, err, cause)
	}
	return fmt.Errorf("%s failed, previous version restored: %w", op, cause)
}

// backupSet is a staged copy of a live binary/unit pair, held in temp
// files beside the .prev files until an update either commits or discards it.
type backupSet struct{ bin, unit string }

// backup stages copies of the live binary and unit beside their .prev
// files, leaving the existing .prev pair untouched. Both live files must
// exist: updating a ref whose files are missing would leave nothing to
// restore.
func (in installed) backup() (backupSet, error) {
	var b backupSet
	for _, f := range []struct {
		live, prev string
		mode       os.FileMode
		dst        *string
	}{{in.bin, in.binPrev, binMode, &b.bin}, {in.unitFile, in.unitBackup, unitMode, &b.unit}} {
		if !exists(f.live) {
			b.discard()
			return backupSet{}, fmt.Errorf("%s is missing; is %s installed on this host?", f.live, in.unit)
		}
		staged, err := stageCopy(f.live, filepath.Dir(f.prev), f.mode)
		if err != nil {
			b.discard()
			return backupSet{}, fmt.Errorf("back up %s: %w", f.live, err)
		}
		*f.dst = staged
	}
	return b, nil
}

// restore copies the staged backup back over the live binary and unit.
func (in installed) restore(b backupSet) error {
	if err := copyFileAtomic(b.bin, in.bin, binMode); err != nil {
		return err
	}
	return copyFileAtomic(b.unit, in.unitFile, unitMode)
}

// commit moves the staged backup over the .prev pair. If only one of the
// two moves succeeds, both .prev files are removed so a later rollback is
// refused rather than restoring a mismatched pair.
func (in installed) commit(b backupSet) error {
	if err := os.Rename(b.unit, in.unitBackup); err != nil {
		return fmt.Errorf("keep previous unit as %s: %w", in.unitBackup, err)
	}
	if err := os.Rename(b.bin, in.binPrev); err != nil {
		os.Remove(in.unitBackup)
		os.Remove(in.binPrev)
		return fmt.Errorf("keep previous binary as %s: %w", in.binPrev, err)
	}
	return nil
}

// discard removes whatever staged backup files remain.
func (b backupSet) discard() {
	for _, p := range []string{b.bin, b.unit} {
		if p != "" {
			os.Remove(p)
		}
	}
}

// swap exchanges the live binary and unit with their .prev files. If the
// unit cannot be swapped, the binary is swapped back so the pair stays
// matched.
func (in installed) swap() error {
	if err := swapFiles(in.bin, in.binPrev, binMode); err != nil {
		return err
	}
	if err := swapFiles(in.unitFile, in.unitBackup, unitMode); err != nil {
		if rerr := swapFiles(in.bin, in.binPrev, binMode); rerr != nil {
			return fmt.Errorf("%w; swapping %s back also failed: %v", err, in.bin, rerr)
		}
		return err
	}
	return nil
}

// swapFiles exchanges the contents of live and prev (which may be on
// different filesystems). Each file is replaced atomically, so a reader
// never sees a partial file; the old live content is staged next to prev
// first so it survives until it is in place.
func swapFiles(live, prev string, mode os.FileMode) error {
	staged, err := stageCopy(live, filepath.Dir(prev), mode)
	if err != nil {
		return err
	}
	defer os.Remove(staged) // no-op after the rename
	if err := copyFileAtomic(prev, live, mode); err != nil {
		return err
	}
	if err := os.Rename(staged, prev); err != nil {
		// live now holds prev's content; put the original back.
		if rerr := copyFileAtomic(staged, live, mode); rerr != nil {
			return fmt.Errorf("move %s into place: %w; restoring %s also failed: %v", prev, err, live, rerr)
		}
		return fmt.Errorf("move %s into place: %w", prev, err)
	}
	return nil
}

// stageCopy copies src into a new temp file in dir and returns its path.
func stageCopy(src, dir string, mode os.FileMode) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(src)+".*.tmp")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, in)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("stage %s: %w", src, err)
	}
	return tmp.Name(), nil
}
