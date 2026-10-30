package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/sums"
)

const (
	immutableCacheControl = "public, max-age=31536000, immutable"
	movingCacheControl    = "no-cache"
	contentType           = "application/octet-stream"
)

// PublishOptions configures Publish.
type PublishOptions struct {
	Project, Dir, Version string
	Channels              []string
	Keep                  int
	Commit                string // `git rev-parse HEAD` of the build, when known
	Now                   func() time.Time
	Out                   io.Writer
}

// Publish uploads the files in o.Dir to <project>/<version>/ (if a version is
// given) and to each moving channel. Everything is validated before the first
// upload; SHA256SUMS is always uploaded last within a folder.
func Publish(ctx context.Context, st store.Store, o PublishOptions) error {
	if o.Version == "" && len(o.Channels) == 0 {
		return errors.New("nothing to publish: give --version and/or --channels")
	}
	if o.Version != "" {
		if err := layout.ValidateVersion(o.Version); err != nil {
			return err
		}
	}
	for _, c := range o.Channels {
		if !layout.IsMoving(c) {
			return fmt.Errorf("channel %q is not a moving channel: --channels accepts only latest and dev (use --version for an immutable release)", c)
		}
	}
	if !layout.ValidName(o.Project) {
		return fmt.Errorf("invalid project name %q", o.Project)
	}
	_, files, err := layout.ScanDir(o.Project, o.Dir)
	if err != nil {
		return err
	}

	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	publishedAt := now().UTC()
	if o.Out == nil {
		o.Out = io.Discard
	}

	base, err := sums.Compute(o.Dir, files)
	if err != nil {
		return err
	}

	skipVersion := false
	if o.Version != "" {
		if skipVersion, err = checkExistingVersion(ctx, st, o, base); err != nil {
			return err
		}
	}

	// Each target folder gets its own release.json (the version field is the
	// channel name when no --version is given), hence its own SHA256SUMS.
	upload := func(prefix, version, cacheControl string) error {
		rel, err := json.MarshalIndent(Release{Project: o.Project, Version: version, Commit: o.Commit, PublishedAt: publishedAt}, "", "  ")
		if err != nil {
			return err
		}
		rel = append(rel, '\n')
		sm := sums.Sums{}
		for k, v := range base {
			sm[k] = v
		}
		digest := sha256.Sum256(rel)
		sm[ReleaseFile] = hex.EncodeToString(digest[:])

		put := store.PutOptions{CacheControl: cacheControl, ContentType: contentType}
		for _, f := range files {
			if err := putFile(ctx, st, prefix+f, filepath.Join(o.Dir, f), put); err != nil {
				return err
			}
		}
		if err := putBytes(ctx, st, prefix+ReleaseFile, rel, put); err != nil {
			return err
		}
		return putBytes(ctx, st, prefix+SumsFile, sm.Marshal(), put)
	}

	if o.Version != "" && !skipVersion {
		prefix := o.Project + "/" + o.Version + "/"
		if err := upload(prefix, o.Version, immutableCacheControl); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "published %s\n", prefix)
	}
	for _, c := range o.Channels {
		prefix := o.Project + "/" + c + "/"
		version := o.Version
		if version == "" {
			version = c
		}
		if err := upload(prefix, version, movingCacheControl); err != nil {
			return err
		}
		if err := removeStale(ctx, st, prefix, append(append([]string{}, files...), ReleaseFile, SumsFile)); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "updated channel %s\n", prefix)
	}

	return pruneAfterPublish(ctx, st, o)
}

// checkExistingVersion looks at an existing <project>/<version>/ folder.
// One without SHA256SUMS is left over from a failed publish: its objects are
// deleted and the version is published normally. One whose SHA256SUMS lists
// exactly the files of this build (release.json aside) is already published:
// skip reports that its upload can be skipped. Anything else is refused,
// because versions are immutable.
func checkExistingVersion(ctx context.Context, st store.Store, o PublishOptions, base sums.Sums) (skip bool, err error) {
	prefix := o.Project + "/" + o.Version + "/"
	existing, err := st.List(ctx, prefix)
	if err != nil || len(existing) == 0 {
		return false, err
	}
	raw, err := readAll(ctx, st, prefix+SumsFile)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(o.Out, "%s is incomplete (no %s): deleting it and publishing again\n", prefix, SumsFile)
		keys := make([]string, 0, len(existing))
		for _, ob := range existing {
			keys = append(keys, ob.Key)
		}
		return false, wrapAccess(st.Delete(ctx, keys))
	}
	if err != nil {
		return false, err
	}
	if old, perr := sums.Parse(raw); perr == nil && sameBuild(old, base) {
		fmt.Fprintf(o.Out, "%s already published, continuing\n", prefix)
		return true, nil
	}
	return false, fmt.Errorf("%s/%s is already published with different files; versions are immutable, publish a new version", o.Project, o.Version)
}

// sameBuild reports whether published lists exactly the files and digests
// of build, ignoring release.json.
func sameBuild(published, build sums.Sums) bool {
	n := 0
	for name, digest := range published {
		if name == ReleaseFile {
			continue
		}
		if build[name] != digest {
			return false
		}
		n++
	}
	return n == len(build)
}

// pruneAfterPublish runs version pruning (--keep) once a publish has fully
// succeeded. The version just published is protected, so a clock running
// behind the one that published older versions cannot get it deleted. A
// failure is returned (Prune already explains a read-only key), but the
// publish itself stays done.
func pruneAfterPublish(ctx context.Context, st store.Store, o PublishOptions) error {
	if o.Keep <= 0 {
		return nil
	}
	po := PruneOptions{Project: o.Project, Keep: o.Keep, Yes: true, Now: o.Now, Out: o.Out}
	if o.Version != "" {
		po.Protect = []string{o.Version}
	}
	_, err := Prune(ctx, st, po)
	return err
}

func removeStale(ctx context.Context, st store.Store, prefix string, keep []string) error {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[prefix+k] = true
	}
	objs, err := st.List(ctx, prefix)
	if err != nil {
		return err
	}
	var stale []string
	for _, ob := range objs {
		if !keepSet[ob.Key] {
			stale = append(stale, ob.Key)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return wrapAccess(st.Delete(ctx, stale))
}

func putFile(ctx context.Context, st store.Store, key, path string, o store.PutOptions) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return wrapAccess(st.Put(ctx, key, f, fi.Size(), o))
}

func putBytes(ctx context.Context, st store.Store, key string, b []byte, o store.PutOptions) error {
	return wrapAccess(st.Put(ctx, key, bytes.NewReader(b), int64(len(b)), o))
}

func wrapAccess(err error) error {
	if errors.Is(err, store.ErrAccessDenied) {
		return fmt.Errorf("R2 refused the write: the key is read-only; publish and prune need a write key (SHIP_R2_ACCESS_KEY_ID / SHIP_R2_SECRET_ACCESS_KEY): %w", err)
	}
	return err
}
