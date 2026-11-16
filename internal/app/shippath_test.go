package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocateShip(t *testing.T) {
	dir := t.TempDir()
	// A versioned install: /nix/store/<hash>/bin/ship, linked from PATH.
	versioned := filepath.Join(dir, "store", "ship-1.2.3", "ship")
	require.NoError(t, os.MkdirAll(filepath.Dir(versioned), 0o755))
	require.NoError(t, os.WriteFile(versioned, []byte("ship"), 0o755))
	onPath := filepath.Join(dir, "bin", "ship")
	require.NoError(t, os.MkdirAll(filepath.Dir(onPath), 0o755))
	require.NoError(t, os.Symlink(versioned, onPath))
	other := filepath.Join(dir, "other-ship")
	require.NoError(t, os.WriteFile(other, []byte("other"), 0o755))

	exe := func() (string, error) { return versioned, nil }
	look := func(p string) func(string) (string, error) {
		return func(name string) (string, error) {
			assert.Equal(t, "ship", name)
			return p, nil
		}
	}

	got, err := locateShip(look(onPath), exe)
	require.NoError(t, err)
	assert.Equal(t, onPath, got, "the stable PATH entry, not the versioned target")

	got, err = locateShip(look(other), exe)
	require.NoError(t, err)
	assert.Equal(t, versioned, got, "PATH finds another ship: the running one")

	got, err = locateShip(func(string) (string, error) { return "", errors.New("not found") }, exe)
	require.NoError(t, err)
	assert.Equal(t, versioned, got)

	// The running executable's own path is kept as is, symlinks included.
	link := filepath.Join(dir, "link-ship")
	require.NoError(t, os.Symlink(versioned, link))
	got, err = locateShip(look(other), func() (string, error) { return link, nil })
	require.NoError(t, err)
	assert.Equal(t, link, got)

	_, err = locateShip(look(onPath), func() (string, error) { return "", errors.New("no exe") })
	require.EqualError(t, err, "no exe")
}
