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