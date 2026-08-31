package main

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "time"
)

// resolveShell prefers Termux bash (users' interactive shell) over Android's
// mksh, which rejects bashisms like [[ ]].
func resolveShell() string {
    var candidates []string
    if p := os.Getenv("TERMUX_PREFIX"); p != "" {
        candidates = append(candidates, p+"/bin/bash", p+"/bin/sh")
    }
    if sh := os.Getenv("SHELL"); sh != "" {
        candidates = append(candidates, sh)
    }
    candidates = append(candidates,
        "/data/data/com.termux/files/usr/bin/bash",
        "/data/data/com.termux/files/usr/bin/sh",
        "/system/bin/sh",
    )
    for _, sh := range candidates {
        if st, err := os.Stat(sh); err == nil && !st.IsDir() {
            return sh
        }
    }
    return "/system/bin/sh"
}

func ExecuteCommand(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
    if timeout <= 0 {
        timeout = 15 * time.Second
    }
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    c := exec.CommandContext(ctx, resolveShell(), "-c", cmd)
    c.Env = os.Environ()

    setPgid(c)
    // If the command exits but a daemonised child keeps the output pipe
    // open, CombinedOutput would block until the turn context expires.
    // WaitDelay forces completion shortly after the process exits.
    c.WaitDelay = 2 * time.Second

    out, err := c.CombinedOutput()
    if ctx.Err() == context.DeadlineExceeded {
        if c.Process != nil {
            _ = killGroup(c.Process.Pid)
        }
        return string(out), fmt.Errorf("command timed out after %v", timeout)
    }
    return string(out), err
}