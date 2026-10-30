//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// NewSys returns the real Sys for this host.
func NewSys() Sys { return linuxSys{} }

// linuxSys drives systemd and the user database through the system tools.
type linuxSys struct{}

// run executes name with args; a failure includes the command's output.
func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (linuxSys) DaemonReload(ctx context.Context) error {
	return run(ctx, "systemctl", "daemon-reload")
}

func (linuxSys) Systemctl(ctx context.Context, args ...string) error {
	return run(ctx, "systemctl", args...)
}

func (linuxSys) IsActive(ctx context.Context, unit string) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

func (linuxSys) JournalTail(ctx context.Context, unit string, n int) string {
	out, _ := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(n), "--no-pager").CombinedOutput()
	return string(out)
}

func (linuxSys) VerifyUnit(ctx context.Context, path string) error {
	return run(ctx, "systemd-analyze", "verify", path)
}

func (linuxSys) UserExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

func (linuxSys) CreateUser(ctx context.Context, name, home string) error {
	return run(ctx, "useradd", "--system", "--user-group", "--no-create-home",
		"--shell", "/usr/sbin/nologin", "--home-dir", home, name)
}

func (linuxSys) DeleteUser(ctx context.Context, name string) error { return run(ctx, "userdel", name) }

// Chown sets path's owner to "user" (group unchanged) or "user:group".
func (linuxSys) Chown(path, owner string) error {
	userName, groupName, hasGroup := strings.Cut(owner, ":")
	u, err := user.Lookup(userName)
	if err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("chown %s: uid %q: %w", path, u.Uid, err)
	}
	gid := -1
	if hasGroup {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		if gid, err = strconv.Atoi(g.Gid); err != nil {
			return fmt.Errorf("chown %s: gid %q: %w", path, g.Gid, err)
		}
	}
	return os.Chown(path, uid, gid)
}

// Owner looks up the names of path's owning uid and gid; an id without a
// name is returned as the number.
func (linuxSys) Owner(path string) (string, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", fmt.Errorf("owner of %s: no stat data", path)
	}
	uid, gid := strconv.FormatUint(uint64(st.Uid), 10), strconv.FormatUint(uint64(st.Gid), 10)
	userName, groupName := uid, gid
	if u, err := user.LookupId(uid); err == nil {
		userName = u.Username
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		groupName = g.Name
	}
	return userName, groupName, nil
}

func (linuxSys) CreateTmpfiles(ctx context.Context, conf string) error {
	return run(ctx, "systemd-tmpfiles", "--create", conf)
}
