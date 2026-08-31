//go:build linux || android

package main

import (
	"os/exec"
	"syscall"
)

func setPgid(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setpgid = true
}

func killGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
