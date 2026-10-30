package host_test

import (
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
