package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/config"
	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
)

// fakeEnv is what the newApp seam hands to commands: an in-memory store, a
// state file in a temp dir and a fake host.
type fakeEnv struct {
	store    *storetest.Mem
	state    state.File
	sys      *host.FakeSys
	cfg      config.Config
	storeErr error
	exec     [][]string
}

// useFakeApp replaces newApp and execFn for the test.
func useFakeApp(t *testing.T) *fakeEnv {
	t.Helper()
	env := &fakeEnv{
		store: storetest.NewMem(),
		state: state.File{Path: filepath.Join(t.TempDir(), "state.json")},
		sys:   &host.FakeSys{},
	}
	oldA, oldE := newApp, execFn
	t.Cleanup(func() { newApp, execFn = oldA, oldE })
	newApp = func(out, errOut io.Writer) (*app.App, error) {
		a := &app.App{
			Cfg: env.cfg, Out: out, Err: errOut, State: env.state,
			Host: &host.Host{Sys: env.sys, Out: out},
			Now:  time.Now,
		}
		a.NewStore = func() (store.Store, error) {
			if env.storeErr != nil {
				return nil, env.storeErr
			}
			return env.store, nil
		}
		return a, nil
	}
	execFn = func(argv []string) error { env.exec = append(env.exec, argv); return nil }
	return env
}

// runCLI executes the root command with args and returns stdout and stderr.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(&out, &errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// buildDir writes a publishable folder holding binaries for every arch.
func buildDir(t *testing.T, project, content string) string {
	t.Helper()
	dir := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, project+"-linux-"+arch), []byte(content), 0o644))
	}
	return dir
}

func TestPublishThenVersions(t *testing.T) {
	env := useFakeApp(t)

	out, _, err := runCLI(t, "publish", "econumo", "--path", buildDir(t, "econumo", "one"), "--version", "v1.0.0")
	require.NoError(t, err)
	assert.Contains(t, out, "published econumo/v1.0.0/")

	out, _, err = runCLI(t, "publish", "econumo", "--path", buildDir(t, "econumo", "two"), "--version", "v1.1.0", "--channels", "latest")
	require.NoError(t, err)
	assert.Contains(t, out, "published econumo/v1.1.0/")
	assert.Contains(t, out, "updated channel econumo/latest/")
	assert.Equal(t, []byte("two"), env.store.Objects["econumo/latest/econumo-linux-amd64"].Data)

	out, _, err = runCLI(t, "versions", "econumo")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 4, out)
	assert.Equal(t, []string{"CHANNEL", "VERSION", "PUBLISHED", "SIZE"}, strings.Fields(lines[0]))
	channels := map[string][]string{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		channels[f[0]] = f
	}
	require.Contains(t, channels, "latest")
	require.Contains(t, channels, "v1.0.0")
	require.Contains(t, channels, "v1.1.0")
	assert.Equal(t, "v1.1.0", channels["latest"][1], "latest points to v1.1.0")
	assert.Equal(t, "v1.0.0", channels["v1.0.0"][1])
	assert.False(t, strings.HasPrefix(channels["v1.0.0"][2], "~"), "release.json dates are exact")
}

func TestPublishBucketOverride(t *testing.T) {
	env := useFakeApp(t)
	var bucket string
	oldA := newApp
	newApp = func(out, errOut io.Writer) (*app.App, error) {
		a, err := oldA(out, errOut)
		if err != nil {
			return nil, err
		}
		inner := a.NewStore
		a.NewStore = func() (store.Store, error) { bucket = a.Cfg.R2.Bucket; return inner() }
		return a, nil
	}
	env.cfg.R2.Bucket = "from-config"
	_, _, err := runCLI(t, "publish", "econumo", "--path", buildDir(t, "econumo", "x"), "--channels", "dev", "--bucket", "other")
	require.NoError(t, err)
	assert.Equal(t, "other", bucket)
	assert.Contains(t, env.store.Objects, "econumo/dev/econumo-linux-amd64")
}

func TestPublishErrors(t *testing.T) {
	env := useFakeApp(t)
	_, _, err := runCLI(t, "publish", "econumo", "--path", buildDir(t, "econumo", "x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to publish")

	env.storeErr = errors.New("no creds")
	_, _, err = runCLI(t, "publish", "econumo", "--path", buildDir(t, "econumo", "x"), "--channels", "dev")
	assert.EqualError(t, err, "no creds")
}

func TestVersionsEstimatedDateAndMissingRelease(t *testing.T) {
	env := useFakeApp(t)
	env.store.Objects["econumo/v0.9/econumo-linux-amd64"] = storetest.MemObject{Data: make([]byte, 3*1024*1024)}

	out, _, err := runCLI(t, "versions", "econumo")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 2, out)
	f := strings.Fields(lines[1])
	assert.Equal(t, "v0.9", f[0])
	assert.Equal(t, "-", f[1], "no release.json: no version")
	assert.True(t, strings.HasPrefix(f[2], "~"), "date estimated from LastModified: %q", lines[1])
	assert.True(t, strings.HasSuffix(lines[1], "3.0 MiB"), lines[1])
}

func TestVersionsErrors(t *testing.T) {
	env := useFakeApp(t)
	_, _, err := runCLI(t, "versions", "Bad/Ref/x")
	var ue *usageError
	require.ErrorAs(t, err, &ue)
	assert.Contains(t, err.Error(), "invalid ref")

	_, _, err = runCLI(t, "versions")
	assert.Error(t, err, "a project is required")

	env.storeErr = errors.New("no creds")
	_, _, err = runCLI(t, "versions", "econumo")
	assert.EqualError(t, err, "no creds")
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		0:                   "0 B",
		1023:                "1023 B",
		1024:                "1.0 KiB",
		1536:                "1.5 KiB",
		5 * 1024 * 1024:     "5.0 MiB",
		3 << 30:             "3.0 GiB",
		2 << 40:             "2.0 TiB",
		1024*1024*1024 - 1:  "1024.0 MiB",
		1024*1024 + 1024*10: "1.0 MiB",
	} {
		assert.Equal(t, want, humanSize(n), n)
	}
}

func publishVersions(t *testing.T, st *storetest.Mem, project string, versions ...string) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, v := range versions {
		now := base.Add(time.Duration(i) * 24 * time.Hour)
		require.NoError(t, release.Publish(context.Background(), st, release.PublishOptions{
			Project: project, Dir: buildDir(t, project, v), Version: v,
			Now: func() time.Time { return now }, Out: io.Discard,
		}))
	}
}

func TestPruneDryRunAndYes(t *testing.T) {
	env := useFakeApp(t)
	publishVersions(t, env.store, "econumo", "v1", "v2", "v3")

	out, _, err := runCLI(t, "prune", "econumo", "--keep", "1")
	require.NoError(t, err)
	assert.Contains(t, out, "would delete econumo/v1")
	assert.Contains(t, out, "would delete econumo/v2")
	assert.NotContains(t, out, "econumo/v3")
	assert.Contains(t, out, "re-run with --yes to delete")
	assert.Contains(t, env.store.Objects, "econumo/v1/econumo-linux-amd64", "dry run deletes nothing")

	out, _, err = runCLI(t, "prune", "econumo", "--keep", "1", "--older-than", "1d", "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "freed")
	assert.NotContains(t, env.store.Objects, "econumo/v1/econumo-linux-amd64")
	assert.NotContains(t, env.store.Objects, "econumo/v2/econumo-linux-amd64")
	assert.Contains(t, env.store.Objects, "econumo/v3/econumo-linux-amd64")

	out, _, err = runCLI(t, "prune", "econumo", "--keep", "1")
	require.NoError(t, err)
	assert.Equal(t, "nothing to prune\n", out)
}

func TestPruneValidation(t *testing.T) {
	env := useFakeApp(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"prune", "Bad Name"}, "invalid project name Bad Name"},
		{[]string{"prune", "econumo", "--older-than", "soon"}, `invalid --older-than "soon"`},
		{[]string{"prune", "econumo", "--older-than", "-3d"}, `invalid --older-than "-3d"`},
		{[]string{"prune", "econumo", "--older-than", "-1h"}, `invalid --older-than "-1h"`},
	} {
		_, _, err := runCLI(t, c.args...)
		var ue *usageError
		require.ErrorAs(t, err, &ue, c.args)
		assert.EqualError(t, err, c.want, c.args)
	}

	env.storeErr = errors.New("no creds")
	_, _, err := runCLI(t, "prune", "econumo")
	assert.EqualError(t, err, "no creds")
}

func TestNewAppErrorIsReturned(t *testing.T) {
	useFakeApp(t)
	newApp = func(io.Writer, io.Writer) (*app.App, error) { return nil, errors.New("bad config") }
	for _, args := range [][]string{
		{"publish", "econumo", "--path", "."},
		{"versions", "econumo"},
		{"prune", "econumo"},
		{"status"},
		{"logs", "econumo"},
		{"self-update"},
	} {
		_, _, err := runCLI(t, args...)
		assert.EqualError(t, err, "bad config", args)
	}
}

func installedState(t *testing.T, sf state.File) {
	t.Helper()
	s, err := sf.Load()
	require.NoError(t, err)
	s.Service("econumo").Refs["econumo"] = &state.RefState{Unit: "econumo", Channel: "latest", Version: "v1"}
	s.Service("econumo-beta").Refs["econumo"] = &state.RefState{Unit: "econumo-beta", Channel: "dev", Version: "v2"}
	require.NoError(t, sf.Save(s))
}

func TestStatus(t *testing.T) {
	env := useFakeApp(t)
	out, _, err := runCLI(t, "status")
	require.NoError(t, err)
	assert.Equal(t, "no services installed\n", out)

	installedState(t, env.state)
	env.sys.Active = map[string]bool{"econumo": true}
	out, _, err = runCLI(t, "status")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 3, out)
	assert.Equal(t, []string{"econumo", "econumo", "econumo", "latest", "v1", "active"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"econumo-beta", "econumo", "econumo-beta", "dev", "v2", "inactive"}, strings.Fields(lines[2]))

	out, _, err = runCLI(t, "status", "--service", "econumo-beta")
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimRight(out, "\n"), "\n"), 2)

	_, _, err = runCLI(t, "status", "--service", "nope")
	assert.EqualError(t, err, "service nope is not installed")
}

func TestLogsExecsJournalctl(t *testing.T) {
	env := useFakeApp(t)
	installedState(t, env.state)

	_, _, err := runCLI(t, "logs", "econumo", "--service", "econumo-beta", "-n", "20")
	require.NoError(t, err)
	_, _, err = runCLI(t, "logs", "econumo", "--no-follow")
	require.NoError(t, err)
	require.Len(t, env.exec, 2)
	assert.Equal(t, []string{"journalctl", "-u", "econumo-beta.service", "-n", "20", "-f"}, env.exec[0])
	assert.Equal(t, []string{"journalctl", "-u", "econumo.service", "-n", "100"}, env.exec[1])

	_, _, err = runCLI(t, "logs", "other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
	assert.Len(t, env.exec, 2, "nothing is run for an unknown ref")
}

func TestExecArgvMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := execArgv([]string{"no-such-binary-for-ship"})
	require.Error(t, err)
	assert.ErrorIs(t, err, exec.ErrNotFound)
}

func TestGitCommit(t *testing.T) {
	head := gitCommit()
	assert.Regexp(t, `^[0-9a-f]{40,64}$`, head, "the test runs inside the ship checkout")

	t.Chdir(t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(mustGetwd(t)))
	assert.Empty(t, gitCommit(), "outside a repository there is no commit")
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}

// listFailStore is a Mem whose ListDirs fails.
type listFailStore struct{ *storetest.Mem }

func (listFailStore) ListDirs(context.Context, string) ([]string, error) {
	return nil, errors.New("list denied")
}

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

func TestVersionsListAndWriteErrors(t *testing.T) {
	env := useFakeApp(t)
	publishVersions(t, env.store, "econumo", "v1")

	root := NewRoot(failWriter{}, io.Discard)
	root.SetArgs([]string{"versions", "econumo"})
	assert.EqualError(t, root.Execute(), "stdout closed")

	inner := newApp
	newApp = func(out, errOut io.Writer) (*app.App, error) {
		a, err := inner(out, errOut)
		if err == nil {
			a.NewStore = func() (store.Store, error) { return listFailStore{env.store}, nil }
		}
		return a, err
	}
	_, _, err := runCLI(t, "versions", "econumo")
	assert.EqualError(t, err, "list denied")
}

func TestRefreshListingToKeepsCacheOnListError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "listing.json")
	refreshListingTo(context.Background(), listFailStore{storetest.NewMem()}, p)
	_, err := os.Stat(p)
	assert.ErrorIs(t, err, os.ErrNotExist, "a failed refresh writes no cache")
}
