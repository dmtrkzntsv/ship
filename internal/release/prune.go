package release

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// PruneOptions configures Prune. With Yes false nothing is deleted.
type PruneOptions struct {
	Project   string
	Keep      int
	OlderThan time.Duration // 0: no age filter
	Yes       bool
	Now       func() time.Time
	Out       io.Writer
}

// PrunePlan is the outcome of a prune: Delete and Keep are immutable versions
// (newest first); Protected names versions pointed to by a moving channel.
type PrunePlan struct {
	Delete, Keep []VersionInfo
	Protected    []string
}

// Prune deletes old immutable versions of a project: it keeps the newest
// o.Keep (versions a moving channel points to are protected and do not count),
// and with OlderThan deletes only those that are also older than that.
// Moving channel folders are never touched.
func Prune(ctx context.Context, st store.Store, o PruneOptions) (PrunePlan, error) {
	var plan PrunePlan
	if !layout.ValidName(o.Project) {
		return plan, fmt.Errorf("invalid project name %q", o.Project)
	}
	if o.Keep < 0 {
		return plan, fmt.Errorf("keep must be >= 0")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	all, err := Versions(ctx, st, o.Project)
	if err != nil {
		return plan, err
	}

	pointed := map[string]bool{}
	for _, v := range all {
		if layout.IsMoving(v.Channel) && v.Release != nil && !layout.IsMoving(v.Release.Version) {
			pointed[v.Release.Version] = true
		}
	}
	var candidates []VersionInfo
	for _, v := range all {
		switch {
		case layout.IsMoving(v.Channel):
		case pointed[v.Channel]:
			plan.Protected = append(plan.Protected, v.Channel)
			delete(pointed, v.Channel)
		default:
			candidates = append(candidates, v)
		}
	}
	// Names a channel points to that no longer exist are protected too.
	var missing []string
	for name := range pointed {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	plan.Protected = append(plan.Protected, missing...)

	cutoff := now().Add(-o.OlderThan)
	for i, v := range candidates {
		if i < o.Keep || (o.OlderThan > 0 && !v.PublishedAt.Before(cutoff)) {
			plan.Keep = append(plan.Keep, v)
		} else {
			plan.Delete = append(plan.Delete, v)
		}
	}

	var total int64
	var keys []string
	for _, v := range plan.Delete {
		total += v.Bytes
		keys = append(keys, v.Keys...)
		est := ""
		if v.Estimated {
			est = " (date estimated: no release.json)"
		}
		verb := "would delete"
		if o.Yes {
			verb = "deleting"
		}
		fmt.Fprintf(o.Out, "%s %s/%s  %d bytes%s\n", verb, o.Project, v.Channel, v.Bytes, est)
	}
	if len(plan.Delete) == 0 {
		fmt.Fprintln(o.Out, "nothing to prune")
		return plan, nil
	}
	if !o.Yes {
		fmt.Fprintf(o.Out, "would free %d bytes in %d versions; re-run with --yes to delete\n", total, len(plan.Delete))
		return plan, nil
	}
	if err := wrapAccess(st.Delete(ctx, keys)); err != nil {
		return plan, err
	}
	fmt.Fprintf(o.Out, "freed %d bytes in %d versions\n", total, len(plan.Delete))
	return plan, nil
}
