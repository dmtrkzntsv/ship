package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// execArgv replaces the process with argv, looking argv[0] up in PATH, so
// signals and the terminal go straight to the child.
func execArgv(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

func requireLinux(cmdName string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("ship %s: Linux only", cmdName)
	}
	return nil
}
