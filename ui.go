package main

import (
    "fmt"
    "os"
    "sync"
    "time"
)

type SimpleSpinner struct {
    stop chan struct{}
    done chan struct{}
    once sync.Once
}

func NewSpinner(status string) *SimpleSpinner {
    s := &SimpleSpinner{
        stop: make(chan struct{}),
        done: make(chan struct{}),
    }
    go s.run(status)
    return s
}

func (s *SimpleSpinner) run(status string) {
    defer close(s.done)
    frames := []string{"|", "/", "-", "\\"}
    i := 0
    ticker := time.NewTicker(250 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            fmt.Fprintf(os.Stderr, "\r\033[K\033[1;33m%s\033[0m %s", frames[i], status)
            i = (i + 1) % len(frames)
        case <-s.stop:
            fmt.Fprint(os.Stderr, "\r\033[K")
            return
        }
    }
}

// Stop halts the spinner and waits for the goroutine to exit, so no stray
// frame garbles output printed afterwards. Safe to call more than once.
func (s *SimpleSpinner) Stop() {
    s.once.Do(func() { close(s.stop) })
    <-s.done
}

// printToolActivity prints a single line to the terminal for each tool
// call the agent makes: a check or cross, the tool name, what it acted on
// (a path or a command), and a short outcome note. Called right after the
// spinner for that call stops, so a project being built looks like real
// work happening on screen rather than a silent wait for a chat reply.
func printToolActivity(name, detail string, ok bool, note string) {
    mark := "\033[1;32m✓\033[0m"
    if !ok {
        mark = "\033[1;31m✗\033[0m"
    }
    line := fmt.Sprintf("  %s \033[2m%s\033[0m %s", mark, name, detail)
    if note != "" {
        line += "  " + note
    }
    fmt.Fprintln(os.Stderr, line)
}