// Package cli implements the ship command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

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
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newUninstallCmd())
	root.AddCommand(newLifecycleCmd("start", "Start an installed ref and wait for it to stay up"))
	root.AddCommand(newLifecycleCmd("stop", "Stop an installed ref"))
	root.AddCommand(newLifecycleCmd("restart", "Restart an installed ref and wait for it to stay up"))
	root.AddCommand(newLogsCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newSelfUpdateCmd())
	hidePowerShell(root)
	return root
}

// Execute runs the CLI and returns the process exit code. SIGINT, SIGTERM
// and SIGHUP cancel the command's context rather than killing ship, so a
// host change in progress (an update's health check, say) reverts cleanly.
func Execute() int {
	ctx, stop := signalContext()
	defer stop()
	root := NewRoot(os.Stdout, os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// signalContext is cancelled by SIGINT, SIGTERM or SIGHUP. Until stop is
// called further signals are absorbed too, so a revert in progress is not
// cut short; stop restores the default handling.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
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
