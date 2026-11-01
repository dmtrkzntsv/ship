package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/host"
	"github.com/dmtrkzntsv/ship/internal/state"
)

func TestRunSplitsBinaryArgsAfterDash(t *testing.T) {
	sf := state.File{Path: filepath.Join(t.TempDir(), "state.json")}
	s, err := sf.Load()
	require.NoError(t, err)
	s.Service("econumo-beta").Refs["econumo"] = &state.RefState{Unit: "econumo-beta", Channel: "dev", Version: "1", InstalledAt: time.Now()}
	require.NoError(t, sf.Save(s))

	var got []string
	oldG, oldA, oldE, oldT := runGuard, newApp, execFn, isTerminal
	t.Cleanup(func() { runGuard, newApp, execFn, isTerminal = oldG, oldA, oldE, oldT })
	runGuard = func(string) error { return nil }
	isTerminal = func(uintptr) bool { return false }
	newApp = func(out, errOut io.Writer) (*app.App, error) {
		return &app.App{Out: out, Err: errOut, State: sf, Host: &host.Host{Sys: &host.FakeSys{}, Out: out}}, nil
	}
	execFn = func(argv []string) error { got = argv; return nil }

	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"run", "econumo", "--service", "econumo-beta", "--", "currency:update-rates", "--force"}, []string{"currency:update-rates", "--force"}},
		{[]string{"run", "--service", "econumo-beta", "econumo", "--", "x"}, []string{"x"}},
		{[]string{"run", "econumo", "--service", "econumo-beta"}, nil},
	}
	for _, c := range cases {
		got = nil
		root := NewRoot(&bytes.Buffer{}, io.Discard)
		root.SetArgs(c.args)
		require.NoError(t, root.ExecuteContext(context.Background()), c.args)
		require.NotEmpty(t, got)
		assert.Equal(t, "/opt/econumo-beta/econumo", got[len(got)-len(c.want)-1], c.args)
		assert.Equal(t, c.want, append([]string(nil), got[len(got)-len(c.want):]...), c.args)
		assert.Contains(t, got, "--property=User=econumo-beta")
		assert.Equal(t, "--pipe", got[1], "no terminal on stdin, stdout or stderr: stdio is piped")
	}

	// Any one terminal among stdin, stdout and stderr means a pty, so a
	// root terminal is never handed to the service user.
	for _, tty := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		isTerminal = func(fd uintptr) bool { return fd == tty.Fd() }
		root := NewRoot(io.Discard, io.Discard)
		root.SetArgs([]string{"run", "econumo", "--service", "econumo-beta", "--", "x"})
		require.NoError(t, root.Execute())
		assert.Equal(t, "--pty", got[1], "terminal on fd %d: a pty is attached", tty.Fd())
	}

	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"run", "--", "x"})
	assert.Error(t, root.Execute())
}

func TestLogsRejectsNonPositiveLines(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"logs", "econumo", "-n", "0"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-n must be positive")
}

func TestRunErrors(t *testing.T) {
	env := useFakeApp(t)
	installedState(t, env.state)
	oldG := runGuard
	t.Cleanup(func() { runGuard = oldG })
	runGuard = func(string) error { return nil }

	_, _, err := runCLI(t, "run", "other", "--", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
	assert.Empty(t, env.exec, "nothing is run for an unknown ref")

	newApp = func(io.Writer, io.Writer) (*app.App, error) { return nil, errors.New("bad config") }
	_, _, err = runCLI(t, "run", "econumo")
	assert.EqualError(t, err, "bad config")
}
