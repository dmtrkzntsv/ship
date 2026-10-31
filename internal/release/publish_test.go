package release_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
	"github.com/dmtrkzntsv/ship/internal/sums"
)

const (
	immutableCC = "public, max-age=31536000, immutable"
	fixedCommit = "abc123"
)

var fixedNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	if len(names) == 0 {
		names = []string{"econumo-linux-amd64", "econumo-linux-arm64", "econumo.service"}
	}
	for _, n := range names {
		content := "data-" + n
		if n == "econumo.service" {
			content = "[Service]\nExecStart={{BIN}}\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte(content), 0o644))
	}
	return dir
}

func opts(dir string) release.PublishOptions {
	return release.PublishOptions{
		Project: "econumo", Dir: dir, Commit: fixedCommit,
		Now: func() time.Time { return fixedNow }, Out: &bytes.Buffer{}, Keep: 10,
	}
}

func lastTwo(t *testing.T, order []string, prefix string) (second, last string) {
	t.Helper()
	var keys []string
	for _, k := range order {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	require.GreaterOrEqual(t, len(keys), 2)
	return keys[len(keys)-2], keys[len(keys)-1]
}

func TestPublishVersionAndChannels(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	o.Version, o.Channels = "v1.0.0", []string{"latest"}
	require.NoError(t, release.Publish(context.Background(), m, o))

	for _, f := range []string{"econumo-linux-amd64", "econumo-linux-arm64", "econumo.service", "release.json", "SHA256SUMS"} {
		assert.Contains(t, m.Objects, "econumo/v1.0.0/"+f)
		assert.Contains(t, m.Objects, "econumo/latest/"+f)
		assert.Equal(t, immutableCC, m.Objects["econumo/v1.0.0/"+f].Opts.CacheControl)
		assert.Equal(t, "no-cache", m.Objects["econumo/latest/"+f].Opts.CacheControl)
		assert.Equal(t, "application/octet-stream", m.Objects["econumo/latest/"+f].Opts.ContentType)
	}
	for _, p := range []string{"econumo/v1.0.0/", "econumo/latest/"} {
		second, last := lastTwo(t, m.PutOrder, p)
		assert.Equal(t, p+"release.json", second)
		assert.Equal(t, p+"SHA256SUMS", last)

		var r release.Release
		require.NoError(t, json.Unmarshal(m.Objects[p+"release.json"].Data, &r))
		assert.Equal(t, release.Release{Project: "econumo", Version: "v1.0.0", Commit: fixedCommit, PublishedAt: fixedNow}, r)

		parsed, err := sums.Parse(m.Objects[p+"SHA256SUMS"].Data)
		require.NoError(t, err)
		assert.Len(t, parsed, 4)
		require.NoError(t, parsed.Verify("release.json", bytes.NewReader(m.Objects[p+"release.json"].Data)))
		assert.NotContains(t, parsed, "SHA256SUMS")
	}
	assert.Contains(t, o.Out.(*bytes.Buffer).String(), "latest")
}

func TestPublishRefusesExistingVersion(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	o.Version = "v1.0.0"
	require.NoError(t, release.Publish(context.Background(), m, o))
	m.PutOrder = nil

	// A different build of the same version.
	o = opts(fixture(t, "econumo-linux-amd64", "econumo-linux-arm64"))
	o.Version, o.Channels = "v1.0.0", []string{"latest"}
	err := release.Publish(context.Background(), m, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already published")
	assert.Contains(t, err.Error(), "publish a new version")
	assert.Empty(t, m.PutOrder)
}

func TestPublishRetriesIncompleteVersion(t *testing.T) {
	m := storetest.NewMem()
	// A failed earlier publish left files but no SHA256SUMS.
	m.Objects["econumo/v1.0.0/econumo-linux-amd64"] = storetest.MemObject{Data: []byte("partial")}
	m.Objects["econumo/v1.0.0/stray"] = storetest.MemObject{Data: []byte("x")}
	o := opts(fixture(t))
	o.Version = "v1.0.0"
	require.NoError(t, release.Publish(context.Background(), m, o))

	assert.NotContains(t, m.Objects, "econumo/v1.0.0/stray")
	assert.Equal(t, "data-econumo-linux-amd64", string(m.Objects["econumo/v1.0.0/econumo-linux-amd64"].Data))
	assert.Contains(t, m.Objects, "econumo/v1.0.0/SHA256SUMS")
	assert.Contains(t, o.Out.(*bytes.Buffer).String(), "incomplete")
}

func TestPublishSameBuildContinuesToChannels(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	o.Version = "v1.0.0"
	require.NoError(t, release.Publish(context.Background(), m, o))
	sumsBefore := m.Objects["econumo/v1.0.0/SHA256SUMS"].Data
	m.PutOrder = nil

	// The same build again (e.g. a retry after the channel upload failed),
	// published at a different time.
	o = opts(fixture(t))
	o.Version, o.Channels = "v1.0.0", []string{"latest"}
	o.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	require.NoError(t, release.Publish(context.Background(), m, o))

	for _, k := range m.PutOrder {
		assert.NotContains(t, k, "econumo/v1.0.0/", "the version folder must not be re-uploaded")
	}
	assert.Equal(t, sumsBefore, m.Objects["econumo/v1.0.0/SHA256SUMS"].Data)
	assert.Contains(t, m.Objects, "econumo/latest/SHA256SUMS")
	assert.Contains(t, o.Out.(*bytes.Buffer).String(), "already published, continuing")
}

func TestPublishChannelOnly(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	o.Channels = []string{"dev"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	for k := range m.Objects {
		assert.Contains(t, k, "econumo/dev/")
	}
	var r release.Release
	require.NoError(t, json.Unmarshal(m.Objects["econumo/dev/release.json"].Data, &r))
	assert.Equal(t, "dev", r.Version)
}

func TestPublishRemovesStaleChannelFiles(t *testing.T) {
	m := storetest.NewMem()
	m.Objects["econumo/latest/old-file"] = storetest.MemObject{Data: []byte("x")}
	m.Objects["econumo/v0.9.0/keep-me"] = storetest.MemObject{Data: []byte("x")}
	o := opts(fixture(t))
	o.Channels = []string{"latest"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	assert.NotContains(t, m.Objects, "econumo/latest/old-file")
	assert.Contains(t, m.Objects, "econumo/v0.9.0/keep-me")
	assert.Contains(t, m.Objects, "econumo/latest/SHA256SUMS")
}

func TestPublishValidatesBeforeUpload(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t, "econumo-linux-amd64", "econumo.service"))
	o.Version = "v1.0.0"
	require.Error(t, release.Publish(context.Background(), m, o))
	assert.Empty(t, m.PutOrder)
}

func TestPublishRequiresTarget(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	require.Error(t, release.Publish(context.Background(), m, o))

	o.Channels = []string{"beta"}
	err := release.Publish(context.Background(), m, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only latest and dev")
	assert.Empty(t, m.PutOrder)
}

func TestPublishReadOnlyKeyMessage(t *testing.T) {
	m := storetest.NewMem()
	m.ReadOnly = true
	o := opts(fixture(t))
	o.Version = "v1.0.0"
	err := release.Publish(context.Background(), m, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	assert.Contains(t, err.Error(), "SHIP_R2_")
	assert.ErrorIs(t, err, store.ErrAccessDenied)
}
