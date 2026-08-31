//go:build !linux && !android

package main

import "os/exec"

func setPgid(c *exec.Cmd) {}
func killGroup(pid int) error { return nil }
