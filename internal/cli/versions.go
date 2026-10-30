package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
)

func newVersionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "versions <project>",
		Short:             "List the published versions and channels of a project",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProjects,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := layout.ParseRef(args[0])
			if err != nil {
				return &usageError{err.Error()}
			}
			a, err := app.New(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			st, err := a.NewStore()
			if err != nil {
				return err
			}
			vs, err := release.Versions(cmd.Context(), st, ref.Project)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "CHANNEL\tVERSION\tPUBLISHED\tSIZE")
			for _, v := range vs {
				version, when := "-", v.PublishedAt.UTC().Format("2006-01-02 15:04")
				if v.Release != nil {
					version = v.Release.Version
				}
				if v.Estimated {
					when = "~" + when
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", v.Channel, version, when, humanSize(v.Bytes))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			refreshListing(cmd.Context(), st)
			return nil
		},
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
