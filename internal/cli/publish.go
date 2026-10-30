package cli

import (
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
)

func newPublishCmd() *cobra.Command {
	var (
		path, version, bucket string
		channels              []string
		keep                  int
	)
	cmd := &cobra.Command{
		Use:   "publish <project>",
		Short: "Upload a folder of built files to R2 as a release",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := args[0]
			if !layout.ValidName(project) {
				return &usageError{"invalid project name " + project}
			}
			if keep < 0 {
				return &usageError{"--keep must be >= 0"}
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if bucket != "" {
				a.Cfg.R2.Bucket = bucket
			}
			st, err := a.NewStore()
			if err != nil {
				return err
			}
			return release.Publish(cmd.Context(), st, release.PublishOptions{
				Project: project, Dir: path, Version: version, Channels: channels, Keep: keep,
				Commit: gitCommit(), Now: time.Now, Out: a.Out,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&path, "path", "", "folder with the built files")
	f.StringVar(&version, "version", "", "release version (immutable channel)")
	f.StringSliceVar(&channels, "channels", nil, "moving channels to update (latest,dev)")
	f.IntVar(&keep, "keep", 10, "versions to keep after publishing (0 disables pruning)")
	f.StringVar(&bucket, "bucket", "", "R2 bucket (overrides config)")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// gitCommit returns HEAD of the current directory, or "" when unavailable.
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
