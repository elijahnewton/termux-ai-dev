//go:build linux || android

package main

import (
    "errors"
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
    err := syscall.Kill(-pid, syscall.SIGKILL)
    if errors.Is(err, syscall.ESRCH) {
        return nil // already gone
    }
    return err
}