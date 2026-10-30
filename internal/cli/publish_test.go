package cli

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishRejectsInvalidProject(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"publish", "Bad Name", "--path", t.TempDir(), "--channels", "latest"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid project name")
}

func TestPublishRequiresPath(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"publish", "econumo"})
	assert.Error(t, root.Execute())
}

func TestKeepMustBeNonNegative(t *testing.T) {
	for _, args := range [][]string{
		{"publish", "econumo", "--path", ".", "--channels", "latest", "--keep", "-1"},
		{"prune", "econumo", "--keep", "-1"},
	} {
		root := NewRoot(io.Discard, io.Discard)
		root.SetArgs(args)
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--keep must be >= 0")
	}
}

func TestParseDuration(t *testing.T) {
	d, err := parseDuration("90d")
	require.NoError(t, err)
	assert.Equal(t, 90*24*time.Hour, d)
	d, err = parseDuration("72h")
	require.NoError(t, err)
	assert.Equal(t, 72*time.Hour, d)
	_, err = parseDuration("abc")
	assert.Error(t, err)
}
