// Package cli implements the ship command-line interface.
package cli

import (
	"fmt"
	"io"
	"os"

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
	root.AddCommand(newVersionsCmd())
	root.AddCommand(newPruneCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newAdoptCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newUninstallCmd())
	root.AddCommand(newLifecycleCmd("start", "Start an installed ref and wait for it to stay up"))
	root.AddCommand(newLifecycleCmd("stop", "Stop an installed ref"))
	root.AddCommand(newLifecycleCmd("restart", "Restart an installed ref and wait for it to stay up"))
	root.AddCommand(newLogsCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newStatusCmd())
	hidePowerShell(root)
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
	if err := requireLinux(cmdName); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("ship %s: must run as root (try: sudo ship …)", cmdName)
	}
	return nil
}
