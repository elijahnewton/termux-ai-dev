package main

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"
)

// maxReadBytes caps how much of a file read_file returns in one call, so
// pointing it at a huge log or binary can't blow the history budget or the
// model's context window in a single tool result.
const maxReadBytes = 64 << 10 // 64 KiB

// maxListEntries caps how many directory entries list_directory reports.
const maxListEntries = 500

// errListLimit stops filepath.Walk early once maxListEntries is hit; it is
// never surfaced to the caller.
var errListLimit = errors.New("list_directory: entry limit reached")

// WriteFile creates a file with the given content, creating any missing
// parent directories along the way. This is how the agent scaffolds real
// project folders: writing "myapp/src/index.html" creates "myapp/src/" as
// a side effect. It overwrites an existing file outright — use
// ApplySearchReplace for a targeted edit to a file that should otherwise
// stay as-is.
func WriteFile(path, content string) (string, error) {
    target, err := resolveTarget(path)
    if err != nil {
        return "", err
    }
    if info, err := os.Stat(target); err == nil && info.IsDir() {
        return "", fmt.Errorf("%s is a directory", path)
    }

    dir := filepath.Dir(target)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return "", fmt.Errorf("creating directory %s: %w", dir, err)
    }

    // Atomic write, matching ApplySearchReplace: temp file in the same
    // directory, then rename, so a crash never leaves a half-written file.
    tmp, err := os.CreateTemp(dir, ".termux-agent-write-*")
    if err != nil {
        return "", err
    }
    tmpName := tmp.Name()
    if _, err := tmp.WriteString(content); err != nil {
        tmp.Close()
        os.Remove(tmpName)
        return "", err
    }
    if err := tmp.Close(); err != nil {
        os.Remove(tmpName)
        return "", err
    }
    if err := os.Chmod(tmpName, 0o644); err != nil {
        os.Remove(tmpName)
        return "", err
    }
    if err := os.Rename(tmpName, target); err != nil {
        os.Remove(tmpName)
        return "", err
    }
    return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// ReadFile returns a file's real current content, so the agent can inspect
// what's actually on disk before proposing an apply_search_replace edit
// instead of guessing at exact search text.
func ReadFile(path string) (string, error) {
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
    if len(data) > maxReadBytes {
        return fmt.Sprintf("%s\n... (truncated; file is %d bytes total)", string(data[:maxReadBytes]), len(data)), nil
    }
    return string(data), nil
}

// ListDirectory recursively lists files and folders under path (skipping
// .git, node_modules, and vendor) so the agent — and the person watching —
// can confirm the real on-disk project layout instead of assuming it
// matches whatever was planned.
func ListDirectory(path string) (string, error) {
    if strings.TrimSpace(path) == "" {
        path = "."
    }
    target, err := resolveTarget(path)
    if err != nil {
        return "", err
    }
    info, err := os.Stat(target)
    if err != nil {
        return "", err
    }
    if !info.IsDir() {
        return "", fmt.Errorf("%s is not a directory", path)
    }

    var lines []string
    walkErr := filepath.Walk(target, func(p string, fi os.FileInfo, err error) error {
        if err != nil {
            return nil // skip unreadable entries rather than aborting the whole listing
        }
        if p == target {
            return nil
        }
        name := fi.Name()
        if fi.IsDir() && (name == ".git" || name == "node_modules" || name == "vendor") {
            return filepath.SkipDir
        }
        if len(lines) >= maxListEntries {
            return errListLimit
        }
        rel, _ := filepath.Rel(target, p)
        if fi.IsDir() {
            rel += "/"
        }
        lines = append(lines, rel)
        return nil
    })
    if walkErr != nil && walkErr != errListLimit {
        return "", walkErr
    }

    if len(lines) == 0 {
        return "(empty directory)", nil
    }
    sort.Strings(lines)
    out := strings.Join(lines, "\n")
    if walkErr == errListLimit {
        out += fmt.Sprintf("\n... (truncated at %d entries)", maxListEntries)
    }
    return out, nil
}
