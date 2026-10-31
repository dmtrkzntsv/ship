package release

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/dmtrkzntsv/ship/internal/store"
)

// VersionInfo describes one channel folder (immutable version or moving channel).
type VersionInfo struct {
	Channel     string
	Release     *Release  // nil if release.json is missing or unreadable
	PublishedAt time.Time // from Release, else newest LastModified
	Estimated   bool      // true when PublishedAt came from LastModified
	Bytes       int64
	Keys        []string
}

// Versions lists every channel folder of project, newest first (ties broken by
// channel name, descending). Moving channels are included.
func Versions(ctx context.Context, st store.Store, project string) ([]VersionInfo, error) {
	dirs, err := st.ListDirs(ctx, project+"/")
	if err != nil {
		return nil, err
	}
	out := make([]VersionInfo, 0, len(dirs))
	for _, d := range dirs {
		prefix := project + "/" + d + "/"
		objs, err := st.List(ctx, prefix)
		if err != nil {
			return nil, err
		}
		vi := VersionInfo{Channel: d}
		var newest time.Time
		for _, ob := range objs {
			vi.Keys = append(vi.Keys, ob.Key)
			vi.Bytes += ob.Size
			if ob.LastModified.After(newest) {
				newest = ob.LastModified
			}
		}
		if len(objs) == 0 {
			continue
		}
		if rel, err := readRelease(ctx, st, prefix+ReleaseFile); err != nil {
			return nil, err
		} else if rel != nil {
			vi.Release, vi.PublishedAt = rel, rel.PublishedAt
		} else {
			vi.PublishedAt, vi.Estimated = newest, true
		}
		out = append(out, vi)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].PublishedAt.Equal(out[j].PublishedAt) {
			return out[i].PublishedAt.After(out[j].PublishedAt)
		}
		return out[i].Channel > out[j].Channel
	})
	return out, nil
}

// readRelease returns nil (no error) when the file is missing or not valid JSON.
func readRelease(ctx context.Context, st store.Store, key string) (*Release, error) {
	rc, err := st.Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var rel Release
	if json.Unmarshal(b, &rel) != nil || rel.PublishedAt.IsZero() {
		return nil, nil
	}
	return &rel, nil
}
