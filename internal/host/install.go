package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
)

// Host installs and manages services under Paths using Sys.
type Host struct {
	Paths                  Paths
	Sys                    Sys
	Out                    io.Writer
	HealthWait, HealthTick time.Duration
}

// Target is one project ref being installed into a service as unit.
type Target struct {
	Service string
	Ref     layout.Ref
	Unit    string
	Fetched release.Fetched
}

// Install lays out a fetched release for t: user, directories, tmpfiles
// entry, binary, env file (if absent) and verified unit; then daemon-reload
// and enable (a failure there removes the new unit file again). It never
// starts the unit. The unit template is rendered before
// anything on the host is touched, and the unit is only moved into place
// after `systemd-analyze verify` accepts it.
func (h *Host) Install(ctx context.Context, t Target) error {
	s, p := t.Service, h.Paths
	tmpl, err := os.ReadFile(t.Fetched.Unit)
	if err != nil {
		return fmt.Errorf("read unit template: %w", err)
	}
	rendered, err := layout.RenderUnit(string(tmpl), p.Placeholders(s, t.Ref, t.Unit))
	if err != nil {
		return err
	}

	if !h.Sys.UserExists(s) {
		if err := h.Sys.CreateUser(ctx, s, Paths{}.DataDir(s)); err != nil {
			return fmt.Errorf("create user %s: %w", s, err)
		}
	}
	if err := makeDir(p.BinDir(s), 0o755); err != nil {
		return err
	}
	if err := makeDir(p.DataDir(s), 0o750); err != nil {
		return err
	}
	if err := h.Sys.Chown(p.DataDir(s), s+":"+s); err != nil {
		return err
	}
	if err := makeDir(p.EnvDir(s), 0o750); err != nil {
		return err
	}

	conf := p.Tmpfiles(s)
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		return err
	}
	line := fmt.Sprintf("d %s 0750 %s %s -\n", Paths{}.RunDir(s), s, s)
	if err := writeFileAtomic(conf, []byte(line), 0o644); err != nil {
		return err
	}
	if err := h.Sys.CreateTmpfiles(ctx, conf); err != nil {
		return fmt.Errorf("create %s: %w", Paths{}.RunDir(s), err)
	}

	if err := copyFileAtomic(t.Fetched.Binary, p.Bin(s, t.Ref.Asset()), 0o755); err != nil {
		return err
	}

	scratch, err := os.MkdirTemp("", "ship-unit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	staged := filepath.Join(scratch, t.Unit+".service")
	if err := os.WriteFile(staged, []byte(rendered), 0o644); err != nil {
		return err
	}
	if err := h.Sys.VerifyUnit(ctx, staged); err != nil {
		return fmt.Errorf("unit %s.service does not verify: %w", t.Unit, err)
	}
	// The env file goes in before the unit: once the unit file is in place it
	// is the marker a retry refuses on, so nothing fallible may follow it
	// without undoing it.
	if err := h.installEnv(t); err != nil {
		return err
	}

	unitPath := p.UnitPath(t.Unit)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(unitPath, []byte(rendered), 0o644); err != nil {
		return err
	}
	if err := h.Sys.DaemonReload(ctx); err != nil {
		h.removeUnit(ctx, unitPath)
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := h.Sys.Systemctl(ctx, "enable", t.Unit); err != nil {
		h.removeUnit(ctx, unitPath)
		return fmt.Errorf("enable %s: %w", t.Unit, err)
	}
	return nil
}

// removeUnit undoes placing a new unit file after a later install step
// failed, so a retry is not refused as a foreign unit. Best effort.
func (h *Host) removeUnit(ctx context.Context, unitPath string) {
	if err := os.Remove(unitPath); err == nil {
		_ = h.Sys.DaemonReload(ctx)
	}
}

// installEnv creates the ref's env file from the example (or empty) unless
// one already exists; an existing file is left exactly as it is.
func (h *Host) installEnv(t Target) error {
	path := h.Paths.EnvFile(t.Service, t.Ref.Name())
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var content []byte
	if t.Fetched.EnvExample != "" {
		b, err := os.ReadFile(t.Fetched.EnvExample)
		if err != nil {
			return fmt.Errorf("read env example: %w", err)
		}
		content = b
	}
	if err := writeFileAtomic(path, content, 0o640); err != nil {
		return err
	}
	return h.Sys.Chown(path, "root:"+t.Service)
}

// ForeignUnit reports whether the unit file exists on disk.
func (h *Host) ForeignUnit(unit string) bool { return exists(h.Paths.UnitPath(unit)) }

// LegacyLayout reports whether service has the layout of the old install
// script: /etc/systemd/system/S.service, /opt/S/<project> and /etc/S/env.
func (h *Host) LegacyLayout(service, project string) bool {
	p := h.Paths
	return exists(p.UnitPath(service)) &&
		exists(p.Bin(service, project)) &&
		exists(filepath.Join(p.EnvDir(service), "env"))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// makeDir creates dir (and parents) and sets its mode, regardless of umask.
func makeDir(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}

// writeFileAtomic writes data to a temp file next to path and renames it
// into place, so readers see either the old or the new file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return replaceAtomic(path, mode, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// copyFileAtomic copies src to dst via a temp file in dst's directory.
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return replaceAtomic(dst, mode, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

func replaceAtomic(path string, mode os.FileMode, fill func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := fill(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}
