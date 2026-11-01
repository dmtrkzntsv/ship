package host_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
)

const sqlUnit = `[Service]
User={{USER}}
Group={{USER}}
EnvironmentFile={{ENV_FILE}}
Environment=TWSQL_SOCKET={{RUN_DIR}}/sql.sock
ExecStart={{BIN}}
ReadOnlyPaths={{DATA_DIR}}
ReadWritePaths={{RUN_DIR}}
`

// fetched writes a fake fetched release for project twillingate-sql into a temp dir.
func fetched(t *testing.T, envExample string) release.Fetched {
	t.Helper()
	dir := t.TempDir()
	f := release.Fetched{Dir: dir, Release: release.Release{Project: "twillingate-sql", Version: "v1"}}
	f.Binary = filepath.Join(dir, "twillingate-sql-linux-amd64")
	require.NoError(t, os.WriteFile(f.Binary, []byte("binary"), 0o755))
	f.Unit = filepath.Join(dir, "twillingate-sql.service")
	require.NoError(t, os.WriteFile(f.Unit, []byte(sqlUnit), 0o644))
	if envExample != "" {
		f.EnvExample = filepath.Join(dir, "twillingate-sql.env.example")
		require.NoError(t, os.WriteFile(f.EnvExample, []byte(envExample), 0o644))
	}
	return f
}

func newHost(t *testing.T) (*host.Host, *host.FakeSys) {
	t.Helper()
	fs := &host.FakeSys{}
	return &host.Host{Paths: host.Paths{Root: t.TempDir()}, Sys: fs, Out: io.Discard}, fs
}

func target(f release.Fetched) host.Target {
	return host.Target{Service: "tw", Ref: layout.Ref{Project: "twillingate-sql"}, Unit: "twillingate-sql", Fetched: f}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Mode().Perm()
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func hasCallPrefix(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestPaths(t *testing.T) {
	p := host.Paths{Root: "/r"}
	assert.Equal(t, "/r/opt/tw", p.BinDir("tw"))
	assert.Equal(t, "/r/opt/tw/twillingate-sql", p.Bin("tw", "twillingate-sql"))
	assert.Equal(t, "/r/etc/tw", p.EnvDir("tw"))
	assert.Equal(t, "/r/etc/tw/sql.env", p.EnvFile("tw", "sql"))
	assert.Equal(t, "/r/var/lib/tw", p.DataDir("tw"))
	assert.Equal(t, "/r/run/tw", p.RunDir("tw"))
	assert.Equal(t, "/r/etc/systemd/system/tw-x.service", p.UnitPath("tw-x"))
	assert.Equal(t, "/r/opt/tw/tw-x.service.prev", p.UnitBackup("tw", "tw-x"))
	assert.Equal(t, "/r/etc/tmpfiles.d/ship-tw.conf", p.Tmpfiles("tw"))
	assert.Equal(t, "/opt/tw", host.Paths{}.BinDir("tw"))

	ph := p.Placeholders("tw", layout.Ref{Project: "twillingate-sql"}, "twillingate-sql")
	assert.Equal(t, layout.Placeholders{
		Service: "tw", Bin: "/opt/tw/twillingate-sql", BinDir: "/opt/tw", EnvFile: "/etc/tw/twillingate-sql.env",
		DataDir: "/var/lib/tw", RunDir: "/run/tw", Unit: "twillingate-sql",
	}, ph)
}

func TestInstallLaysOutFiles(t *testing.T) {
	for _, example := range []string{"", "TWSQL_X=1\n"} {
		h, fs := newHost(t)
		p := h.Paths
		require.NoError(t, h.Install(context.Background(), target(fetched(t, example))))

		bin := p.Bin("tw", "twillingate-sql")
		assert.Equal(t, "binary", read(t, bin))
		assert.Equal(t, os.FileMode(0o755), mode(t, bin))

		unit := read(t, p.UnitPath("twillingate-sql"))
		assert.Contains(t, unit, "/run/tw/sql.sock")
		assert.Contains(t, unit, "ExecStart=/opt/tw/twillingate-sql")
		assert.Contains(t, unit, "EnvironmentFile=/etc/tw/twillingate-sql.env")
		assert.NotContains(t, unit, "{{")
		assert.NotContains(t, unit, p.Root)
		assert.Equal(t, os.FileMode(0o644), mode(t, p.UnitPath("twillingate-sql")))

		env := p.EnvFile("tw", "twillingate-sql")
		assert.Equal(t, example, read(t, env))
		assert.Equal(t, os.FileMode(0o640), mode(t, env))

		assert.Equal(t, os.FileMode(0o755), mode(t, p.BinDir("tw")))
		assert.Equal(t, os.FileMode(0o750), mode(t, p.DataDir("tw")))
		assert.Equal(t, os.FileMode(0o750), mode(t, p.EnvDir("tw")))

		assert.Contains(t, read(t, p.Tmpfiles("tw")), "d /run/tw 0750 tw tw -")

		calls := fs.Calls
		assert.Contains(t, calls, "CreateUser tw /var/lib/tw")
		assert.Contains(t, calls, "CreateTmpfiles "+p.Tmpfiles("tw"))
		assert.Contains(t, calls, "Chown "+p.DataDir("tw")+" tw:tw")
		assert.Contains(t, calls, "Chown "+env+" root:tw")
		// the service reads its own files in /etc/S (keys, certificates)
		assert.Contains(t, calls, "Chown "+p.EnvDir("tw")+" root:tw")
		assert.True(t, hasCallPrefix(calls, "VerifyUnit "), calls)
		assert.Contains(t, calls, "DaemonReload")
		assert.Contains(t, calls, "systemctl enable twillingate-sql")
		for _, c := range calls {
			assert.NotContains(t, c, "start", c)
		}
		// the unit is verified under its real name, before it is moved into place
		for _, c := range calls {
			if path, ok := strings.CutPrefix(c, "VerifyUnit "); ok {
				assert.Equal(t, "twillingate-sql.service", filepath.Base(path))
				assert.NotEqual(t, p.UnitPath("twillingate-sql"), path)
			}
		}
		// order: user, tmpfiles, verify, reload, enable
		idx := func(prefix string) int {
			for i, c := range calls {
				if strings.HasPrefix(c, prefix) {
					return i
				}
			}
			return -1
		}
		assert.Less(t, idx("CreateUser"), idx("CreateTmpfiles"))
		assert.Less(t, idx("CreateTmpfiles"), idx("VerifyUnit"))
		assert.Less(t, idx("VerifyUnit"), idx("DaemonReload"))
		assert.Less(t, idx("DaemonReload"), idx("systemctl enable"))
		// no leftover temp files next to the installed files
		for _, d := range []string{p.BinDir("tw"), p.EnvDir("tw"), filepath.Dir(p.UnitPath("x"))} {
			ents, err := os.ReadDir(d)
			require.NoError(t, err)
			for _, e := range ents {
				assert.False(t, strings.HasPrefix(e.Name(), "."), "leftover %s in %s", e.Name(), d)
			}
		}
	}
}

func TestInstallSkipsExistingUser(t *testing.T) {
	h, fs := newHost(t)
	fs.Users = map[string]bool{"tw": true}
	require.NoError(t, h.Install(context.Background(), target(fetched(t, ""))))
	assert.False(t, hasCallPrefix(fs.Calls, "CreateUser"), fs.Calls)
}

func TestInstallKeepsExistingEnv(t *testing.T) {
	h, _ := newHost(t)
	env := h.Paths.EnvFile("tw", "twillingate-sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(env), 0o750))
	require.NoError(t, os.WriteFile(env, []byte("SECRET=keep\n"), 0o600))

	require.NoError(t, h.Install(context.Background(), target(fetched(t, "SECRET=example\n"))))
	assert.Equal(t, "SECRET=keep\n", read(t, env))
	assert.Equal(t, os.FileMode(0o600), mode(t, env))
}

func TestInstallVerifyFails(t *testing.T) {
	h, fs := newHost(t)
	fs.VerifyErr = errors.New("bad unit")
	err := h.Install(context.Background(), target(fetched(t, "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad unit")
	assert.NoFileExists(t, h.Paths.UnitPath("twillingate-sql"))
	assert.NoFileExists(t, h.Paths.EnvFile("tw", "twillingate-sql"))
	assert.False(t, hasCallPrefix(fs.Calls, "systemctl enable"), fs.Calls)
	assert.False(t, hasCallPrefix(fs.Calls, "DaemonReload"), fs.Calls)
}

func TestInstallBadTemplateTouchesNothing(t *testing.T) {
	h, fs := newHost(t)
	f := fetched(t, "")
	require.NoError(t, os.WriteFile(f.Unit, []byte("[Service]\nExecStart={{NOPE}}\n"), 0o644))
	err := h.Install(context.Background(), target(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "{{NOPE}}")
	assert.Empty(t, fs.Calls)
	ents, err := os.ReadDir(h.Paths.Root)
	require.NoError(t, err)
	assert.Empty(t, ents)
}

func TestForeignUnit(t *testing.T) {
	h, _ := newHost(t)
	p := h.Paths
	assert.False(t, h.ForeignUnit("econumo"))

	write := func(path string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, nil, 0o644))
	}
	write(p.UnitPath("econumo"))
	assert.True(t, h.ForeignUnit("econumo"))
}

func TestInstallLateFailureRemovesUnit(t *testing.T) {
	for _, name := range []string{"enable", "daemon-reload"} {
		t.Run(name, func(t *testing.T) {
			h, fs := newHost(t)
			if name == "enable" {
				fs.EnableErr = errors.New("enable failed")
			} else {
				fs.ReloadErr = errors.New("reload failed")
			}
			err := h.Install(context.Background(), target(fetched(t, "")))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed")
			assert.NoFileExists(t, h.Paths.UnitPath("twillingate-sql"))
			assert.False(t, h.ForeignUnit("twillingate-sql"))

			fs.EnableErr, fs.ReloadErr, fs.Calls = nil, nil, nil
			require.NoError(t, h.Install(context.Background(), target(fetched(t, ""))))
			assert.FileExists(t, h.Paths.UnitPath("twillingate-sql"))
			assert.Contains(t, fs.Calls, "systemctl enable twillingate-sql")
		})
	}
}

func TestInstallFailureRemovesWhatItCreated(t *testing.T) {
	h, fs := newHost(t)
	fs.VerifyErr = errors.New("bad unit")
	p := h.Paths
	require.Error(t, h.Install(context.Background(), target(fetched(t, ""))))
	for _, gone := range []string{p.BinDir("tw"), p.EnvDir("tw"), p.DataDir("tw"), p.Tmpfiles("tw"), p.RunDir("tw")} {
		assert.NoFileExists(t, gone)
		assert.NoDirExists(t, gone)
	}
	assert.Contains(t, fs.Calls, "DeleteUser tw")
	assert.False(t, fs.UserExists("tw"))

	// A retry starts from scratch.
	fs.VerifyErr = nil
	require.NoError(t, h.Install(context.Background(), target(fetched(t, ""))))
	assert.FileExists(t, p.UnitPath("twillingate-sql"))
}

func TestInstallFailureKeepsWhatExisted(t *testing.T) {
	h, fs := newHost(t)
	ctx := context.Background()
	require.NoError(t, h.Install(ctx, target(fetched(t, ""))))
	fs.VerifyErr, fs.Calls = errors.New("bad unit"), nil
	p := h.Paths
	err := h.Install(ctx, host.Target{Service: "tw", Ref: layout.Ref{Project: "twillingate"}, Unit: "twillingate", Fetched: fetched(t, "")})
	require.Error(t, err)
	for _, kept := range []string{p.BinDir("tw"), p.EnvDir("tw"), p.DataDir("tw"), p.Tmpfiles("tw"), p.Bin("tw", "twillingate-sql")} {
		_, serr := os.Stat(kept)
		assert.NoError(t, serr, kept)
	}
	assert.NoFileExists(t, p.Bin("tw", "twillingate"))
	assert.False(t, hasCallPrefix(fs.Calls, "DeleteUser"), fs.Calls)
}

func TestInstallFixesExistingEnvGroup(t *testing.T) {
	h, fs := newHost(t)
	env := h.Paths.EnvFile("tw", "twillingate-sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(env), 0o750))
	require.NoError(t, os.WriteFile(env, []byte("SECRET=keep\n"), 0o600))

	require.NoError(t, h.Install(context.Background(), target(fetched(t, ""))))
	assert.Contains(t, fs.Calls, "Chown "+env+" root:tw")
	assert.Equal(t, "SECRET=keep\n", read(t, env))
	assert.Equal(t, os.FileMode(0o600), mode(t, env), "a hand-tuned mode is kept")
}

func TestInstallLeavesEnvWithServiceGroup(t *testing.T) {
	h, fs := newHost(t)
	env := h.Paths.EnvFile("tw", "twillingate-sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(env), 0o750))
	require.NoError(t, os.WriteFile(env, []byte("SECRET=keep\n"), 0o640))
	fs.Owners = map[string]string{env: "admin:tw"}

	require.NoError(t, h.Install(context.Background(), target(fetched(t, ""))))
	assert.False(t, hasCallPrefix(fs.Calls, "Chown "+env), fs.Calls)
	assert.Equal(t, "admin:tw", fs.Owners[env])
}

// assertHostClean checks that a failed first install left nothing behind:
// no directories, no tmpfiles entry, no unit file, and the user it created
// is deleted again.
func assertHostClean(t *testing.T, h *host.Host, fs *host.FakeSys) {
	t.Helper()
	p := h.Paths
	for _, gone := range []string{p.BinDir("tw"), p.EnvDir("tw"), p.DataDir("tw"), p.Tmpfiles("tw"), p.RunDir("tw"),
		p.UnitPath("twillingate-sql")} {
		assert.False(t, pathExists(gone), gone)
	}
	assert.Contains(t, fs.Calls, "DeleteUser tw")
	assert.False(t, fs.UserExists("tw"))
}

func TestInstallSysFailureUndoes(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name, call, want string
	}{
		{"chown data dir", "Chown {root}/var/lib/tw tw:tw", "boom"},
		{"chown env dir", "Chown {root}/etc/tw root:tw", "boom"},
		{"chown env file", "Chown {root}/etc/tw/twillingate-sql.env root:tw", "boom"},
		{"tmpfiles", "CreateTmpfiles {root}/etc/tmpfiles.d/ship-tw.conf", "create /run/tw: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := newHost(t)
			fs.Fail = map[string]error{strings.ReplaceAll(tc.call, "{root}", h.Paths.Root): boom}
			err := h.Install(context.Background(), target(fetched(t, "")))
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.ErrorIs(t, err, boom)
			assertHostClean(t, h, fs)
			assert.False(t, hasCallPrefix(fs.Calls, "systemctl"), fs.Calls)
		})
	}
}

func TestInstallCreateUserFails(t *testing.T) {
	h, fs := newHost(t)
	fs.Fail = map[string]error{"CreateUser tw /var/lib/tw": errors.New("useradd: exit status 9")}
	err := h.Install(context.Background(), target(fetched(t, "")))
	require.Error(t, err)
	assert.Equal(t, "create user tw: useradd: exit status 9", err.Error())
	assert.Equal(t, []string{"CreateUser tw /var/lib/tw"}, fs.Calls, "nothing else is attempted")
	ents, err := os.ReadDir(h.Paths.Root)
	require.NoError(t, err)
	assert.Empty(t, ents)
}

func TestInstallUnreadableTemplate(t *testing.T) {
	h, fs := newHost(t)
	f := fetched(t, "")
	f.Unit = filepath.Join(t.TempDir(), "missing.service")
	err := h.Install(context.Background(), target(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read unit template: ")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, fs.Calls)
}

func TestInstallFileSystemFailureUndoes(t *testing.T) {
	// Each case puts a plain file where install needs a directory (or a
	// directory where it needs a file), so one step fails part way through.
	for _, tc := range []struct {
		name  string
		block func(p host.Paths) string // path to occupy with a file
		want  string
	}{
		{"data dir", func(p host.Paths) string { return filepath.Dir(p.DataDir("tw")) }, "not a directory"},
		{"env dir", func(p host.Paths) string { return p.EnvDir("tw") }, "not a directory"},
		{"tmpfiles dir", func(p host.Paths) string { return filepath.Dir(p.Tmpfiles("tw")) }, "not a directory"},
		{"unit dir", func(p host.Paths) string { return filepath.Dir(p.UnitPath("x")) }, "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := newHost(t)
			blocker := tc.block(h.Paths)
			require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o755))
			require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

			err := h.Install(context.Background(), target(fetched(t, "")))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, "x", read(t, blocker), "a path that existed before is kept")
			p := h.Paths
			for _, gone := range []string{p.BinDir("tw"), p.DataDir("tw"), p.Tmpfiles("tw"), p.UnitPath("twillingate-sql")} {
				if gone != blocker && !strings.HasPrefix(gone, blocker+"/") {
					assert.False(t, pathExists(gone), gone)
				}
			}
			assert.Contains(t, fs.Calls, "DeleteUser tw")
			assert.False(t, hasCallPrefix(fs.Calls, "systemctl"), fs.Calls)
		})
	}
}

func TestInstallBinaryCopyFails(t *testing.T) {
	t.Run("missing binary", func(t *testing.T) {
		h, fs := newHost(t)
		f := fetched(t, "")
		f.Binary = filepath.Join(t.TempDir(), "missing")
		err := h.Install(context.Background(), target(f))
		require.ErrorIs(t, err, os.ErrNotExist)
		assertHostClean(t, h, fs)
	})
	t.Run("unreadable binary", func(t *testing.T) {
		h, fs := newHost(t)
		f := fetched(t, "")
		f.Binary = t.TempDir() // opens, but reading a directory fails
		err := h.Install(context.Background(), target(f))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write "+h.Paths.Bin("tw", "twillingate-sql")+": ")
		assertHostClean(t, h, fs)
		assert.False(t, hasCallPrefix(fs.Calls, "VerifyUnit"), fs.Calls)
	})
	t.Run("binary path is a directory", func(t *testing.T) {
		h, fs := newHost(t)
		bin := h.Paths.Bin("tw", "twillingate-sql")
		require.NoError(t, os.MkdirAll(filepath.Join(bin, "sub"), 0o755))
		err := h.Install(context.Background(), target(fetched(t, "")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install "+bin+": ")
		assert.DirExists(t, filepath.Join(bin, "sub"), "an existing path is not removed")
		assert.Contains(t, fs.Calls, "DeleteUser tw")
		assertNoTemp(t, h.Paths.BinDir("tw"))
	})
}

func TestInstallNoTempDir(t *testing.T) {
	h, fs := newHost(t)
	f := fetched(t, "")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	err := h.Install(context.Background(), target(f))
	require.ErrorIs(t, err, os.ErrNotExist)
	assertHostClean(t, h, fs)
	assert.False(t, hasCallPrefix(fs.Calls, "VerifyUnit"), fs.Calls)
}

func TestInstallEnvExampleUnreadable(t *testing.T) {
	h, fs := newHost(t)
	f := fetched(t, "X=1\n")
	require.NoError(t, os.Remove(f.EnvExample))
	err := h.Install(context.Background(), target(f))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read env example: ")
	assertHostClean(t, h, fs)
	assert.NoFileExists(t, h.Paths.EnvFile("tw", "twillingate-sql"))
}

func TestInstallExistingEnvOwnerFails(t *testing.T) {
	h, fs := newHost(t)
	env := h.Paths.EnvFile("tw", "twillingate-sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(env), 0o750))
	require.NoError(t, os.WriteFile(env, []byte("SECRET=keep\n"), 0o600))
	fs.Fail = map[string]error{"Owner " + env: errors.New("stat failed")}

	err := h.Install(context.Background(), target(fetched(t, "")))
	require.Error(t, err)
	assert.Equal(t, "stat failed", err.Error())
	assert.Equal(t, "SECRET=keep\n", read(t, env), "an existing env file is never removed")
	assert.NoFileExists(t, h.Paths.UnitPath("twillingate-sql"))
	assert.NoDirExists(t, h.Paths.BinDir("tw"))
}

func TestCheckUnclaimed(t *testing.T) {
	mkdir := func(t *testing.T, path string) {
		require.NoError(t, os.MkdirAll(path, 0o755))
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, p host.Paths, fs *host.FakeSys)
		want  string // "" means no error
	}{
		{"nothing there", func(*testing.T, host.Paths, *host.FakeSys) {}, ""},
		{"bin dir exists", func(t *testing.T, p host.Paths, fs *host.FakeSys) {
			mkdir(t, p.BinDir("tw"))
			fs.Users = map[string]bool{"tw": true}
		}, "/opt/tw"},
		{"kept env and data dirs", func(t *testing.T, p host.Paths, fs *host.FakeSys) {
			mkdir(t, p.EnvDir("tw"))
			mkdir(t, p.DataDir("tw"))
			fs.Users = map[string]bool{"tw": true}
			fs.Owners = map[string]string{p.EnvDir("tw"): "root:tw", p.DataDir("tw"): "tw:tw"}
		}, ""},
		{"env dir with another group", func(t *testing.T, p host.Paths, fs *host.FakeSys) {
			mkdir(t, p.EnvDir("tw"))
			fs.Users = map[string]bool{"tw": true}
			fs.Owners = map[string]string{p.EnvDir("tw"): "root:root"}
		}, "/etc/tw"},
		{"data dir owned by another user", func(t *testing.T, p host.Paths, fs *host.FakeSys) {
			mkdir(t, p.DataDir("tw"))
			fs.Users = map[string]bool{"tw": true}
			fs.Owners = map[string]string{p.DataDir("tw"): "postgres:tw"}
		}, "/var/lib/tw"},
		{"user missing", func(t *testing.T, p host.Paths, fs *host.FakeSys) {
			mkdir(t, p.DataDir("tw"))
			fs.Owners = map[string]string{p.DataDir("tw"): "tw:tw"}
		}, "/var/lib/tw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fs := newHost(t)
			tc.setup(t, h.Paths, fs)
			err := h.CheckUnclaimed("tw")
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.want+" already exists and service tw is not managed by ship; ship won't take over existing paths (remove it or pick another --service)", err.Error())
		})
	}
}

func TestCheckUnclaimedOwnerFails(t *testing.T) {
	h, fs := newHost(t)
	require.NoError(t, os.MkdirAll(h.Paths.EnvDir("tw"), 0o755))
	fs.Fail = map[string]error{"Owner " + h.Paths.EnvDir("tw"): errors.New("stat failed")}
	assert.EqualError(t, h.CheckUnclaimed("tw"), "stat failed")
}
