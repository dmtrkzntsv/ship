package host_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/release"
)

const restartCall = "systemctl restart twillingate-sql"

// newRelease writes a fake fetched release whose binary holds bin and whose
// unit template has an extra marker line, so tests can tell versions apart.
func newRelease(t *testing.T, bin, marker string) release.Fetched {
	t.Helper()
	f := fetched(t, "")
	f.Release.Version = bin
	require.NoError(t, os.WriteFile(f.Binary, []byte(bin), 0o755))
	require.NoError(t, os.WriteFile(f.Unit, []byte(sqlUnit+"# "+marker+"\n"), 0o644))
	return f
}

// installed returns a host with v1 of twillingate-sql installed in service tw.
func installed(t *testing.T) (*host.Host, *host.FakeSys) {
	t.Helper()
	h, fs := newHost(t)
	h.HealthWait, h.HealthTick = 30*time.Millisecond, 10*time.Millisecond
	require.NoError(t, h.Install(context.Background(), target(newRelease(t, "old", "unit-old"))))
	fs.Calls = nil
	return h, fs
}

func countCalls(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

// activeUnless makes the unit inactive while the installed binary holds bad.
func activeUnless(t *testing.T, h *host.Host, bad string) func(string) bool {
	return func(string) bool {
		b, err := os.ReadFile(h.Paths.Bin("tw", "twillingate-sql"))
		require.NoError(t, err)
		return string(b) != bad
	}
}

func TestUpdateSwapsAndKeepsPrev(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))

	require.NoError(t, h.Update(context.Background(), target(newRelease(t, "new", "unit-new"))))

	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Contains(t, read(t, p.UnitPath("twillingate-sql")), "# unit-new")
	assert.Contains(t, read(t, p.UnitPath("twillingate-sql")), "ExecStart=/opt/tw/twillingate-sql")
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
	assert.Equal(t, oldUnit, read(t, p.UnitBackup("tw", "twillingate-sql")))
	assert.Equal(t, os.FileMode(0o755), mode(t, p.Bin("tw", "twillingate-sql")))

	assert.Contains(t, fs.Calls, "DaemonReload")
	assert.Equal(t, 1, countCalls(fs.Calls, restartCall))
	for _, c := range fs.Calls {
		if strings.HasPrefix(c, "systemctl ") {
			assert.Equal(t, restartCall, c, "only this ref's unit may be touched")
		}
	}
}

func TestUpdateUnhealthyRollsBack(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))
	fs.ActiveAfterRestart = activeUnless(t, h, "new")
	fs.Journal = "panic: boom"

	err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twillingate-sql failed to come up")
	assert.Contains(t, err.Error(), "--- last log lines ---\npanic: boom")
	assert.Contains(t, fs.Calls, "JournalTail twillingate-sql 30")

	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.Equal(t, 2, countCalls(fs.Calls, restartCall))
	assert.GreaterOrEqual(t, countCalls(fs.Calls, "DaemonReload"), 2)
}

func TestUpdateRestartFailureRollsBack(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))
	fs.RestartErr = errors.New("exit status 1")
	fs.Journal = "bad config"

	err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.Contains(t, err.Error(), "bad config")
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.Equal(t, 2, countCalls(fs.Calls, restartCall))
}

func TestUpdateVerifyFailureChangesNothing(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))
	fs.VerifyErr = errors.New("bad unit")

	err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad unit")
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.NoFileExists(t, p.Bin("tw", "twillingate-sql")+".prev")
	assert.NoFileExists(t, p.UnitBackup("tw", "twillingate-sql"))
	assert.False(t, hasCallPrefix(fs.Calls, "systemctl"), "no restart: %v", fs.Calls)
	assertNoTemp(t, p.BinDir("tw"))
}

func TestUpdateUnitWriteFailureRestoresBinary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))
	unitDir := filepath.Dir(p.UnitPath("twillingate-sql"))
	require.NoError(t, os.Chmod(unitDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(unitDir, 0o755) })

	require.Error(t, h.Update(context.Background(), target(newRelease(t, "new", "unit-new"))))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.NoFileExists(t, p.Bin("tw", "twillingate-sql")+".prev")
	assert.False(t, hasCallPrefix(fs.Calls, "systemctl"), "no restart: %v", fs.Calls)
	assertNoTemp(t, p.BinDir("tw"))
}

func TestFailedUpdateKeepsRollbackTarget(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))
	require.NoError(t, h.Update(context.Background(), target(newRelease(t, "new", "unit-new"))))
	newUnit := read(t, p.UnitPath("twillingate-sql"))

	fs.ActiveAfterRestart = activeUnless(t, h, "newer")
	tg := target(newRelease(t, "newer", "unit-newer"))
	require.Error(t, h.Update(context.Background(), tg))
	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, newUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
	assert.Equal(t, oldUnit, read(t, p.UnitBackup("tw", "twillingate-sql")))
	assertNoTemp(t, p.BinDir("tw"))

	require.NoError(t, h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"), "stray temp file %s", e.Name())
	}
}

func TestUpdateBadTemplateTouchesNothing(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	f := newRelease(t, "new", "unit-new")
	require.NoError(t, os.WriteFile(f.Unit, []byte("ExecStart={{NOPE}}\n"), 0o644))

	require.Error(t, h.Update(context.Background(), target(f)))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.NoFileExists(t, p.Bin("tw", "twillingate-sql")+".prev")
	assert.Empty(t, fs.Calls)
}

func TestUpdateNotOnHost(t *testing.T) {
	h, fs := newHost(t)
	err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
	require.Error(t, err)
	assert.False(t, hasCallPrefix(fs.Calls, "systemctl"))
	assert.NoFileExists(t, h.Paths.Bin("tw", "twillingate-sql"))
}

func TestRollbackSwapsPrev(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	oldUnit := read(t, p.UnitPath("twillingate-sql"))
	tg := target(newRelease(t, "new", "unit-new"))
	require.NoError(t, h.Update(context.Background(), tg))
	newUnit := read(t, p.UnitPath("twillingate-sql"))
	fs.Calls = nil

	require.NoError(t, h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
	assert.Equal(t, newUnit, read(t, p.UnitBackup("tw", "twillingate-sql")))
	assert.Equal(t, os.FileMode(0o755), mode(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, os.FileMode(0o755), mode(t, p.Bin("tw", "twillingate-sql")+".prev"))
	assert.Equal(t, []string{"DaemonReload", restartCall}, fs.Calls[:2])
	assert.Contains(t, fs.Calls, "IsActive twillingate-sql")

	// A second rollback goes forward again.
	require.NoError(t, h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit))
	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, newUnit, read(t, p.UnitPath("twillingate-sql")))
}

func TestRollbackNothingToRollBack(t *testing.T) {
	h, fs := installed(t)
	tg := target(release.Fetched{})
	err := h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit)
	require.Error(t, err)
	assert.Equal(t, "nothing to roll back for twillingate-sql", err.Error())
	assert.Empty(t, fs.Calls)
}

func TestRollbackUnhealthyRestores(t *testing.T) {
	h, fs := installed(t)
	p := h.Paths
	tg := target(newRelease(t, "new", "unit-new"))
	require.NoError(t, h.Update(context.Background(), tg))
	newUnit := read(t, p.UnitPath("twillingate-sql"))
	fs.Calls = nil
	fs.ActiveAfterRestart = activeUnless(t, h, "old")

	err := h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to come up")
	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, newUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
	assert.Equal(t, 2, countCalls(fs.Calls, restartCall))
}

func TestWaitHealthy(t *testing.T) {
	h, fs := newHost(t)
	h.HealthWait, h.HealthTick = 30*time.Millisecond, 10*time.Millisecond
	fs.Active = map[string]bool{"u": true}
	require.NoError(t, h.WaitHealthy(context.Background(), "u"))
	assert.GreaterOrEqual(t, countCalls(fs.Calls, "IsActive u"), 3)

	// Goes down partway through the window.
	n := 0
	fs.ActiveAfterRestart = func(string) bool { n++; return n < 3 }
	fs.Journal = "line1\nline2"
	err := h.WaitHealthy(context.Background(), "u")
	require.Error(t, err)
	assert.Equal(t, "u failed to come up\n--- last log lines ---\nline1\nline2", err.Error())
	assert.Equal(t, 3, n)
}

func TestUpdateKeepsUnitDirLayout(t *testing.T) {
	h, _ := installed(t)
	require.NoError(t, h.Update(context.Background(), target(newRelease(t, "new", "unit-new"))))
	// No stray temp files next to the binary or unit.
	assertNoTemp(t, h.Paths.BinDir("tw"))
	assertNoTemp(t, filepath.Dir(h.Paths.UnitPath("x")))
}

// skipIfRoot skips tests that rely on directory permissions.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
}

// readOnly makes dir read-only until the test ends.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// assertUnchanged checks that the old pair is live and no .prev pair was made.
func assertUnchanged(t *testing.T, h *host.Host, oldUnit string) {
	t.Helper()
	p := h.Paths
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, oldUnit, read(t, p.UnitPath("twillingate-sql")))
	assert.NoFileExists(t, p.Bin("tw", "twillingate-sql")+".prev")
	assert.NoFileExists(t, p.UnitBackup("tw", "twillingate-sql"))
}

func TestUpdateFailsBeforeTouchingHost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h *host.Host, f *release.Fetched)
		want  string
	}{
		{"unreadable template", func(t *testing.T, _ *host.Host, f *release.Fetched) {
			require.NoError(t, os.Remove(f.Unit))
		}, "read unit template: "},
		{"no temp dir", func(t *testing.T, _ *host.Host, _ *release.Fetched) {
			t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		}, "no such file or directory"},
		{"missing binary", func(t *testing.T, _ *host.Host, f *release.Fetched) {
			require.NoError(t, os.Remove(f.Binary))
		}, "no such file or directory"},
		{"live unit missing", func(t *testing.T, h *host.Host, _ *release.Fetched) {
			require.NoError(t, os.Remove(h.Paths.UnitPath("twillingate-sql")))
		}, "twillingate-sql.service is missing; is twillingate-sql installed on this host?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := installed(t)
			oldUnit := read(t, h.Paths.UnitPath("twillingate-sql"))
			f := newRelease(t, "new", "unit-new")
			tc.setup(t, h, &f)

			err := h.Update(context.Background(), target(f))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, "old", read(t, h.Paths.Bin("tw", "twillingate-sql")))
			if tc.name != "live unit missing" {
				assertUnchanged(t, h, oldUnit)
			}
			assert.NoFileExists(t, h.Paths.Bin("tw", "twillingate-sql")+".prev")
			assert.False(t, hasCallPrefix(fs.Calls, "systemctl"), fs.Calls)
			assertNoTemp(t, h.Paths.BinDir("tw"))
		})
	}
}

func TestUpdateBackupFails(t *testing.T) {
	skipIfRoot(t)
	h, fs := installed(t)
	oldUnit := read(t, h.Paths.UnitPath("twillingate-sql"))
	readOnly(t, h.Paths.BinDir("tw"))

	err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "back up "+h.Paths.Bin("tw", "twillingate-sql")+": ")
	assertUnchanged(t, h, oldUnit)
	assert.False(t, hasCallPrefix(fs.Calls, "systemctl"), fs.Calls)
}

func TestUpdateRevertFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(fs *host.FakeSys)
		want  string
	}{
		{"daemon-reload fails again", func(fs *host.FakeSys) { fs.ReloadErr = errors.New("reload failed") },
			"update failed; previous binary and unit restored but daemon-reload failed (reload failed): daemon-reload: reload failed"},
		{"restart fails again", func(fs *host.FakeSys) { fs.RestartErr = errors.New("exit status 1") },
			"update failed; previous binary and unit restored but restarting twillingate-sql failed (exit status 1): " +
				"restart twillingate-sql: exit status 1\n--- last log lines ---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := installed(t)
			oldUnit := read(t, h.Paths.UnitPath("twillingate-sql"))
			tc.setup(fs)
			err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assertUnchanged(t, h, oldUnit)
			assertNoTemp(t, h.Paths.BinDir("tw"))
		})
	}
}

func TestUpdateRestoreFails(t *testing.T) {
	skipIfRoot(t)
	h, fs := installed(t)
	binDir := h.Paths.BinDir("tw")
	// The new pair fails its health check, and by then /opt/S has turned
	// read-only, so the old pair cannot be put back.
	fs.ActiveAfterRestart = func(string) bool {
		require.NoError(t, os.Chmod(binDir, 0o555))
		return false
	}
	t.Cleanup(func() { _ = os.Chmod(binDir, 0o755) })

	err := h.Update(context.Background(), target(newRelease(t, "new", "unit-new")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failed and restoring the previous binary and unit failed too (")
	assert.Contains(t, err.Error(), "twillingate-sql failed to come up")
	assert.Equal(t, 1, countCalls(fs.Calls, restartCall), "no restart on a pair that could not be restored")
	assert.Equal(t, "new", read(t, h.Paths.Bin("tw", "twillingate-sql")))
}

func TestUpdateCommitFailsWarns(t *testing.T) {
	skipIfRoot(t)
	h, fs := installed(t)
	out := &bytes.Buffer{}
	h.Out = out
	binDir := h.Paths.BinDir("tw")
	// Healthy, but /opt/S turns read-only before the backup is kept as .prev.
	fs.ActiveAfterRestart = func(string) bool {
		require.NoError(t, os.Chmod(binDir, 0o555))
		return true
	}
	t.Cleanup(func() { _ = os.Chmod(binDir, 0o755) })

	tg := target(newRelease(t, "new", "unit-new"))
	require.NoError(t, h.Update(context.Background(), tg))
	assert.Equal(t, "new", read(t, h.Paths.Bin("tw", "twillingate-sql")))
	assert.Contains(t, out.String(), "warning: keep previous unit as "+h.Paths.UnitBackup("tw", "twillingate-sql"))
	assert.Contains(t, out.String(), "rollback of twillingate-sql is unavailable until the next update\n")
	assert.NoFileExists(t, h.Paths.UnitBackup("tw", "twillingate-sql"))

	require.NoError(t, os.Chmod(binDir, 0o755))
	assert.EqualError(t, h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit), "nothing to roll back for twillingate-sql")
}

// updated returns a host where v1 ("old") was updated to v2 ("new").
func updated(t *testing.T) (*host.Host, *host.FakeSys, host.Target) {
	t.Helper()
	h, fs := installed(t)
	tg := target(newRelease(t, "new", "unit-new"))
	require.NoError(t, h.Update(context.Background(), tg))
	fs.Calls = nil
	return h, fs, tg
}

func TestRollbackSwapFails(t *testing.T) {
	skipIfRoot(t)
	for _, tc := range []struct {
		name string
		dir  func(p host.Paths) string
	}{
		{"binary", func(p host.Paths) string { return p.BinDir("tw") }},
		{"unit", func(p host.Paths) string { return filepath.Dir(p.UnitPath("x")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fs, tg := updated(t)
			p := h.Paths
			newUnit := read(t, p.UnitPath("twillingate-sql"))
			oldUnit := read(t, p.UnitBackup("tw", "twillingate-sql"))
			readOnly(t, tc.dir(p))

			err := h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "permission denied")
			// The pair stays matched: the binary is swapped back if the unit cannot be.
			assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
			assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
			assert.Equal(t, newUnit, read(t, p.UnitPath("twillingate-sql")))
			assert.Equal(t, oldUnit, read(t, p.UnitBackup("tw", "twillingate-sql")))
			assert.Empty(t, fs.Calls)
		})
	}
}

func TestRollbackRevertRestartFails(t *testing.T) {
	h, fs, tg := updated(t)
	p := h.Paths
	fs.RestartErr = errors.New("exit status 1")
	err := h.Rollback(context.Background(), "tw", tg.Ref, tg.Unit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rollback failed; previous binary and unit restored but restarting twillingate-sql failed (exit status 1)")
	assert.Equal(t, "new", read(t, p.Bin("tw", "twillingate-sql")))
	assert.Equal(t, "old", read(t, p.Bin("tw", "twillingate-sql")+".prev"))
}

func TestWaitHealthyInterrupted(t *testing.T) {
	h, fs := newHost(t)
	fs.Active = map[string]bool{"u": true}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.WaitHealthy(ctx, "u")
	require.Error(t, err)
	assert.Equal(t, "health check of u interrupted: context canceled", err.Error())

	// Interrupted while waiting between checks.
	h.HealthWait, h.HealthTick, fs.Calls = time.Minute, time.Minute, nil
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = h.WaitHealthy(ctx, "u")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "health check of u interrupted: ")
	assert.Equal(t, 1, countCalls(fs.Calls, "IsActive u"))
}

func TestWaitHealthyDefaultTick(t *testing.T) {
	h, fs := newHost(t)
	fs.Active = map[string]bool{"u": true}
	h.HealthWait, h.HealthTick = 0, 0 // no window: one check is enough
	require.NoError(t, h.WaitHealthy(context.Background(), "u"))
	assert.Equal(t, []string{"IsActive u"}, fs.Calls)
}
