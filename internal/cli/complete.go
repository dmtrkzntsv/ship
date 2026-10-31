package cli

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/cache"
	"github.com/dmtrkzntsv/ship/internal/config"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/state"
	"github.com/dmtrkzntsv/ship/internal/store"
)

// completionConfig is where completion reads its data from; tests replace
// completionEnv. Completion never needs root and never fails: unreadable
// state or cache simply yields no suggestions.
type completionConfig struct {
	statePath, cachePath string
	newStore             func() (store.Store, error)
	now                  func() time.Time
}

var completionEnv = completionConfig{
	statePath: state.DefaultPath,
	cachePath: cache.DefaultPath,
	newStore:  completionStore,
	now:       time.Now,
}

// completionStore opens the R2 store, failing quietly-checkable when the
// config or credentials are not usable (cache.Get then uses the disk copy).
func completionStore() (store.Store, error) {
	home, _ := os.UserHomeDir()
	cfg, _, _, err := config.Load(os.Getenv, config.DefaultPaths(home, os.Geteuid() == 0))
	if err != nil {
		return nil, err
	}
	if err := cfg.R2.Validate(); err != nil {
		return nil, err
	}
	return store.NewS3(cfg.R2.S3Options())
}

const noFile = cobra.ShellCompDirectiveNoFileComp

func listing(cmd *cobra.Command) cache.Listing {
	return cache.Get(cmd.Context(), completionEnv.cachePath, completionEnv.now(), completionEnv.newStore)
}

func loadState() *state.State {
	s, err := state.File{Path: completionEnv.statePath}.Load()
	if err != nil {
		return &state.State{Services: map[string]*state.Service{}}
	}
	return s
}

// finish sorts, de-duplicates and prefix-filters candidates.
func finish(cands []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	sort.Strings(cands)
	out := make([]string, 0, len(cands))
	for i, c := range cands {
		if (i > 0 && c == cands[i-1]) || !strings.HasPrefix(c, toComplete) {
			continue
		}
		out = append(out, c)
	}
	return out, noFile
}

// publishedRefs are the refs that can be installed: the project itself when
// it publishes <project>.service or has no components, plus project/component.
func publishedRefs(l cache.Listing) []string {
	var refs []string
	for p, e := range l.Projects {
		if e.Root || len(e.Components) == 0 {
			refs = append(refs, p)
		}
		for _, c := range e.Components {
			refs = append(refs, p+"/"+c)
		}
	}
	return refs
}

func projectNames(l cache.Listing) []string {
	names := make([]string, 0, len(l.Projects))
	for p := range l.Projects {
		names = append(names, p)
	}
	return names
}

// completePublishedRefs completes the first argument with installable refs.
func completePublishedRefs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFile
	}
	return finish(publishedRefs(listing(cmd)), toComplete)
}

// completeProjects completes the first argument with project names.
func completeProjects(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFile
	}
	return finish(projectNames(listing(cmd)), toComplete)
}

// completeInstalledRefs completes the first argument with refs installed in
// the --service given so far, or in any service when there is none.
func completeInstalledRefs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFile
	}
	s := loadState()
	service, _ := cmd.Flags().GetString("service")
	var refs []string
	for name, svc := range s.Services {
		if service != "" && name != service {
			continue
		}
		for r := range svc.Refs {
			refs = append(refs, r)
		}
	}
	return finish(refs, toComplete)
}

// serviceFlagCompletion suggests the services in state; when withProject is
// set (install, adopt) it adds the project of the ref given so far.
func serviceFlagCompletion(withProject bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var names []string
		for name := range loadState().Services {
			names = append(names, name)
		}
		if withProject && len(args) > 0 {
			if ref, err := layout.ParseRef(args[0]); err == nil {
				names = append(names, ref.DefaultService())
			}
		}
		return finish(names, toComplete)
	}
}

// completeVersionFlag suggests the channels of the ref's project from the cache.
func completeVersionFlag(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, noFile
	}
	ref, err := layout.ParseRef(args[0])
	if err != nil {
		return nil, noFile
	}
	return finish(append([]string(nil), listing(cmd).Projects[ref.Project].Channels...), toComplete)
}

// registerServiceCompletion attaches a service-flag completion to cmd.
func registerServiceCompletion(cmd *cobra.Command, withProject bool) {
	_ = cmd.RegisterFlagCompletionFunc("service", serviceFlagCompletion(withProject))
}

func registerVersionCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("version", completeVersionFlag)
}

// hidePowerShell keeps the default `completion` command but drops the
// PowerShell generator, which ship does not support.
func hidePowerShell(root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	if c, _, err := root.Find([]string{"completion"}); err == nil && c != root {
		for _, sub := range c.Commands() {
			if sub.Name() == "powershell" {
				sub.Hidden = true
			}
		}
	}
}

// refreshListing refreshes the R2 listing cache after a successful root run.
// It never fails the command: every error is ignored.
func refreshListing(ctx context.Context, st store.Store) {
	if os.Geteuid() != 0 {
		return
	}
	refreshListingTo(ctx, st, cache.DefaultPath)
}

func refreshListingTo(ctx context.Context, st store.Store, path string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	l, err := cache.Refresh(ctx, st)
	if err != nil {
		return
	}
	_ = cache.Save(path, l)
}

// refreshAfter refreshes the listing cache after a successful install or
// update, ignoring every error.
func refreshAfter(cmd *cobra.Command, a *app.App) {
	if os.Geteuid() != 0 {
		return
	}
	if st, err := a.NewStore(); err == nil {
		refreshListing(cmd.Context(), st)
	}
}
