package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
)

func newPruneCmd() *cobra.Command {
	var (
		keep      int
		olderThan string
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "prune <project>",
		Short: "Delete old versions of a project (dry run unless --yes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			if !layout.ValidName(project) {
				return &usageError{"invalid project name " + project}
			}
			if keep < 0 {
				return &usageError{"--keep must be >= 0"}
			}
			var age time.Duration
			if olderThan != "" {
				d, err := parseDuration(olderThan)
				if err != nil {
					return &usageError{err.Error()}
				}
				age = d
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			st, err := a.NewStore()
			if err != nil {
				return err
			}
			_, err = release.Prune(cmd.Context(), st, release.PruneOptions{
				Project: project, Keep: keep, OlderThan: age, Yes: yes, Now: time.Now, Out: a.Out,
			})
			return err
		},
	}
	f := cmd.Flags()
	f.IntVar(&keep, "keep", 10, "number of newest versions to keep")
	f.StringVar(&olderThan, "older-than", "", "only delete versions older than this (e.g. 90d, 72h)")
	f.BoolVar(&yes, "yes", false, "actually delete (default is a dry run)")
	return cmd
}

// parseDuration accepts Go durations plus a "d" (days) suffix such as 90d.
func parseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid --older-than %q", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid --older-than %q", s)
	}
	return d, nil
}
