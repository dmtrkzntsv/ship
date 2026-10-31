// Package host lays out and manages installed services on a systemd host:
// files under a configurable root plus a Sys interface for systemd and users.
package host

import (
	"path/filepath"

	"github.com/dmtrkzntsv/ship/internal/layout"
)

// Paths computes host paths. Root is "" in production and a temp dir in
// tests; every method returns the Root-prefixed path ship reads and writes.
type Paths struct{ Root string }

func (p Paths) join(parts ...string) string {
	return filepath.Join(append([]string{"/", p.Root}, parts...)...)
}

// BinDir is /opt/S.
func (p Paths) BinDir(s string) string { return p.join("opt", s) }

// Bin is /opt/S/A.
func (p Paths) Bin(s, asset string) string { return p.join("opt", s, asset) }

// EnvDir is /etc/S.
func (p Paths) EnvDir(s string) string { return p.join("etc", s) }

// EnvFile is /etc/S/N.env.
func (p Paths) EnvFile(s, name string) string { return p.join("etc", s, name+".env") }

// DataDir is /var/lib/S.
func (p Paths) DataDir(s string) string { return p.join("var", "lib", s) }

// RunDir is /run/S.
func (p Paths) RunDir(s string) string { return p.join("run", s) }

// UnitPath is /etc/systemd/system/<unit>.service.
func (p Paths) UnitPath(unit string) string {
	return p.join("etc", "systemd", "system", unit+".service")
}

// UnitBackup is /opt/S/<unit>.service.prev.
func (p Paths) UnitBackup(s, unit string) string { return p.join("opt", s, unit+".service.prev") }

// Tmpfiles is /etc/tmpfiles.d/ship-S.conf.
func (p Paths) Tmpfiles(s string) string { return p.join("etc", "tmpfiles.d", "ship-"+s+".conf") }

// Placeholders returns the unit template values for ref in service s with
// the given unit name. They are paths as the host sees them (without Root).
func (p Paths) Placeholders(s string, ref layout.Ref, unit string) layout.Placeholders {
	h := Paths{}
	return layout.Placeholders{
		Service: s,
		Bin:     h.Bin(s, ref.Asset()),
		BinDir:  h.BinDir(s),
		EnvFile: h.EnvFile(s, ref.Name()),
		DataDir: h.DataDir(s),
		RunDir:  h.RunDir(s),
		Unit:    unit,
	}
}
