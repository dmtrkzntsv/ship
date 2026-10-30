package cli

import (
	"bytes"
	"context"
	"io"
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
	oldG, oldA, oldE, oldT := runGuard, newApp, execFn, stdinIsTerminal
	t.Cleanup(func() { runGuard, newApp, execFn, stdinIsTerminal = oldG, oldA, oldE, oldT })
	runGuard = func(string) error { return nil }
	stdinIsTerminal = func() bool { return false }
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
		assert.Equal(t, "--pipe", got[1], "no terminal: stdio is piped")
	}

	stdinIsTerminal = func() bool { return true }
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"run", "econumo", "--service", "econumo-beta", "--", "x"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "--pty", got[1], "terminal: a pty is attached")

	root = NewRoot(io.Discard, io.Discard)
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
