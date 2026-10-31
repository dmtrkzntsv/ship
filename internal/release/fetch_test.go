package release_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
	"github.com/dmtrkzntsv/ship/internal/sums"
)

func publishFixture(t *testing.T, project string, names ...string) *storetest.Mem {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		content := "data-" + n
		if filepath.Ext(n) == ".service" {
			content = "[Service]\nExecStart={{BIN}}\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte(content), 0o644))
	}
	st := storetest.NewMem()
	o := opts(dir)
	o.Project, o.Version, o.Channels, o.Keep = project, "v1", []string{"latest"}, 0
	require.NoError(t, release.Publish(context.Background(), st, o))
	return st
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestFetchComponent(t *testing.T) {
	st := publishFixture(t, "winterflow", "winterflow-hub-linux-arm64", "winterflow-hub-linux-amd64", "winterflow-hub.service", "winterflow-hub.env.example")
	dir := t.TempDir()
	ref, err := layout.ParseRef("winterflow/hub")
	require.NoError(t, err)

	f, err := release.Fetch(context.Background(), st, ref, "latest", "arm64", dir)
	require.NoError(t, err)
	assert.Equal(t, "v1", f.Release.Version)
	assert.Equal(t, "winterflow", f.Release.Project)
	assert.Equal(t, dir, f.Dir)
	assert.Equal(t, filepath.Join(dir, "winterflow-hub-linux-arm64"), f.Binary)
	assert.Equal(t, filepath.Join(dir, "winterflow-hub.service"), f.Unit)
	assert.Equal(t, filepath.Join(dir, "winterflow-hub.env.example"), f.EnvExample)
	assert.ElementsMatch(t, []string{"winterflow-hub-linux-arm64", "winterflow-hub.service", "winterflow-hub.env.example"}, dirFiles(t, dir))

	fi, err := os.Stat(f.Binary)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	fi, err = os.Stat(f.Unit)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm())
	b, err := os.ReadFile(f.Binary)
	require.NoError(t, err)
	assert.Equal(t, "data-winterflow-hub-linux-arm64", string(b))
}

func TestFetchMismatchLeavesNoFiles(t *testing.T) {
	for _, tamper := range []string{"econumo/latest/econumo-linux-amd64", "econumo/latest/econumo.service", "econumo/latest/release.json"} {
		st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
		o := st.Objects[tamper]
		o.Data = append([]byte("x"), o.Data...)
		st.Objects[tamper] = o
		dir := t.TempDir()

		_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", dir)
		require.Error(t, err, tamper)
		assert.Contains(t, err.Error(), filepath.Base(tamper))
		assert.Contains(t, err.Error(), "checksum")
		assert.Empty(t, dirFiles(t, dir), tamper)
	}
}

func TestFetchMissingArch(t *testing.T) {
	st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
	dir := t.TempDir()
	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "arm", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no linux/arm build")
	assert.Empty(t, dirFiles(t, dir))
}

func TestFetchUnknownChannel(t *testing.T) {
	st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64")
	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "v9", "amd64", t.TempDir())
	require.Error(t, err)
	assert.EqualError(t, err, `channel "v9" not found for econumo`)
}

func TestFetchToolProject(t *testing.T) {
	st := publishFixture(t, "mytool", "mytool-linux-amd64", "mytool-linux-arm64")
	dir := t.TempDir()
	f, err := release.Fetch(context.Background(), st, layout.Ref{Project: "mytool"}, "v1", "amd64", dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "mytool-linux-amd64"), f.Binary)
	assert.Empty(t, f.Unit)
	assert.Empty(t, f.EnvExample)
	assert.Equal(t, []string{"mytool-linux-amd64"}, dirFiles(t, dir))
}

func TestFetchMalformedReleaseJSON(t *testing.T) {
	st := publishFixture(t, "mytool", "mytool-linux-amd64", "mytool-linux-arm64")
	// Replace release.json with garbage and re-list it in SHA256SUMS so only parsing fails.
	key := "mytool/latest/"
	bad := []byte("not json")
	o := st.Objects[key+"release.json"]
	o.Data = bad
	st.Objects[key+"release.json"] = o
	sm, err := sumsOf(st.Objects[key+"SHA256SUMS"].Data)
	require.NoError(t, err)
	sm["release.json"] = hexSum(bad)
	so := st.Objects[key+"SHA256SUMS"]
	so.Data = sm.Marshal()
	st.Objects[key+"SHA256SUMS"] = so

	dir := t.TempDir()
	_, err = release.Fetch(context.Background(), st, layout.Ref{Project: "mytool"}, "latest", "amd64", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "release.json")
	assert.Empty(t, dirFiles(t, dir))
}

func TestFetchMissingAsset(t *testing.T) {
	st := publishFixture(t, "winterflow", "winterflow-hub-linux-amd64", "winterflow-hub-linux-arm64", "winterflow-hub.service")
	dir := t.TempDir()
	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "winterflow", Component: "agent"}, "latest", "amd64", dir)
	require.Error(t, err)
	assert.EqualError(t, err, `no asset "winterflow-agent" in winterflow/latest`)
	assert.Empty(t, dirFiles(t, dir))
}

func TestFetchReleaseProjectMismatch(t *testing.T) {
	st := publishFixture(t, "mytool", "mytool-linux-amd64", "mytool-linux-arm64")
	// A release.json that names another project, listed correctly in SHA256SUMS.
	key := "mytool/latest/"
	other := []byte(`{"project":"other","version":"v1","published_at":"2026-10-29T12:00:00Z"}`)
	o := st.Objects[key+"release.json"]
	o.Data = other
	st.Objects[key+"release.json"] = o
	sm, err := sumsOf(st.Objects[key+"SHA256SUMS"].Data)
	require.NoError(t, err)
	sm["release.json"] = hexSum(other)
	so := st.Objects[key+"SHA256SUMS"]
	so.Data = sm.Marshal()
	st.Objects[key+"SHA256SUMS"] = so

	dir := t.TempDir()
	_, err = release.Fetch(context.Background(), st, layout.Ref{Project: "mytool"}, "latest", "amd64", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `names project "other"`)
	assert.Empty(t, dirFiles(t, dir))
}

func TestHostArch(t *testing.T) {
	assert.NotEmpty(t, release.HostArch())
}

func sumsOf(b []byte) (sums.Sums, error) { return sums.Parse(b) }

func hexSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
