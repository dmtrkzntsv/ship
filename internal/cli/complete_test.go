package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/cache"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
)

// setupCompletion points the completion seam at temp files: a fresh cache
// listing and a state file with the given service -> refs.
func setupCompletion(t *testing.T, st map[string][]string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	require.NoError(t, cache.Save(filepath.Join(dir, "listing.json"), cache.Listing{
		FetchedAt: now,
		Projects: map[string]cache.ProjectEntry{
			"econumo":    {Channels: []string{"dev", "latest", "v1.0.0"}, Root: true},
			"winterflow": {Channels: []string{"latest", "v2"}, Components: []string{"hub", "agent"}},
		},
	}))
	s := &state.State{Services: map[string]*state.Service{}}
	for svc, refs := range st {
		sv := s.Service(svc)
		for _, r := range refs {
			sv.Refs[r] = &state.RefState{Unit: svc, Channel: "latest", Version: "v1"}
		}
	}
	sf := state.File{Path: filepath.Join(dir, "state.json")}
	require.NoError(t, sf.Save(s))

	old := completionEnv
	completionEnv = completionConfig{
		statePath: sf.Path,
		cachePath: filepath.Join(dir, "listing.json"),
		newStore:  func() (store.Store, error) { return nil, errors.New("offline") },
		now:       func() time.Time { return now },
	}
	t.Cleanup(func() { completionEnv = old })
}

// complete runs cobra's hidden __complete and returns the suggestions.
func complete(t *testing.T, args ...string) []string {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(&out, &errOut)
	root.SetArgs(append([]string{"__complete"}, args...))
	require.NoError(t, root.Execute())
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.HasPrefix(l, ":") {
			assert.Equal(t, ":4", l, "want ShellCompDirectiveNoFileComp")
			continue
		}
		if l == "" || strings.HasPrefix(l, "-") {
			continue // cobra itself offers required flags next to the arguments
		}
		got = append(got, strings.SplitN(l, "\t", 2)[0])
	}
	return got
}

func TestCompleteRefs(t *testing.T) {
	setupCompletion(t, nil)
	assert.Equal(t, []string{"econumo", "winterflow/agent", "winterflow/hub"}, complete(t, "install", ""))
	assert.Equal(t, []string{"winterflow/agent"}, complete(t, "install", "winterflow/a"))
	assert.Empty(t, complete(t, "install", "econumo", ""), "only the first argument is a ref")
}

func TestCompleteServicesFromState(t *testing.T) {
	setupCompletion(t, map[string][]string{"econumo": {"econumo"}, "econumo-beta": {"econumo"}})
	assert.Equal(t, []string{"econumo", "econumo-beta"}, complete(t, "update", "--service", ""))
	assert.Equal(t, []string{"econumo", "econumo-beta", "winterflow"}, complete(t, "install", "winterflow", "--service", ""))
	assert.Equal(t, []string{"econumo", "econumo-beta"}, complete(t, "status", "--service", ""))
}

func TestCompleteVersions(t *testing.T) {
	setupCompletion(t, nil)
	assert.Equal(t, []string{"dev", "latest", "v1.0.0"}, complete(t, "install", "econumo", "--version", ""))
	assert.Equal(t, []string{"latest", "v2"}, complete(t, "install", "winterflow/hub", "--version", ""))
	assert.Empty(t, complete(t, "install", "--version", ""))
}

func TestCompleteInstalledRefs(t *testing.T) {
	setupCompletion(t, map[string][]string{
		"twillingate": {"twillingate", "twillingate-sql"},
		"econumo":     {"econumo"},
	})
	assert.Equal(t, []string{"twillingate", "twillingate-sql"}, complete(t, "update", "--service", "twillingate", ""))
	assert.Equal(t, []string{"econumo", "twillingate", "twillingate-sql"}, complete(t, "rollback", ""))
	assert.Equal(t, []string{"econumo"}, complete(t, "logs", "--service", "econumo", ""))
}

func TestCompleteProjectsForVersionsAndPrune(t *testing.T) {
	setupCompletion(t, nil)
	assert.Equal(t, []string{"econumo", "winterflow"}, complete(t, "versions", ""))
	assert.Equal(t, []string{"econumo", "winterflow"}, complete(t, "prune", ""))
}

func TestCompleteNeverFailsWithoutStateOrCache(t *testing.T) {
	dir := t.TempDir()
	old := completionEnv
	completionEnv = completionConfig{
		statePath: filepath.Join(dir, "none.json"), cachePath: filepath.Join(dir, "none-cache.json"),
		newStore: func() (store.Store, error) { return nil, errors.New("no creds") },
		now:      time.Now,
	}
	t.Cleanup(func() { completionEnv = old })
	assert.Empty(t, complete(t, "install", ""))
	assert.Empty(t, complete(t, "update", "--service", ""))
}

func TestCompletionCommandGeneratesScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		var out bytes.Buffer
		root := NewRoot(&out, &out)
		root.SetArgs([]string{"completion", sh})
		require.NoError(t, root.Execute(), sh)
		assert.Contains(t, out.String(), "ship", sh)
	}
}

func TestRefreshListingSavesAndIgnoresErrors(t *testing.T) {
	m := storetest.NewMem()
	m.Objects["econumo/dev/econumo.service"] = storetest.MemObject{Data: []byte("x")}
	p := filepath.Join(t.TempDir(), "listing.json")
	refreshListingTo(context.Background(), m, p)
	l, err := cache.Load(p)
	require.NoError(t, err)
	assert.Contains(t, l.Projects, "econumo")

	// unwritable path: must not panic or report
	refreshListingTo(context.Background(), m, filepath.Join(p, "x", "l.json"))
}

func TestCompleteEdgeCases(t *testing.T) {
	setupCompletion(t, map[string][]string{"econumo": {"econumo"}})
	assert.Empty(t, complete(t, "versions", "econumo", ""), "only the first argument is a project")
	assert.Empty(t, complete(t, "update", "econumo", ""), "only the first argument is a ref")
	assert.Empty(t, complete(t, "install", "Bad/Ref/x", "--version", ""), "no channels for an invalid ref")

	// A corrupt state file yields no installed refs or services.
	require.NoError(t, os.WriteFile(completionEnv.statePath, []byte("{broken"), 0o644))
	assert.Empty(t, complete(t, "update", ""))
	assert.Empty(t, complete(t, "update", "--service", ""))
}
