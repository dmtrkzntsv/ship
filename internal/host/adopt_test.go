package host_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/layout"
)

const scriptUnit = "[Service]\nEnvironmentFile=/etc/tw/env\nExecStart=/opt/tw/twillingate-sql\n"

// legacyHost lays out service tw as the old deploy script left it for
// project twillingate-sql.
func legacyHost(t *testing.T) (*host.Host, *host.FakeSys) {
	t.Helper()
	h, fs := newHost(t)
	p := h.Paths
	for path, content := range map[string]string{
		p.UnitPath("tw"):                     scriptUnit,
		p.Bin("tw", "twillingate-sql"):       "script",
		filepath.Join(p.EnvDir("tw"), "env"): "A=1",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return h, fs
}

func adoptTarget(t *testing.T) host.Target {
	return host.Target{Service: "tw", Ref: layout.Ref{Project: "twillingate-sql"}, Unit: "tw", Fetched: newRelease(t, "new", "unit-new")}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out[p] = read(t, p)
		return nil
	}))
	return out
}

func TestHostAdoptSwaps(t *testing.T) {
	h, fs := legacyHost(t)
	p := h.Paths
	require.NoError(t, h.Adopt(context.Background(), adoptTarget(t)))

	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, "script", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
	assert.Contains(t, read(t, p.UnitPath("tw")), "EnvironmentFile=/etc/tw/twillingate-sql.env")
	assert.Equal(t, scriptUnit, read(t, p.UnitBackup("tw", "tw")))
	assert.Equal(t, "A=1", read(t, p.EnvFile("tw", "twillingate-sql")))
	assert.NoFileExists(t, filepath.Join(p.EnvDir("tw"), "env"))
	assert.Equal(t, 1, countCalls(fs.Calls, "systemctl restart tw"))
	// /etc/S gets the group a ship install gives it
	assert.Contains(t, fs.Calls, "Chown "+p.EnvDir("tw")+" root:tw")
}

func TestHostAdoptKeepsExistingTmpfiles(t *testing.T) {
	h, fs := legacyHost(t)
	p := h.Paths
	require.NoError(t, os.MkdirAll(filepath.Dir(p.Tmpfiles("tw")), 0o755))
	require.NoError(t, os.WriteFile(p.Tmpfiles("tw"), []byte("custom\n"), 0o644))
	fs.ActiveAfterRestart = activeUnlessBin(t, h, "new")

	require.Error(t, h.Adopt(context.Background(), adoptTarget(t)))
	assert.Equal(t, "custom\n", read(t, p.Tmpfiles("tw")), "an entry adopt did not create stays")
	assert.Contains(t, fs.Calls, "CreateTmpfiles "+p.Tmpfiles("tw"))
}

func TestHostAdoptRestartFailureRestoresAll(t *testing.T) {
	h, fs := legacyHost(t)
	before := snapshot(t, h.Paths.Root)
	fs.RestartErr = errors.New("exit status 1")

	err := h.Adopt(context.Background(), adoptTarget(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adopt failed")
	assert.Equal(t, before, snapshot(t, h.Paths.Root))
	assert.GreaterOrEqual(t, countCalls(fs.Calls, "DaemonReload"), 2)
}

func TestHostAdoptRefusesBeforeChanges(t *testing.T) {
	t.Run("verify fails", func(t *testing.T) {
		h, fs := legacyHost(t)
		before := snapshot(t, h.Paths.Root)
		fs.VerifyErr = errors.New("bad unit")
		require.Error(t, h.Adopt(context.Background(), adoptTarget(t)))
		assert.Equal(t, before, snapshot(t, h.Paths.Root))
		assert.False(t, hasCallPrefix(fs.Calls, "systemctl"))
	})
	t.Run("new env file exists", func(t *testing.T) {
		h, fs := legacyHost(t)
		require.NoError(t, os.WriteFile(h.Paths.EnvFile("tw", "twillingate-sql"), []byte("B=2"), 0o640))
		before := snapshot(t, h.Paths.Root)
		err := h.Adopt(context.Background(), adoptTarget(t))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
		assert.Equal(t, before, snapshot(t, h.Paths.Root))
		assert.False(t, hasCallPrefix(fs.Calls, "systemctl"))
	})
	t.Run("no legacy layout", func(t *testing.T) {
		h, fs := newHost(t)
		require.Error(t, h.Adopt(context.Background(), adoptTarget(t)))
		assert.Empty(t, fs.Calls)
	})
}

// activeUnlessBin makes the unit inactive while tw's binary holds bad.
func activeUnlessBin(t *testing.T, h *host.Host, bad string) func(string) bool {
	return func(string) bool {
		b, err := os.ReadFile(h.Paths.Bin("tw", "twillingate-sql"))
		require.NoError(t, err)
		return string(b) != bad
	}
}

func TestHostAdoptTmpfilesCleanupFailureStillRestartsOldUnit(t *testing.T) {
	h, fs := legacyHost(t)
	p := h.Paths
	conf := p.Tmpfiles("tw")
	fs.ActiveAfterRestart = func(string) bool {
		// Turn the entry adopt wrote into a non-empty directory so its
		// removal fails after the old files are back.
		if b, err := os.ReadFile(p.Bin("tw", "twillingate-sql")); err == nil && string(b) == "new" {
			if fi, err := os.Lstat(conf); err == nil && !fi.IsDir() {
				require.NoError(t, os.Remove(conf))
				require.NoError(t, os.MkdirAll(filepath.Join(conf, "x"), 0o755))
			}
			return false
		}
		return true
	}

	err := h.Adopt(context.Background(), adoptTarget(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adopt failed, previous version restored")
	assert.Contains(t, err.Error(), "removing tmpfiles entry /etc/tmpfiles.d/ship-tw.conf failed")
	assert.NotContains(t, err.Error(), "restoring the previous binary and unit failed")
	assert.Equal(t, 2, countCalls(fs.Calls, "systemctl restart tw"))
	assert.Equal(t, "script", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, scriptUnit, read(t, p.UnitPath("tw")))
	assert.Equal(t, "A=1", read(t, filepath.Join(p.EnvDir("tw"), "env")))
}
