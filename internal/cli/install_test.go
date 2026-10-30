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
