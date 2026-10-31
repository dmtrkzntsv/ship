// Package cache keeps a small listing of what is published in R2 (projects,
// channels, components) so shell completion works without credentials.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/sums"
)

// DefaultPath is where the listing lives on a host.
const DefaultPath = "/var/cache/ship/listing.json"

// TTL is how long a listing counts as fresh.
const TTL = 5 * time.Minute

// refreshTimeout bounds the network refresh done from Get (completion).
const refreshTimeout = 2 * time.Second

type Listing struct {
	FetchedAt time.Time               `json:"fetched_at"`
	Projects  map[string]ProjectEntry `json:"projects"`
}

type ProjectEntry struct {
	Channels   []string `json:"channels"`
	Components []string `json:"components,omitempty"`
	// Root is true when the project publishes its own <project>.service.
	Root bool `json:"root,omitempty"`
}

// Refresh lists every project in the store. Components are the "<project>-X"
// units in the SHA256SUMS of the project's newest channel: latest, else the
// newest version by name, else dev.
func Refresh(ctx context.Context, st store.Store) (Listing, error) {
	projects, err := st.ListDirs(ctx, "")
	if err != nil {
		return Listing{}, err
	}
	l := Listing{FetchedAt: time.Now().UTC(), Projects: map[string]ProjectEntry{}}
	for _, p := range projects {
		if !layout.ValidName(p) {
			continue
		}
		channels, err := st.ListDirs(ctx, p+"/")
		if err != nil {
			return Listing{}, err
		}
		e := ProjectEntry{Channels: channels}
		if newest := newestChannel(channels); newest != "" {
			if e.Components, e.Root, err = units(ctx, st, p, newest); err != nil {
				return Listing{}, err
			}
		}
		l.Projects[p] = e
	}
	return l, nil
}

func newestChannel(channels []string) string {
	newest := ""
	for _, c := range channels {
		if c == "latest" {
			return c
		}
		if !layout.IsMoving(c) {
			newest = c // channels are sorted by name; the last one wins
		}
	}
	if newest == "" {
		for _, c := range channels {
			if c == "dev" {
				return c
			}
		}
	}
	return newest
}

func units(ctx context.Context, st store.Store, project, channel string) (components []string, root bool, err error) {
	rc, err := st.Get(ctx, project+"/"+channel+"/"+release.SumsFile)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, false, err
	}
	s, err := sums.Parse(raw)
	if err != nil {
		return nil, false, nil // a broken release must not break the listing
	}
	for name := range s {
		asset, ok := strings.CutSuffix(name, ".service")
		if !ok {
			continue
		}
		if asset == project {
			root = true
		} else if comp, ok := strings.CutPrefix(asset, project+"-"); ok && layout.ValidName(comp) {
			components = append(components, comp)
		}
	}
	sort.Strings(components)
	return components, root, nil
}

// Load reads a listing file.
func Load(path string) (Listing, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Listing{}, err
	}
	var l Listing
	if err := json.Unmarshal(b, &l); err != nil {
		return Listing{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if l.Projects == nil {
		l.Projects = map[string]ProjectEntry{}
	}
	return l, nil
}

// Save writes a listing file (0644, directory 0755) atomically.
func Save(path string, l Listing) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".listing-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Get returns the listing at path. When it is missing or older than TTL and a
// store is available it refreshes (2s timeout) and saves the result if the
// path is writable. Any failure yields whatever is on disk (possibly empty).
func Get(ctx context.Context, path string, now time.Time, newStore func() (store.Store, error)) Listing {
	disk, err := Load(path)
	if err == nil && now.Sub(disk.FetchedAt) < TTL {
		return disk
	}
	if err != nil {
		disk = Listing{Projects: map[string]ProjectEntry{}}
	}
	st, serr := newStore()
	if serr != nil {
		return disk
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	fresh, rerr := Refresh(ctx, st)
	if rerr != nil {
		return disk
	}
	fresh.FetchedAt = now.UTC()
	_ = Save(path, fresh)
	return fresh
}
