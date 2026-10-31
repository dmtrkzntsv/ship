package host_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/layout"
)

var (
	sqlRef = layout.Ref{Project: "twillingate-sql"}
	twRef  = layout.Ref{Project: "twillingate"}
)

// installTwo installs twillingate-sql and twillingate into service tw.
func installTwo(t *testing.T) (*host.Host, *host.FakeSys, *bytes.Buffer) {
	t.Helper()
	h, fs := newHost(t)
	out := &bytes.Buffer{}
	h.Out = out
	ctx := context.Background()
	require.NoError(t, h.Install(ctx, target(fetched(t, ""))))
	f := fetched(t, "")
	require.NoError(t, h.Install(ctx, host.Target{Service: "tw", Ref: twRef, Unit: "twillingate", Fetched: f}))
	fs.Calls = nil
	return h, fs, out
}

func TestUninstallOneOfTwo(t *testing.T) {
	h, fs, _ := installTwo(t)
	p := h.Paths
	require.NoError(t, os.WriteFile(p.Bin("tw", "twillingate-sql")+".prev", []byte("p"), 0o755))
	require.NoError(t, os.WriteFile(p.UnitBackup("tw", "twillingate-sql"), []byte("p"), 0o644))

	require.NoError(t, h.Uninstall(context.Background(), "tw", sqlRef, "twillingate-sql", false, false, true))

	for _, gone := range []string{p.UnitPath("twillingate-sql"), p.Bin("tw", "twillingate-sql"), p.Bin("tw", "twillingate-sql") + ".prev",
		p.UnitBackup("tw", "twillingate-sql")} {
		assert.NoFileExists(t, gone)
	}
	// the env file holds hand-written config and secrets: kept without --purge
	for _, kept := range []string{p.BinDir("tw"), p.EnvDir("tw"), p.DataDir("tw"), p.Tmpfiles("tw"), p.UnitPath("twillingate"),
		p.EnvFile("tw", "twillingate"), p.EnvFile("tw", "twillingate-sql")} {
		assert.True(t, pathExists(kept), kept)
	}
	assert.True(t, fs.UserExists("tw"))
	assert.Equal(t, []string{
		"systemctl stop twillingate-sql", "systemctl disable twillingate-sql",
		"DaemonReload", "systemctl reset-failed twillingate-sql",
	}, fs.Calls)
}

func TestUninstallOneOfTwoPurgeRemovesEnvFile(t *testing.T) {
	h, _, _ := installTwo(t)
	p := h.Paths
	require.NoError(t, h.Uninstall(context.Background(), "tw", sqlRef, "twillingate-sql", false, true, true))
	assert.NoFileExists(t, p.EnvFile("tw", "twillingate-sql"))
	assert.FileExists(t, p.EnvFile("tw", "twillingate"))
	assert.DirExists(t, p.DataDir("tw"))
}

func uninstallBoth(t *testing.T, purge bool) (*host.Host, *host.FakeSys, *bytes.Buffer) {
	h, fs, out := installTwo(t)
	require.NoError(t, os.MkdirAll(h.Paths.RunDir("tw"), 0o750)) // systemd-tmpfiles creates it on a real host
	ctx := context.Background()
	require.NoError(t, h.Uninstall(ctx, "tw", sqlRef, "twillingate-sql", false, false, true))
	require.NoError(t, h.Uninstall(ctx, "tw", twRef, "twillingate", true, purge, true))
	return h, fs, out
}

func TestUninstallLastKeepsDataAndUser(t *testing.T) {
	h, fs, out := uninstallBoth(t, false)
	p := h.Paths
	require.NoError(t, os.WriteFile(p.EnvDir("tw")+"/issuer.pem", []byte("key"), 0o400))
	for _, gone := range []string{p.BinDir("tw"), p.RunDir("tw"), p.Tmpfiles("tw")} {
		assert.False(t, pathExists(gone), gone)
	}
	assert.DirExists(t, p.DataDir("tw"))
	assert.DirExists(t, p.EnvDir("tw"))
	assert.FileExists(t, p.EnvFile("tw", "twillingate"))
	assert.FileExists(t, p.EnvFile("tw", "twillingate-sql"))
	assert.True(t, fs.UserExists("tw"))
	assert.NotContains(t, fs.Calls, "DeleteUser tw")
	assert.Contains(t, out.String(), "Kept /etc/tw and /var/lib/tw (user tw kept); remove with --purge")
}

func TestUninstallLastPurge(t *testing.T) {
	h, fs, out := uninstallBoth(t, true)
	assert.NoDirExists(t, h.Paths.DataDir("tw"))
	assert.NoDirExists(t, h.Paths.EnvDir("tw"))
	assert.False(t, fs.UserExists("tw"))
	assert.Contains(t, fs.Calls, "DeleteUser tw")
	assert.NotContains(t, out.String(), "Kept")
}

func TestReinstallKeepsEnvFile(t *testing.T) {
	h, fs, _ := uninstallBoth(t, false)
	p := h.Paths
	require.NoError(t, os.WriteFile(p.EnvFile("tw", "twillingate-sql"), []byte("KEEP=1\n"), 0o640))
	fs.Calls = nil

	require.NoError(t, h.Install(context.Background(), target(fetched(t, "NEW=1\n"))))
	for _, c := range fs.Calls {
		assert.NotContains(t, c, "CreateUser")
	}
	b, err := os.ReadFile(p.EnvFile("tw", "twillingate-sql"))
	require.NoError(t, err)
	assert.Equal(t, "KEEP=1\n", string(b))
}

func TestUninstallHalfInstalled(t *testing.T) {
	h, fs, _ := installTwo(t)
	p := h.Paths
	require.NoError(t, os.Remove(p.UnitPath("twillingate-sql")))
	require.NoError(t, os.Remove(p.EnvFile("tw", "twillingate-sql")))
	fs.Calls = nil

	require.NoError(t, h.Uninstall(context.Background(), "tw", sqlRef, "twillingate-sql", false, false, true))
	assert.NoFileExists(t, p.Bin("tw", "twillingate-sql"))
}

func TestUninstallEverythingMissing(t *testing.T) {
	h, _ := newHost(t)
	require.NoError(t, h.Uninstall(context.Background(), "tw", sqlRef, "twillingate-sql", true, true, true))
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestUninstallLastPurgeKeepsUserShipDidNotCreate(t *testing.T) {
	h, fs, out := installTwo(t)
	ctx := context.Background()
	require.NoError(t, h.Uninstall(ctx, "tw", sqlRef, "twillingate-sql", false, false, false))
	require.NoError(t, h.Uninstall(ctx, "tw", twRef, "twillingate", true, true, false))
	assert.NoDirExists(t, h.Paths.DataDir("tw"))
	assert.NotContains(t, fs.Calls, "DeleteUser tw")
	assert.True(t, fs.UserExists("tw"))
	assert.Empty(t, out.String())
}
