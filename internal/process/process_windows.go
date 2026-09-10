//go:build windows

package process

import "os/exec"

func configureProcess(c *exec.Cmd) {}
func terminateProcess(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return c.Process.Kill()
}
func killProcess(c *exec.Cmd) error { return terminateProcess(c) }
