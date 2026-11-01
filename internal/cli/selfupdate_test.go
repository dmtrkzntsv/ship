package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// selfUpdateEnv publishes ship builds to channels of the fake store and
// points the executable seam at a temp "old" binary, returned as exe.
func selfUpdateEnv(t *testing.T) (*fakeEnv, string) {
	t.Helper()
	env := useFakeApp(t)
	for _, c := range []struct{ version, channel, content string }{
		{"v2.0.0", "latest", "latest-binary"},
		{"v3.0.0-rc1", "dev", "dev-binary"},
	} {
		dir := buildDir(t, "ship", c.content)
		require.NoError(t, release.Publish(context.Background(), env.store, release.PublishOptions{
			Project: "ship", Dir: dir, Version: c.version, Channels: []string{c.channel},
			Now: time.Now, Out: io.Discard,
		}))
	}
	exe := filepath.Join(t.TempDir(), "ship")
	require.NoError(t, os.WriteFile(exe, []byte("old-binary"), 0o755))
	old := executable
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = old })
	return env, exe
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func TestSelfUpdateDefaultsToLatest(t *testing.T) {
	env, exe := selfUpdateEnv(t)
	out, errOut, err := runCLI(t, "self-update")
	require.NoError(t, err)
	assert.Equal(t, "ship: "+Version+" -> v2.0.0\n", out)
	assert.Empty(t, errOut)
	assert.Equal(t, "latest-binary", readFile(t, exe))
	_, err = os.Stat(env.state.Path)
	assert.ErrorIs(t, err, os.ErrNotExist, "without --version nothing is remembered")
}

func TestSelfUpdateUsesRememberedChannel(t *testing.T) {
	env, exe := selfUpdateEnv(t)
	require.NoError(t, env.state.Save(&state.State{SelfChannel: "dev", Services: map[string]*state.Service{}}))
	out, _, err := runCLI(t, "self-update")
	require.NoError(t, err)
	assert.Equal(t, "ship: "+Version+" -> v3.0.0-rc1\n", out)
	assert.Equal(t, "dev-binary", readFile(t, exe))
}

func TestSelfUpdateUnreadableStateWarns(t *testing.T) {
	env, exe := selfUpdateEnv(t)
	require.NoError(t, os.WriteFile(env.state.Path, []byte("{not json"), 0o644))
	_, errOut, err := runCLI(t, "self-update")
	require.NoError(t, err)
	assert.Contains(t, errOut, "warning: parse state")
	assert.Contains(t, errOut, "; using latest")
	assert.Equal(t, "latest-binary", readFile(t, exe))
}

func TestSelfUpdateVersionIsRemembered(t *testing.T) {
	env, exe := selfUpdateEnv(t)
	installedState(t, env.state)
	_, _, err := runCLI(t, "self-update", "--version", "dev")
	require.NoError(t, err)
	assert.Equal(t, "dev-binary", readFile(t, exe))
	s, err := env.state.Load()
	require.NoError(t, err)
	assert.Equal(t, "dev", s.SelfChannel)
	assert.Contains(t, s.Services, "econumo", "the rest of the state is kept")

	_, _, err = runCLI(t, "self-update", "--version", "v2.0.0")
	require.NoError(t, err)
	assert.Equal(t, "latest-binary", readFile(t, exe))
	s, err = env.state.Load()
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", s.SelfChannel)
}

func TestSelfUpdateAlreadyCurrent(t *testing.T) {
	_, exe := selfUpdateEnv(t)
	old := Version
	Version = "v2.0.0"
	t.Cleanup(func() { Version = old })
	out, _, err := runCLI(t, "self-update")
	require.NoError(t, err)
	assert.Equal(t, "ship: already on v2.0.0\n", out)
	assert.Equal(t, "old-binary", readFile(t, exe))
}

func TestSelfUpdateErrors(t *testing.T) {
	t.Run("invalid channel", func(t *testing.T) {
		_, exe := selfUpdateEnv(t)
		_, _, err := runCLI(t, "self-update", "--version", "no/slash")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid version "no/slash"`)
		assert.Equal(t, "old-binary", readFile(t, exe))
	})
	t.Run("no arguments", func(t *testing.T) {
		selfUpdateEnv(t)
		_, _, err := runCLI(t, "self-update", "extra")
		assert.Error(t, err)
	})
	t.Run("executable unknown", func(t *testing.T) {
		selfUpdateEnv(t)
		executable = func() (string, error) { return "", errors.New("no exe") }
		_, _, err := runCLI(t, "self-update")
		assert.EqualError(t, err, "no exe")
	})
	t.Run("executable missing", func(t *testing.T) {
		selfUpdateEnv(t)
		executable = func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil }
		_, _, err := runCLI(t, "self-update")
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("store unavailable", func(t *testing.T) {
		env, _ := selfUpdateEnv(t)
		env.storeErr = errors.New("no creds")
		_, _, err := runCLI(t, "self-update")
		assert.EqualError(t, err, "no creds")
	})
	t.Run("channel not published", func(t *testing.T) {
		env, exe := selfUpdateEnv(t)
		_, _, err := runCLI(t, "self-update", "--version", "v9.9.9")
		require.Error(t, err)
		assert.Equal(t, "old-binary", readFile(t, exe))
		s, lerr := env.state.Load()
		require.NoError(t, lerr)
		assert.Empty(t, s.SelfChannel, "a failed update remembers nothing")
	})
	t.Run("state locked", func(t *testing.T) {
		env, exe := selfUpdateEnv(t)
		unlock, err := env.state.Lock()
		require.NoError(t, err)
		defer unlock()
		_, _, err = runCLI(t, "self-update", "--version", "dev")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `updated, but could not remember channel "dev"`)
		assert.Equal(t, "dev-binary", readFile(t, exe))
	})
	t.Run("state unreadable", func(t *testing.T) {
		env, _ := selfUpdateEnv(t)
		require.NoError(t, os.WriteFile(env.state.Path, []byte("{not json"), 0o644))
		_, _, err := runCLI(t, "self-update", "--version", "dev")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse state")
	})
	t.Run("state not writable", func(t *testing.T) {
		env, exe := selfUpdateEnv(t)
		dir := filepath.Dir(env.state.Path)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "state.lock"), nil, 0o644))
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		_, _, err := runCLI(t, "self-update", "--version", "dev")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `updated, but could not remember channel "dev"`)
		assert.Equal(t, "dev-binary", readFile(t, exe))
	})
}
