package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dmtrkzntsv/ship/internal/app"
)

// Seams for tests.
var (
	runGuard   = requireLinuxRoot
	newApp     = app.New
	execFn     = execArgv
	isTerminal = func(fd uintptr) bool { return term.IsTerminal(int(fd)) }
)

// anyTerminal reports whether stdin, stdout or stderr is a terminal. The
// run then gets a pty (systemd-run --pty): --pipe would hand the caller's
// file descriptors, a root terminal among them, to the service user.
func anyTerminal() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if isTerminal(f.Fd()) {
			return true
		}
	}
	return false
}

// runArgs requires the ref; when a "--" is given the ref must precede it.
func runArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 || cmd.ArgsLenAtDash() == 0 {
		return errors.New("requires a <ref> argument")
	}
	if d := cmd.ArgsLenAtDash(); d > 1 {
		return errors.New("only the <ref> may come before --")
	}
	return nil
}

// binaryArgs is everything after "--", or after the ref when there is none.
func binaryArgs(cmd *cobra.Command, args []string) []string {
	if d := cmd.ArgsLenAtDash(); d >= 0 {
		return args[d:]
	}
	return args[1:]
}

func newRunCmd() *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:               "run <ref> [-- args...]",
		Short:             "Run the ref's binary once with its env file, user and data dir",
		ValidArgsFunction: completeInstalledRefs,
		Args:              runArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runGuard("run"); err != nil {
				return err
			}
			a, err := newApp(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			argv, err := a.RunArgs(args[0], service, binaryArgs(cmd, args), anyTerminal())
			if err != nil {
				return err
			}
			return execFn(argv)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "service the ref is installed in (default: the ref's project)")
	registerServiceCompletion(cmd, false)
	return cmd
}
