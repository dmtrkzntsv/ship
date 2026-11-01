package selfupdate_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/selfupdate"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
)

func publishShip(t *testing.T) *storetest.Mem {
	t.Helper()
	dir := t.TempDir()
	for _, arch := range []string{"amd64", "arm64", release.HostArch()} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ship-linux-"+arch), []byte("new-binary"), 0o644))
	}
	st := storetest.NewMem()
	require.NoError(t, release.Publish(context.Background(), st, release.PublishOptions{
		Project: "ship", Dir: dir, Version: "v2.0.0", Channels: []string{"latest"},
		Now: time.Now, Out: &bytes.Buffer{},
	}))
	return st
}

func fakeExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ship")
	require.NoError(t, os.WriteFile(exe, []byte("old-binary"), 0o755))
	return exe
}

func TestSelfUpdateReplaces(t *testing.T) {
	st, exe := publishShip(t), fakeExe(t)
	var out bytes.Buffer

	v, err := selfupdate.Run(context.Background(), st, "latest", exe, &out)
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", v)

	b, err := os.ReadFile(exe)
	require.NoError(t, err)
	assert.Equal(t, "new-binary", string(b))
	fi, err := os.Stat(exe)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	ents, _ := os.ReadDir(filepath.Dir(exe))
	assert.Len(t, ents, 1, "no temp files left behind")
}

func TestSelfUpdateMismatchKeepsOld(t *testing.T) {
	st, exe := publishShip(t), fakeExe(t)
	key := "ship/latest/ship-linux-" + release.HostArch()
	o := st.Objects[key]
	o.Data = append([]byte("x"), o.Data...)
	st.Objects[key] = o

	_, err := selfupdate.Run(context.Background(), st, "latest", exe, &bytes.Buffer{})
	require.Error(t, err)
	b, rerr := os.ReadFile(exe)
	require.NoError(t, rerr)
	assert.Equal(t, "old-binary", string(b))
	ents, _ := os.ReadDir(filepath.Dir(exe))
	assert.Len(t, ents, 1)
}

func TestUpdateSkipsWhenAlreadyCurrent(t *testing.T) {
	st, exe := publishShip(t), fakeExe(t)
	var out bytes.Buffer
	v, err := selfupdate.Update(context.Background(), st, "latest", exe, "v2.0.0", &out)
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", v)
	assert.Equal(t, "ship: already on v2.0.0\n", out.String())
	b, _ := os.ReadFile(exe)
	assert.Equal(t, "old-binary", string(b))
}

func TestUpdatePrintsTransition(t *testing.T) {
	st, exe := publishShip(t), fakeExe(t)
	var out bytes.Buffer
	_, err := selfupdate.Update(context.Background(), st, "latest", exe, "v1.0.0", &out)
	require.NoError(t, err)
	assert.Equal(t, "ship: v1.0.0 -> v2.0.0\n", out.String())
}

func TestUpdateUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	st, exe := publishShip(t), fakeExe(t)
	require.NoError(t, os.Chmod(filepath.Dir(exe), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(exe), 0o755) })
	_, err := selfupdate.Update(context.Background(), st, "latest", exe, "v1", &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sudo")
}

func TestUpdateNoWorkDir(t *testing.T) {
	st, exe := publishShip(t), fakeExe(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	_, err := selfupdate.Update(context.Background(), st, "latest", exe, "v1", &bytes.Buffer{})
	require.Error(t, err)
	b, _ := os.ReadFile(exe)
	assert.Equal(t, "old-binary", string(b))
	ents, _ := os.ReadDir(filepath.Dir(exe))
	assert.Len(t, ents, 1, "the replacement temp file is removed")
}

func TestUpdateUnknownChannel(t *testing.T) {
	st, exe := publishShip(t), fakeExe(t)
	_, err := selfupdate.Update(context.Background(), st, "v9", exe, "v1", &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `channel "v9" not found for ship`)
	b, _ := os.ReadFile(exe)
	assert.Equal(t, "old-binary", string(b))
}

func TestUpdateCannotReplaceExe(t *testing.T) {
	st := publishShip(t)
	// exe is a non-empty directory, so the final rename fails.
	exe := filepath.Join(t.TempDir(), "ship")
	require.NoError(t, os.MkdirAll(filepath.Join(exe, "keep"), 0o755))
	var out bytes.Buffer
	_, err := selfupdate.Update(context.Background(), st, "latest", exe, "v1", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replace "+exe)
	assert.Empty(t, out.String())
	ents, _ := os.ReadDir(filepath.Dir(exe))
	assert.Len(t, ents, 1, "the replacement temp file is removed")
}
