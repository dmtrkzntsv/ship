package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/store"
)

// captureStd runs f with os.Args set to args and os.Stdout/os.Stderr
// redirected to files, and returns f's result and what was written.
func captureStd(t *testing.T, args []string, f func() int) (code int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	open := func(name string) *os.File {
		fh, err := os.Create(filepath.Join(dir, name))
		require.NoError(t, err)
		return fh
	}
	o, e := open("out"), open("err")
	oldArgs, oldOut, oldErr := os.Args, os.Stdout, os.Stderr
	os.Args, os.Stdout, os.Stderr = args, o, e
	code = f()
	os.Args, os.Stdout, os.Stderr = oldArgs, oldOut, oldErr
	require.NoError(t, o.Close())
	require.NoError(t, e.Close())
	return code, readFile(t, o.Name()), readFile(t, e.Name())
}

func TestExecute(t *testing.T) {
	code, out, errOut := captureStd(t, []string{"ship", "version"}, Execute)
	assert.Equal(t, 0, code)
	assert.Equal(t, "ship "+Version+"\n", out)
	assert.Empty(t, errOut)

	code, out, errOut = captureStd(t, []string{"ship", "no-such-command"}, Execute)
	assert.Equal(t, 1, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, `unknown command "no-such-command"`)
}

func TestRequireLinuxRootRefusesNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	assert.EqualError(t, requireLinuxRoot("install"), "ship install: must run as root (try: sudo ship …)")
	assert.NoError(t, requireLinux("logs"), "tests run on Linux")
}

// TestRootCommandsValidateBeforeRootCheck: argument and flag errors are
// reported before the root check, so a non-root user sees them too.
func TestRootCommandsValidateBeforeRootCheck(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"install"}, "accepts 1 arg(s), received 0"},
		{[]string{"install", "a", "b", "--version", "dev"}, "accepts 1 arg(s), received 2"},
		{[]string{"update"}, "accepts 1 arg(s), received 0"},
		{[]string{"rollback", "a", "b"}, "accepts 1 arg(s), received 2"},
		{[]string{"uninstall"}, "accepts 1 arg(s), received 0"},
		{[]string{"start"}, "accepts 1 arg(s), received 0"},
		{[]string{"stop", "a", "b"}, "accepts 1 arg(s), received 2"},
		{[]string{"restart"}, "accepts 1 arg(s), received 0"},
		{[]string{"update", "econumo", "--bogus"}, "unknown flag: --bogus"},
		{[]string{"uninstall", "econumo", "--purge=maybe"}, "invalid argument"},
		{[]string{"run", "a", "b", "--", "x"}, "only the <ref> may come before --"},
		{[]string{"run"}, "requires a <ref> argument"},
		{[]string{"logs"}, "accepts 1 arg(s), received 0"},
		{[]string{"status", "extra"}, `unknown command "extra"`},
	} {
		_, _, err := runCLI(t, c.args...)
		require.Error(t, err, c.args)
		assert.Contains(t, err.Error(), c.want, c.args)
		assert.NotContains(t, err.Error(), "must run as root", c.args)
	}
}

func TestRootCommandsRefuseNonRootWithAllFlags(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, args := range [][]string{
		{"install", "econumo", "--version", "dev", "--service", "econumo-beta", "--cf-zone", "z"},
		{"update", "econumo", "--version", "latest", "--cf-zone", "z", "--purge-cache"},
		{"uninstall", "econumo", "--purge", "--purge-cache"},
		{"rollback", "econumo", "--service", "econumo-beta"},
	} {
		_, _, err := runCLI(t, args...)
		require.Error(t, err, args)
		assert.EqualError(t, err, "ship "+args[0]+": must run as root (try: sudo ship …)", args)
	}
}

func TestRefreshHelpersSkipWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	// Not root: neither helper may touch the store (a nil store would panic).
	refreshListing(t.Context(), nil)
	env := useFakeApp(t)
	a, err := newApp(io.Discard, io.Discard)
	require.NoError(t, err)
	called := false
	a.NewStore = func() (store.Store, error) { called = true; return env.store, nil }
	refreshAfter(NewRoot(io.Discard, io.Discard), a)
	assert.False(t, called, "non-root runs do not refresh the listing cache")
}

func TestCompletionStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"SHIP_R2_ENDPOINT", "SHIP_R2_ACCESS_KEY_ID", "SHIP_R2_SECRET_ACCESS_KEY", "SHIP_R2_BUCKET"} {
		t.Setenv(k, "")
	}
	cfgDir := filepath.Join(home, ".config", "ship")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	cfgFile := filepath.Join(cfgDir, "config.toml")

	require.NoError(t, os.WriteFile(cfgFile, []byte("not = [toml"), 0o600))
	_, err := completionStore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse "+cfgFile)

	require.NoError(t, os.WriteFile(cfgFile, []byte("[r2]\nbucket = \"b\"\n"), 0o600))
	_, err = completionStore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R2 endpoint is not set")

	t.Setenv("SHIP_R2_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("SHIP_R2_ACCESS_KEY_ID", "k")
	t.Setenv("SHIP_R2_SECRET_ACCESS_KEY", "s")
	st, err := completionStore()
	require.NoError(t, err)
	assert.NotNil(t, st)
}
