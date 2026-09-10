//go:build unix

package process

import (
	"os/exec"
	"syscall"
)

func configureProcess(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func terminateProcess(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
}
func killProcess(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
