package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func resolveShell() string {
	if p := os.Getenv("TERMUX_PREFIX"); p != "" {
		sh := p + "/bin/sh"
		if st, err := os.Stat(sh); err == nil && !st.IsDir() {
			return sh
		}
	}
	candidates := []string{
		"/data/data/com.termux/files/usr/bin/sh",
		"/system/bin/sh",
	}
	for _, sh := range candidates {
		if st, err := os.Stat(sh); err == nil && !st.IsDir() {
			return sh
		}
	}
	return "/system/bin/sh"
}

func ExecuteCommand(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sh := resolveShell()
	c := exec.CommandContext(ctx, sh, "-c", cmd)
	c.Env = os.Environ()

	setPgid(c)

	out, err := c.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		if c.Process != nil {
			_ = killGroup(c.Process.Pid)
		}
		return string(out), fmt.Errorf("command timed out after %v", timeout)
	}
	return string(out), err
}
