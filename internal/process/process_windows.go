//go:build windows

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	return exec.CommandContext(ctx, shell, "/S", "/C", command)
}

func configureProcess(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	c.Cancel = func() error { return killProcess(c) }
}
func terminateProcess(c *exec.Cmd) error { return terminateProcessTree(c, false) }
func killProcess(c *exec.Cmd) error      { return terminateProcessTree(c, true) }

func terminateProcessTree(c *exec.Cmd, force bool) error {
	if c.Process == nil {
		return os.ErrProcessDone
	}
	args := []string{"/PID", strconv.Itoa(c.Process.Pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	if err := exec.Command("taskkill", args...).Run(); err == nil {
		return nil
	} else if killErr := c.Process.Kill(); errors.Is(killErr, os.ErrProcessDone) {
		return os.ErrProcessDone
	} else if force {
		return killErr
	} else {
		return err
	}
}
