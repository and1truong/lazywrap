//go:build unix

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func configureProcess(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CommandContext's default cancellation only kills the shell. Kill the
	// process group so descendants cannot outlive a canceled command.
	c.Cancel = func() error { return killProcess(c) }
}
func terminateProcess(c *exec.Cmd) error { return signalProcess(c, syscall.SIGTERM) }
func killProcess(c *exec.Cmd) error      { return signalProcess(c, syscall.SIGKILL) }

func signalProcess(c *exec.Cmd, signal syscall.Signal) error {
	if c.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-c.Process.Pid, signal); errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	} else {
		return err
	}
}
