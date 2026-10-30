package cli

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallRequiresVersionFlag(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"install", "econumo"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"version" not set`)
}

func TestInstallRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"install", "econumo", "--version", "dev"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ship install:")
}

func TestUpdateAndRollbackRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, cmd := range []string{"update", "rollback"} {
		root := NewRoot(io.Discard, io.Discard)
		root.SetArgs([]string{cmd, "econumo", "--service", "econumo-beta"})
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ship "+cmd+":")
	}
}

func TestOpsCommandsRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	for _, cmd := range []string{"uninstall", "start", "stop", "restart", "run"} {
		root := NewRoot(io.Discard, io.Discard)
		root.SetArgs([]string{cmd, "econumo"})
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ship "+cmd+": must run as root")
	}
}

func TestAdoptFlags(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"adopt", "econumo", "--service", "econumo-beta"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"version" not set`)

	if os.Geteuid() == 0 {
		return
	}
	root = NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"adopt", "econumo", "--service", "econumo-beta", "--version", "dev"})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ship adopt: must run as root")

	root = NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"adopt", "econumo", "--version", "dev", "--purge-cache"})
	require.Error(t, root.Execute(), "adopt has no --purge-cache")
}
