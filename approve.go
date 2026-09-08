package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// DangerousCommand reports whether a shell command looks destructive enough
// that the agent should ask the human before running it on a phone.
func DangerousCommand(cmd string) (bool, string) {
	c := strings.TrimSpace(cmd)
	if c == "" {
		return false, ""
	}
	lower := strings.ToLower(c)

	patterns := []struct {
		re  *regexp.Regexp
		why string
	}{
		{regexp.MustCompile(`(?i)\brm\s+-rf\b`), "recursive force delete (rm -rf)"},
		{regexp.MustCompile(`(?i)\brm\s+-fr\b`), "recursive force delete (rm -fr)"},
		{regexp.MustCompile(`(?i)\bmkfs\b`), "filesystem format"},
		{regexp.MustCompile(`(?i)\bdd\s+.*\bof=/dev/`), "raw disk write"},
		{regexp.MustCompile(`(?i)\b(curl|wget)\b.*\|\s*(ba)?sh\b`), "pipe remote script to shell"},
		{regexp.MustCompile(`(?i)\bchmod\s+-R\s+777\b`), "world-writable recursive chmod"},
		{regexp.MustCompile(`(?i)\b(shutdown|reboot|halt)\b`), "system power control"},
		{regexp.MustCompile(`(?i)\bgit\s+push\s+.*--force\b`), "force push"},
		{regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`), "hard reset (discards work)"},
		{regexp.MustCompile(`(?i)\bgit\s+clean\s+-fd`), "git clean -fd"},
	}
	for _, p := range patterns {
		if p.re.MatchString(c) {
			return true, p.why
		}
	}
	if strings.Contains(lower, "rm -rf /") || strings.Contains(lower, "rm -fr /") {
		return true, "rm -rf /"
	}
	return false, ""
}

// Approver decides whether a dangerous command may run.
type Approver func(cmd, reason string) bool

// DefaultCLIApprover prompts on stderr/stdin. Returns true to allow.
func DefaultCLIApprover(cmd, reason string) bool {
	fmt.Fprintf(os.Stderr, "\033[1;33m⚠ Dangerous command (%s):\033[0m\n  %s\n", reason, cmd)
	ans, ok := readLine("Allow? [y/N]: ")
	if !ok {
		return false
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}
