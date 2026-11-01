package selfupdate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// fakeFetch makes Update see a verified release whose binary is at binary.
func fakeFetch(t *testing.T, binary string) {
	t.Helper()
	orig := fetch
	t.Cleanup(func() { fetch = orig })
	fetch = func(_ context.Context, _ store.Store, ref layout.Ref, channel, arch, dir string) (release.Fetched, error) {
		assert.Equal(t, layout.Ref{Project: "ship"}, ref)
		assert.Equal(t, "latest", channel)
		assert.Equal(t, release.HostArch(), arch)
		assert.DirExists(t, dir)
		return release.Fetched{Dir: dir, Binary: binary, Release: release.Release{Project: "ship", Version: "v2"}}, nil
	}
}

func oldExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ship")
	require.NoError(t, os.WriteFile(exe, []byte("old-binary"), 0o755))
	return exe
}

// assertUntouched checks exe still holds the old binary and no temp file is left.
func assertUntouched(t *testing.T, exe string) {
	t.Helper()
	b, err := os.ReadFile(exe)
	require.NoError(t, err)
	assert.Equal(t, "old-binary", string(b))
	ents, err := os.ReadDir(filepath.Dir(exe))
	require.NoError(t, err)
	assert.Len(t, ents, 1, "no temp files left behind")
}

func TestUpdateFetchedBinaryMissing(t *testing.T) {
	exe := oldExe(t)
	fakeFetch(t, filepath.Join(t.TempDir(), "gone"))
	var out bytes.Buffer
	_, err := Update(context.Background(), nil, "latest", exe, "v1", &out)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, out.String())
	assertUntouched(t, exe)
}

func TestUpdateFetchedBinaryUnreadable(t *testing.T) {
	exe := oldExe(t)
	fakeFetch(t, t.TempDir()) // a directory opens but cannot be read
	_, err := Update(context.Background(), nil, "latest", exe, "v1", &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write "+filepath.Join(filepath.Dir(exe), ".ship-update-"))
	assertUntouched(t, exe)
}
