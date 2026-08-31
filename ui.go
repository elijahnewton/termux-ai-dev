package main

import (
	"fmt"
	"os"
	"time"
)

type SimpleSpinner struct {
	stop chan struct{}
}

func NewSpinner(status string) *SimpleSpinner {
	s := &SimpleSpinner{stop: make(chan struct{})}
	go s.run(status)
	return s
}

func (s *SimpleSpinner) run(status string) {
	frames := []string{"|", "/", "-", "\\"}
	i := 0
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			fmt.Fprintf(os.Stderr, "[K[1;33m%s[0m %s", frames[i], status)
			i = (i + 1) % len(frames)
		case <-s.stop:
			fmt.Fprintf(os.Stderr, "[K")
			return
		}
	}
}

func (s *SimpleSpinner) Stop() {
	close(s.stop)
	time.Sleep(60 * time.Millisecond)
}
