// Package selfupdate replaces the running ship binary with the one published
// under ship/<channel>/ in the object store.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// Run fetches ship/<channel>/ (verified against SHA256SUMS) and atomically
// replaces exe with the binary for this host's arch. It returns the version
// that was installed.
func Run(ctx context.Context, st store.Store, channel, exe string, out io.Writer) (string, error) {
	return Update(ctx, st, channel, exe, "", out)
}

// Update is Run that also knows the running version: when the channel's
// release.json version equals current, exe is left alone. It reports the
// outcome on out as "ship: <old> -> <new>" or "ship: already on <version>".
func Update(ctx context.Context, st store.Store, channel, exe, current string, out io.Writer) (string, error) {
	dir := filepath.Dir(exe)
	// Create the replacement next to exe first: it proves the directory is
	// writable and guarantees the final rename stays on one filesystem.
	tmp, err := os.CreateTemp(dir, ".ship-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to %s (try: sudo ship self-update): %w", dir, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	defer tmp.Close()

	work, err := os.MkdirTemp("", "ship-selfupdate-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	ref, err := layout.ParseRef("ship")
	if err != nil {
		return "", err
	}
	f, err := release.Fetch(ctx, st, ref, channel, release.HostArch(), work)
	if err != nil {
		return "", err
	}
	version := f.Release.Version
	if current != "" && version == current {
		fmt.Fprintf(out, "ship: already on %s\n", version)
		return version, nil
	}

	src, err := os.Open(f.Binary)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if _, err := io.Copy(tmp, src); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return "", fmt.Errorf("replace %s: %w", exe, err)
	}
	if current != "" {
		fmt.Fprintf(out, "ship: %s -> %s\n", current, version)
	}
	return version, nil
}
