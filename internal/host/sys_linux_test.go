//go:build linux

package host_test

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
)

// stubScript records "name args..." to $STUB_LOG, prints $STUB_OUT and exits
// with $STUB_EXIT. It uses only shell builtins: PATH holds nothing else.
const stubScript = `#!/bin/sh
echo "${0##*/} $*" >> "$STUB_LOG"
if [ -n "$STUB_OUT" ]; then printf '%s' "$STUB_OUT" >&2; fi
exit ${STUB_EXIT:-0}
`

// stubTools puts stub systemctl, journalctl, systemd-analyze,
// systemd-tmpfiles, useradd and userdel alone on PATH and returns a function
// reading the recorded command lines.
func stubTools(t *testing.T, exit, out string) func() []string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"systemctl", "journalctl", "systemd-analyze", "systemd-tmpfiles", "useradd", "userdel"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(stubScript), 0o755))
	}
	log := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir)
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_EXIT", exit)
	t.Setenv("STUB_OUT", out)
	return func() []string {
		b, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err)
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

// sysCommands are the Sys methods that run one command, with the command
// line each must run.
var sysCommands = []struct {
	name string
	call func(context.Context, host.Sys) error
	want string
}{
	{"DaemonReload", func(ctx context.Context, s host.Sys) error { return s.DaemonReload(ctx) }, "systemctl daemon-reload"},
	{"Systemctl", func(ctx context.Context, s host.Sys) error { return s.Systemctl(ctx, "enable", "--now", "tw") }, "systemctl enable --now tw"},
	{"VerifyUnit", func(ctx context.Context, s host.Sys) error { return s.VerifyUnit(ctx, "/tmp/x/tw.service") }, "systemd-analyze verify /tmp/x/tw.service"},
	{"CreateUser", func(ctx context.Context, s host.Sys) error { return s.CreateUser(ctx, "tw", "/var/lib/tw") },
		"useradd --system --user-group --no-create-home --shell /usr/sbin/nologin --home-dir /var/lib/tw tw"},
	{"DeleteUser", func(ctx context.Context, s host.Sys) error { return s.DeleteUser(ctx, "tw") }, "userdel tw"},
	{"CreateTmpfiles", func(ctx context.Context, s host.Sys) error {
		return s.CreateTmpfiles(ctx, "/etc/tmpfiles.d/ship-tw.conf")
	}, "systemd-tmpfiles --create /etc/tmpfiles.d/ship-tw.conf"},
}

func TestLinuxSysRunsCommands(t *testing.T) {
	for _, c := range sysCommands {
		t.Run(c.name, func(t *testing.T) {
			calls := stubTools(t, "0", "ignored on success")
			require.NoError(t, c.call(context.Background(), host.NewSys()))
			assert.Equal(t, []string{c.want}, calls())
		})
	}
}

func TestLinuxSysCommandFailure(t *testing.T) {
	for _, c := range sysCommands {
		t.Run(c.name, func(t *testing.T) {
			stubTools(t, "3", "  unit tw: boom\n")
			err := c.call(context.Background(), host.NewSys())
			require.Error(t, err)
			assert.Equal(t, c.want+": exit status 3: unit tw: boom", err.Error())
		})
	}
}

func TestLinuxSysCommandFailureWithoutOutput(t *testing.T) {
	calls := stubTools(t, "1", "")
	err := host.NewSys().DeleteUser(context.Background(), "tw")
	require.Error(t, err)
	assert.Equal(t, "userdel tw: exit status 1", err.Error())
	assert.Equal(t, []string{"userdel tw"}, calls())
}

func TestLinuxSysCommandMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := host.NewSys().DaemonReload(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "systemctl daemon-reload: ")
	assert.Contains(t, err.Error(), "executable file not found")
}

func TestLinuxSysIsActive(t *testing.T) {
	calls := stubTools(t, "0", "")
	assert.True(t, host.NewSys().IsActive(context.Background(), "tw"))
	assert.Equal(t, []string{"systemctl is-active --quiet tw"}, calls())

	stubTools(t, "3", "")
	assert.False(t, host.NewSys().IsActive(context.Background(), "tw"))
}

func TestLinuxSysJournalTail(t *testing.T) {
	calls := stubTools(t, "0", "line1\nline2")
	assert.Equal(t, "line1\nline2", host.NewSys().JournalTail(context.Background(), "tw", 30))
	assert.Equal(t, []string{"journalctl -u tw -n 30 --no-pager"}, calls())

	// A failing journalctl still yields whatever it printed.
	stubTools(t, "1", "No journal files were found.")
	assert.Equal(t, "No journal files were found.", host.NewSys().JournalTail(context.Background(), "tw", 5))
}

func currentUser(t *testing.T) (userName, groupName string) {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	g, err := user.LookupGroupId(u.Gid)
	require.NoError(t, err)
	return u.Username, g.Name
}

func TestLinuxSysUserExists(t *testing.T) {
	me, _ := currentUser(t)
	s := host.NewSys()
	assert.True(t, s.UserExists(me))
	assert.False(t, s.UserExists("ship-test-no-such-user"))
}

func TestLinuxSysChownAndOwner(t *testing.T) {
	me, group := currentUser(t)
	s := host.NewSys()
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	u, g, err := s.Owner(path)
	require.NoError(t, err)
	assert.Equal(t, me, u)
	assert.Equal(t, group, g)

	// A non-root user may chown a file to itself, with or without its group.
	require.NoError(t, s.Chown(path, me))
	require.NoError(t, s.Chown(path, me+":"+group))
	u, g, err = s.Owner(path)
	require.NoError(t, err)
	assert.Equal(t, me, u)
	assert.Equal(t, group, g)
}

func TestLinuxSysChownErrors(t *testing.T) {
	me, _ := currentUser(t)
	s := host.NewSys()
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	err := s.Chown(path, "ship-test-no-such-user")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chown "+path+": ")
	assert.Contains(t, err.Error(), "ship-test-no-such-user")

	err = s.Chown(path, me+":ship-test-no-such-group")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chown "+path+": ")
	assert.Contains(t, err.Error(), "ship-test-no-such-group")

	missing := filepath.Join(t.TempDir(), "missing")
	assert.ErrorIs(t, s.Chown(missing, me), os.ErrNotExist)
}

func TestLinuxSysOwnerMissing(t *testing.T) {
	_, _, err := host.NewSys().Owner(filepath.Join(t.TempDir(), "missing"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
