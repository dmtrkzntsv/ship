package release

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseJSON(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC)
	r := Release{Project: "econumo", Version: "1.2.3", Commit: "abc123", PublishedAt: at}
	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"published_at":"2026-10-29T12:30:00Z"`)

	var back Release
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, r, back)

	b, err = json.Marshal(Release{Project: "p", Version: "v", PublishedAt: at})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "commit")
}

func TestFileNames(t *testing.T) {
	assert.Equal(t, "release.json", ReleaseFile)
	assert.Equal(t, "SHA256SUMS", SumsFile)
}
