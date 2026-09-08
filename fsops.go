package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteFile removes a file (not a directory) inside allowed roots.
func DeleteFile(path string) (string, error) {
	target, err := resolveTarget(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; refuse to delete directories via delete_file (use execute_command carefully if needed)", path)
	}
	if err := os.Remove(target); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted %s", path), nil
}

// MoveFile renames/moves a file or directory within allowed roots.
func MoveFile(src, dst string) (string, error) {
	from, err := resolveTarget(src)
	if err != nil {
		return "", err
	}
	to, err := resolveTarget(dst)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(from); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(from, to); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved %s → %s", src, dst), nil
}

// ReadFileRange reads a file with optional 1-based start line and max lines.
// offset<=0 means start at line 1; limit<=0 means default cap by bytes.
func ReadFileRange(path string, offset, limit int) (string, error) {
	target, err := resolveTarget(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; use list_directory", path)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	content := string(data)
	if offset <= 0 && limit <= 0 {
		if len(data) > maxReadBytes {
			return fmt.Sprintf("%s\n... (truncated; file is %d bytes total — use offset/limit)", string(data[:maxReadBytes]), len(data)), nil
		}
		return content, nil
	}

	lines := splitKeepEnds(content)
	total := len(lines)
	start := 0
	if offset > 0 {
		start = offset - 1
	}
	if start >= total {
		return fmt.Sprintf("(file has %d lines; offset %d is past end)", total, offset), nil
	}
	end := total
	if limit > 0 {
		end = start + limit
		if end > total {
			end = total
		}
	} else {
		n := 0
		end = start
		for end < total && n < maxReadBytes {
			n += len(lines[end])
			end++
		}
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
	}
	out := b.String()
	header := fmt.Sprintf("lines %d-%d of %d\n", start+1, end, total)
	if end < total {
		return header + out + "\n... (more lines below; raise limit or offset)", nil
	}
	return header + out, nil
}

func splitKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
