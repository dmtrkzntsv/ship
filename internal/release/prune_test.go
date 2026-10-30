package release_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// seed writes a version folder with a binary and a release.json.
func seed(t *testing.T, m *storetest.Mem, channel, version string, at time.Time, size int) {
	t.Helper()
	rel, err := json.Marshal(release.Release{Project: "econumo", Version: version, PublishedAt: at})
	require.NoError(t, err)
	p := "econumo/" + channel + "/"
	m.Objects[p+"econumo-linux-amd64"] = storetest.MemObject{Data: bytes.Repeat([]byte("x"), size), LastModified: at}
	m.Objects[p+"release.json"] = storetest.MemObject{Data: rel, LastModified: at}
	m.Objects[p+"SHA256SUMS"] = storetest.MemObject{Data: []byte("s"), LastModified: at}
}

// fixture: v1..v13 one day apart, latest -> v13, dev -> v2.
func prunable(t *testing.T) *storetest.Mem {
	t.Helper()
	m := storetest.NewMem()
	for i := 1; i <= 13; i++ {
		seed(t, m, fmt.Sprintf("v%d", i), fmt.Sprintf("v%d", i), base.AddDate(0, 0, i-1), 100)
	}
	seed(t, m, "latest", "v13", base.AddDate(0, 0, 12), 100)
	seed(t, m, "dev", "v2", base.AddDate(0, 0, 1), 100)
	return m
}

func names(vs []release.VersionInfo) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Channel)
	}
	return out
}

func TestPruneKeepsNewestAndProtected(t *testing.T) {
	m := prunable(t)
	plan, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: 10, Now: func() time.Time { return fixedNow }})
	require.NoError(t, err)
	assert.Equal(t, []string{"v1"}, names(plan.Delete))
	assert.Equal(t, []string{"v12", "v11", "v10", "v9", "v8", "v7", "v6", "v5", "v4", "v3"}, names(plan.Keep))
	assert.Equal(t, []string{"v13", "v2"}, plan.Protected)
	assert.Contains(t, m.Objects, "econumo/v1/release.json", "dry run must not delete")
}

func TestPruneDryRun(t *testing.T) {
	m := prunable(t)
	before := len(m.Objects)
	var out bytes.Buffer
	_, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: 10, Out: &out})
	require.NoError(t, err)
	assert.Len(t, m.Objects, before)
	assert.Contains(t, out.String(), "v1")
	size := 100 + len(m.Objects["econumo/v1/release.json"].Data) + 1
	assert.Contains(t, out.String(), fmt.Sprintf("%d bytes", size))
}

func TestPruneYesDeletes(t *testing.T) {
	m := prunable(t)
	_, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: 10, Yes: true})
	require.NoError(t, err)
	for k := range m.Objects {
		assert.False(t, strings.HasPrefix(k, "econumo/v1/"), k)
	}
	assert.Contains(t, m.Objects, "econumo/v2/release.json")
}

func TestPruneOlderThan(t *testing.T) {
	m := prunable(t)
	now := base.AddDate(0, 0, 13) // v13 + 1 day
	plan, err := release.Prune(context.Background(), m, release.PruneOptions{
		Project: "econumo", Keep: 2, OlderThan: 10 * 24 * time.Hour, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"v3", "v1"}, names(plan.Delete))
}

func TestPruneNeverTouchesMoving(t *testing.T) {
	for _, keep := range []int{0, 1, 10} {
		m := prunable(t)
		_, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: keep, Yes: true})
		require.NoError(t, err)
		for _, ch := range []string{"latest", "dev"} {
			for _, f := range []string{"econumo-linux-amd64", "release.json", "SHA256SUMS"} {
				assert.Contains(t, m.Objects, "econumo/"+ch+"/"+f, "keep=%d", keep)
			}
		}
		assert.Contains(t, m.Objects, "econumo/v13/release.json")
		assert.Contains(t, m.Objects, "econumo/v2/release.json")
	}
}

func TestPruneProtectsMissingVersionName(t *testing.T) {
	m := prunable(t)
	seed(t, m, "dev", "v0.9", base, 1) // points to a version that does not exist
	plan, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: 10})
	require.NoError(t, err)
	assert.Contains(t, plan.Protected, "v0.9")
	assert.Equal(t, []string{"v1", "v2"}, append(names(plan.Delete)[:0:0], sortedAsc(names(plan.Delete))...))
}

func sortedAsc(s []string) []string {
	out := append([]string{}, s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestPruneEstimatedWhenNoReleaseJSON(t *testing.T) {
	m := prunable(t)
	// legacy folder with no release.json, newest object is the oldest of all
	m.Objects["econumo/old/econumo-linux-amd64"] = storetest.MemObject{Data: []byte("zz"), LastModified: base.AddDate(0, 0, -5)}
	vs, err := release.Versions(context.Background(), m, "econumo")
	require.NoError(t, err)
	require.Len(t, vs, 16)
	last := vs[len(vs)-1]
	assert.Equal(t, "old", last.Channel)
	assert.True(t, last.Estimated)
	assert.Nil(t, last.Release)
	assert.Equal(t, base.AddDate(0, 0, -5), last.PublishedAt)
	assert.False(t, vs[0].Estimated)

	plan, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{"v1", "old"}, names(plan.Delete))
}

func TestVersionsNewestFirstTieBreak(t *testing.T) {
	m := storetest.NewMem()
	seed(t, m, "a", "a", base, 1)
	seed(t, m, "b", "b", base, 1)
	seed(t, m, "latest", "b", base.AddDate(0, 0, 1), 1)
	vs, err := release.Versions(context.Background(), m, "econumo")
	require.NoError(t, err)
	assert.Equal(t, []string{"latest", "b", "a"}, names(vs))
	assert.Equal(t, int64(1+len(m.Objects["econumo/b/release.json"].Data)+1), vs[1].Bytes)
	assert.Len(t, vs[1].Keys, 3)
}

func TestPublishAutoPrunes(t *testing.T) {
	setup := func() *storetest.Mem {
		m := storetest.NewMem()
		for i := 1; i <= 3; i++ {
			seed(t, m, fmt.Sprintf("v%d", i), fmt.Sprintf("v%d", i), base.AddDate(0, 0, i-1), 10)
		}
		return m
	}
	publishV4 := func(m *storetest.Mem, keep int) error {
		o := opts(fixture(t))
		o.Version, o.Keep = "v4", keep
		return release.Publish(context.Background(), m, o)
	}

	m := setup()
	require.NoError(t, publishV4(m, 2))
	for _, v := range []string{"v1", "v2"} {
		assert.NotContains(t, m.Objects, "econumo/"+v+"/release.json")
	}
	assert.Contains(t, m.Objects, "econumo/v3/release.json")
	assert.Contains(t, m.Objects, "econumo/v4/release.json")

	m = setup()
	require.NoError(t, publishV4(m, 0))
	assert.Contains(t, m.Objects, "econumo/v1/release.json")

	m = setup()
	boom := errors.New("boom")
	m.FailDelete = func(string) error { return boom }
	err := publishV4(m, 2)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, m.Objects, "econumo/v4/release.json")
	assert.Contains(t, m.Objects, "econumo/v4/SHA256SUMS")

	m = setup()
	m.FailDelete = func(string) error { return fmt.Errorf("x: %w", store.ErrAccessDenied) }
	err = publishV4(m, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}
