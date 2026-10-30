// Package cli implements the ship command-line interface.
package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

// NewRoot builds the root command writing to out and errOut.
func NewRoot(out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "ship",
		Short:         "Publish project binaries to R2 and install them as systemd services",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.AddCommand(newVersionCmd())
	root.AddCommand(newPublishCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	root := NewRoot(os.Stdout, os.Stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// requireLinuxRoot guards host-changing commands.
func requireLinuxRoot(cmdName string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("ship %s: Linux only", cmdName)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("ship %s: must run as root (try: sudo ship …)", cmdName)
	}
	return nil
}
