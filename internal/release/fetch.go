package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/sums"
)

// Fetched is a verified release downloaded into Dir.
type Fetched struct {
	Dir        string
	Release    Release
	Binary     string // path in Dir
	Unit       string // path in Dir; "" for tool projects
	EnvExample string // path in Dir; "" when not published
}

// ErrNoUnit is what install maps a Fetched with an empty Unit to.
var ErrNoUnit = errors.New("project publishes no systemd unit")

// HostArch is the linux arch name of the running binary ("amd64", "arm64", "arm").
func HostArch() string { return runtime.GOARCH }

// Fetch downloads the files of ref's channel needed to install it for arch
// into dir (which the caller owns) and verifies each against SHA256SUMS.
// On any error every file written to dir by this call is removed.
func Fetch(ctx context.Context, st store.Store, ref layout.Ref, channel, arch, dir string) (_ Fetched, err error) {
	prefix := ref.Project + "/" + channel + "/"

	sumsRaw, err := readAll(ctx, st, prefix+SumsFile)
	if errors.Is(err, store.ErrNotFound) {
		return Fetched{}, fmt.Errorf("channel %q not found for %s", channel, ref.Project)
	}
	if err != nil {
		return Fetched{}, err
	}
	sm, err := sums.Parse(sumsRaw)
	if err != nil {
		return Fetched{}, fmt.Errorf("%s%s: %w", prefix, SumsFile, err)
	}

	asset := ref.Asset()
	binName := layout.BinaryFile(asset, arch)
	if _, ok := sm[binName]; !ok {
		return Fetched{}, fmt.Errorf("no linux/%s build", arch)
	}

	var written []string
	defer func() {
		if err != nil {
			for _, p := range written {
				os.Remove(p)
			}
		}
	}()

	f := Fetched{Dir: dir}
	relRaw, err := readAll(ctx, st, prefix+ReleaseFile)
	if err != nil {
		return Fetched{}, fmt.Errorf("%s: %w", ReleaseFile, err)
	}
	if err := sm.Verify(ReleaseFile, bytes.NewReader(relRaw)); err != nil {
		return Fetched{}, err
	}
	if err := json.Unmarshal(relRaw, &f.Release); err != nil {
		return Fetched{}, fmt.Errorf("%s: %w", ReleaseFile, err)
	}

	fetch := func(name string, mode os.FileMode) (string, error) {
		path := filepath.Join(dir, name)
		written = append(written, path)
		if err := download(ctx, st, prefix+name, path, mode, sm, name); err != nil {
			return "", err
		}
		return path, nil
	}

	if f.Binary, err = fetch(binName, 0o755); err != nil {
		return Fetched{}, err
	}
	if _, ok := sm[layout.UnitFile(asset)]; ok {
		if f.Unit, err = fetch(layout.UnitFile(asset), 0o644); err != nil {
			return Fetched{}, err
		}
	}
	if _, ok := sm[layout.EnvExampleFile(asset)]; ok {
		if f.EnvExample, err = fetch(layout.EnvExampleFile(asset), 0o644); err != nil {
			return Fetched{}, err
		}
	}
	return f, nil
}

// download streams key into path while hashing it; a file that does not
// verify is removed by the caller's cleanup (its path is already recorded).
func download(ctx context.Context, st store.Store, key, path string, mode os.FileMode, sm sums.Sums, name string) error {
	body, err := st.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer body.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	verr := sm.Verify(name, io.TeeReader(body, out))
	if cerr := out.Close(); verr == nil {
		verr = cerr
	}
	if verr != nil {
		return verr
	}
	return os.Chmod(path, mode) // OpenFile's mode is subject to umask
}

func readAll(ctx context.Context, st store.Store, key string) ([]byte, error) {
	body, err := st.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}
