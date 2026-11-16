package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

// ErrBadBuild marks a fetch that failed because what the store holds is
// wrong (missing asset or arch, checksum mismatch, malformed or foreign
// metadata), as opposed to the store being unreachable or the channel absent.
var ErrBadBuild = errors.New("bad build")

// ErrMismatch marks a bad build whose files do not match SHA256SUMS (a
// checksum mismatch or a file not listed). Such a fetch can also be a
// publish landing in between, which a later fetch tells apart.
var ErrMismatch = errors.New("checksum mismatch")

// MismatchError is a verification failure against SHA256SUMS; SumsHash is
// the hex SHA-256 of the SHA256SUMS bytes the files were checked against.
// errors.Is matches both ErrMismatch and ErrBadBuild.
type MismatchError struct {
	Err      error
	SumsHash string
}

func (e *MismatchError) Error() string   { return e.Err.Error() }
func (e *MismatchError) Unwrap() []error { return []error{e.Err, ErrMismatch, ErrBadBuild} }

// badBuild keeps err's text and makes errors.Is(.., ErrBadBuild) true.
func badBuild(err error) error { return classified{err} }

type classified struct{ err error }

func (c classified) Error() string   { return c.err.Error() }
func (c classified) Unwrap() []error { return []error{c.err, ErrBadBuild} }

// HostArch is the linux arch name of the running binary ("amd64", "arm64", "arm").
func HostArch() string { return runtime.GOARCH }

// Fetch downloads the files of ref's channel needed to install it for arch
// into dir (which the caller owns) and verifies each against SHA256SUMS.
// On any error every file written to dir by this call is removed; once
// release.json has been read and verified, the returned Fetched still carries
// its Release, so callers can name the build that failed.
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
		return Fetched{}, badBuild(fmt.Errorf("%s%s: %w", prefix, SumsFile, err))
	}

	asset := ref.Asset()
	binName := layout.BinaryFile(asset, arch)
	if _, ok := sm[binName]; !ok {
		if !hasPrefix(sm, asset+"-linux-") {
			return Fetched{}, badBuild(fmt.Errorf("no asset %q in %s/%s", asset, ref.Project, channel))
		}
		return Fetched{}, badBuild(fmt.Errorf("no linux/%s build", arch))
	}

	var written []string
	defer func() {
		if err != nil {
			for _, p := range written {
				os.Remove(p)
			}
		}
	}()

	// A file that does not verify is a bad build only if SHA256SUMS still
	// reads as it did: a publish landing mid-fetch replaces it, and the
	// next try will find a consistent channel.
	unverified := func(verr error) error {
		now, err := readAll(ctx, st, prefix+SumsFile)
		switch {
		case err != nil:
			return fmt.Errorf("%w; reading %s%s again: %w", verr, prefix, SumsFile, err)
		case !bytes.Equal(now, sumsRaw):
			return fmt.Errorf("%s%s changed during the download; try again", prefix, SumsFile)
		}
		h := sha256.Sum256(sumsRaw)
		return &MismatchError{Err: verr, SumsHash: hex.EncodeToString(h[:])}
	}

	f := Fetched{Dir: dir}
	relRaw, err := readAll(ctx, st, prefix+ReleaseFile)
	if err != nil {
		return Fetched{}, fmt.Errorf("%s: %w", ReleaseFile, err)
	}
	if err := sm.Verify(ReleaseFile, bytes.NewReader(relRaw)); err != nil {
		return Fetched{}, unverified(err)
	}
	if err := json.Unmarshal(relRaw, &f.Release); err != nil {
		return Fetched{}, badBuild(fmt.Errorf("%s: %w", ReleaseFile, err))
	}
	if f.Release.Project != ref.Project {
		return Fetched{}, badBuild(fmt.Errorf("%s%s names project %q, want %q", prefix, ReleaseFile, f.Release.Project, ref.Project))
	}

	fetch := func(name string, mode os.FileMode) (string, error) {
		path := filepath.Join(dir, name)
		written = append(written, path)
		if err := download(ctx, st, prefix+name, path, mode, sm, name); err != nil {
			var bad errUnverified
			if errors.As(err, &bad) {
				return "", unverified(bad.err)
			}
			return "", err
		}
		return path, nil
	}

	if f.Binary, err = fetch(binName, 0o755); err != nil {
		return Fetched{Release: f.Release}, err
	}
	if _, ok := sm[layout.UnitFile(asset)]; ok {
		if f.Unit, err = fetch(layout.UnitFile(asset), 0o644); err != nil {
			return Fetched{Release: f.Release}, err
		}
	}
	if _, ok := sm[layout.EnvExampleFile(asset)]; ok {
		if f.EnvExample, err = fetch(layout.EnvExampleFile(asset), 0o644); err != nil {
			return Fetched{Release: f.Release}, err
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
	src := &errTrackingReader{r: io.TeeReader(body, out)}
	verr := sm.Verify(name, src)
	cerr := out.Close()
	switch {
	case src.err != nil:
		return verr // the store or the disk failed mid-transfer; the build is not at fault
	case verr != nil:
		return errUnverified{verr}
	case cerr != nil:
		return cerr // a local write error on close is not the build's fault either
	}
	return os.Chmod(path, mode) // OpenFile's mode is subject to umask
}

// errUnverified is a download that did not match its checksum; Fetch
// decides whether that makes a bad build.
type errUnverified struct{ err error }

func (e errUnverified) Error() string { return e.err.Error() }

// errTrackingReader remembers the first error its source returned, other
// than io.EOF, so a failed transfer can be told from a failed checksum.
type errTrackingReader struct {
	r   io.Reader
	err error
}

func (e *errTrackingReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}

// hasPrefix reports whether any file listed in sm starts with prefix.
func hasPrefix(sm sums.Sums, prefix string) bool {
	for name := range sm {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func readAll(ctx context.Context, st store.Store, key string) ([]byte, error) {
	body, err := st.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}
