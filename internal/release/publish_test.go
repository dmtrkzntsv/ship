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
	for p, channel := range map[string]string{"econumo/v1.0.0/": "", "econumo/latest/": "latest"} {
		second, last := lastTwo(t, m.PutOrder, p)
		assert.Equal(t, p+"release.json", second)
		assert.Equal(t, p+"SHA256SUMS", last)

		var r release.Release
		require.NoError(t, json.Unmarshal(m.Objects[p+"release.json"].Data, &r))
		assert.Equal(t, release.Release{Project: "econumo", Version: "v1.0.0", Channel: channel, Commit: fixedCommit, PublishedAt: fixedNow}, r)

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
	assert.Equal(t, "dev", r.Channel)
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

	for _, bad := range []string{"1.2.3", "Edge", "edge.1", "a/b"} {
		o.Channels = []string{bad}
		err := release.Publish(context.Background(), m, o)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "invalid channel name", bad)
	}

	o.Version, o.Channels = "edge", []string{"latest", "edge"}
	err := release.Publish(context.Background(), m, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both --version and --channels")
	assert.Empty(t, m.PutOrder)
}

func readRelease(t *testing.T, m *storetest.Mem, key string) release.Release {
	t.Helper()
	var r release.Release
	require.NoError(t, json.Unmarshal(m.Objects[key].Data, &r))
	return r
}

func TestPublishCustomChannel(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	o.Channels = []string{"edge"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	for _, f := range []string{"econumo-linux-amd64", "econumo-linux-arm64", "econumo.service", "release.json", "SHA256SUMS"} {
		assert.Equal(t, "no-cache", m.Objects["econumo/edge/"+f].Opts.CacheControl, f)
	}
	r := readRelease(t, m, "econumo/edge/release.json")
	assert.Equal(t, "edge", r.Channel)
	assert.Equal(t, "edge", r.Version)
	assert.Contains(t, o.Out.(*bytes.Buffer).String(), "updated channel econumo/edge/")

	// Moving: a new build replaces it.
	o = opts(fixture(t, "econumo-linux-amd64", "econumo-linux-arm64"))
	o.Channels = []string{"edge"}
	o.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	require.NoError(t, release.Publish(context.Background(), m, o))
	assert.NotContains(t, m.Objects, "econumo/edge/econumo.service")
	assert.Equal(t, fixedNow.Add(time.Hour), readRelease(t, m, "econumo/edge/release.json").PublishedAt)

	// A release moves it along with latest; the version folder has no marker.
	o = opts(fixture(t))
	o.Version, o.Channels = "v1.0.0", []string{"latest", "edge"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	assert.Equal(t, release.Release{Project: "econumo", Version: "v1.0.0", Channel: "edge", Commit: fixedCommit, PublishedAt: fixedNow}, readRelease(t, m, "econumo/edge/release.json"))
	assert.Equal(t, "", readRelease(t, m, "econumo/v1.0.0/release.json").Channel)
}

func TestPublishChannelRefusesVersionFolder(t *testing.T) {
	ctx := context.Background()
	for name, seed := range map[string]func(m *storetest.Mem){
		"published version": func(m *storetest.Mem) {
			o := opts(fixture(t))
			o.Version = "edge"
			require.NoError(t, release.Publish(ctx, m, o))
		},
		// Published before ship wrote release.json.
		"version without release.json": func(m *storetest.Mem) {
			m.Objects["econumo/edge/econumo-linux-amd64"] = storetest.MemObject{Data: []byte("x")}
			m.Objects["econumo/edge/SHA256SUMS"] = storetest.MemObject{Data: []byte("x")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := storetest.NewMem()
			seed(m)
			m.PutOrder = nil
			o := opts(fixture(t))
			o.Channels = []string{"latest", "edge"}
			err := release.Publish(ctx, m, o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "econumo/edge is already published as an immutable version")
			assert.Empty(t, m.PutOrder, "nothing may be uploaded, not even latest")
		})
	}
}

func TestPublishChannelReplacesIncompleteFolder(t *testing.T) {
	m := storetest.NewMem()
	// A first edge publish that failed before release.json.
	m.Objects["econumo/edge/econumo-linux-amd64"] = storetest.MemObject{Data: []byte("partial")}
	m.Objects["econumo/edge/stray"] = storetest.MemObject{Data: []byte("x")}
	o := opts(fixture(t))
	o.Channels = []string{"edge"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	assert.NotContains(t, m.Objects, "econumo/edge/stray")
	assert.Equal(t, "edge", readRelease(t, m, "econumo/edge/release.json").Channel)
}

func TestPublishBuiltinChannelOverOldFolder(t *testing.T) {
	m := storetest.NewMem()
	// latest as published by a ship without the channel field.
	rel, err := json.Marshal(release.Release{Project: "econumo", Version: "v0.9.0", PublishedAt: fixedNow})
	require.NoError(t, err)
	m.Objects["econumo/latest/release.json"] = storetest.MemObject{Data: rel}
	m.Objects["econumo/latest/SHA256SUMS"] = storetest.MemObject{Data: []byte("x")}
	o := opts(fixture(t))
	o.Version, o.Channels = "v1.0.0", []string{"latest"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	assert.Equal(t, "latest", readRelease(t, m, "econumo/latest/release.json").Channel)
}

func TestPublishVersionRefusesChannelFolder(t *testing.T) {
	m := storetest.NewMem()
	o := opts(fixture(t))
	o.Channels = []string{"edge"}
	require.NoError(t, release.Publish(context.Background(), m, o))
	m.PutOrder = nil

	o = opts(fixture(t))
	o.Version = "edge"
	err := release.Publish(context.Background(), m, o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "econumo/edge is a moving channel")
	assert.Empty(t, m.PutOrder)
	assert.Equal(t, "edge", readRelease(t, m, "econumo/edge/release.json").Channel)
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
