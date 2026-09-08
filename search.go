package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxGrepMatches   = 80
	maxGrepFileBytes = 512 << 10 // skip files larger than 512 KiB
	maxGlobMatches   = 200
)

var noiseDirNames = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".svn": true,
	".hg": true, "__pycache__": true, ".venv": true, "venv": true,
	"dist": true, "build": true, ".next": true, ".turbo": true,
	"target": true, ".cache": true, "coverage": true, ".termux-agent": true,
}

// GrepFiles searches text files under root for pattern (literal or regex).
// Returns compact path:line:text hits, capped for mobile context windows.
func GrepFiles(root, pattern string, useRegex bool, maxHits int) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("empty search pattern")
	}
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if maxHits <= 0 || maxHits > maxGrepMatches {
		maxHits = maxGrepMatches
	}

	target, err := resolveTarget(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return grepOneFile(target, root, pattern, useRegex, maxHits)
	}

	var re *regexp.Regexp
	if useRegex {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex: %w", err)
		}
	}

	var (
		hits   []string
		scanned int
		skipped int
	)
	walkErr := filepath.Walk(target, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil
		}
		if fi.IsDir() {
			if noiseDirNames[fi.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.Size() > maxGrepFileBytes || fi.Size() == 0 {
			skipped++
			return nil
		}
		if !isProbablyText(p) {
			skipped++
			return nil
		}
		scanned++
		fileHits, err := grepFile(p, target, pattern, re, useRegex, maxHits-len(hits))
		if err != nil {
			return nil
		}
		hits = append(hits, fileHits...)
		if len(hits) >= maxHits {
			return errListLimit
		}
		return nil
	})
	if walkErr != nil && walkErr != errListLimit {
		return "", walkErr
	}

	if len(hits) == 0 {
		return fmt.Sprintf("no matches for %q (scanned %d files, skipped %d)", pattern, scanned, skipped), nil
	}
	out := strings.Join(hits, "\n")
	if walkErr == errListLimit || len(hits) >= maxHits {
		out += fmt.Sprintf("\n... (truncated at %d matches; scanned %d files)", maxHits, scanned)
	}
	return out, nil
}

func grepOneFile(absPath, displayRoot, pattern string, useRegex bool, maxHits int) (string, error) {
	var re *regexp.Regexp
	var err error
	if useRegex {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex: %w", err)
		}
	}
	base := filepath.Dir(absPath)
	hits, err := grepFile(absPath, base, pattern, re, useRegex, maxHits)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no matches for %q in %s", pattern, displayRoot), nil
	}
	return strings.Join(hits, "\n"), nil
}

func grepFile(absPath, root, pattern string, re *regexp.Regexp, useRegex bool, limit int) ([]string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		rel = absPath
	}
	rel = filepath.ToSlash(rel)

	var hits []string
	sc := bufio.NewScanner(f)
	// Allow long lines without failing the whole file.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		matched := false
		if useRegex {
			matched = re.MatchString(line)
		} else {
			matched = strings.Contains(line, pattern)
		}
		if !matched {
			continue
		}
		trimmed := line
		if len(trimmed) > 200 {
			trimmed = trimmed[:200] + "…"
		}
		hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, lineNo, trimmed))
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

// GlobFiles finds files matching a path pattern (filepath.Match on relative paths,
// plus ** support for recursive directory globs).
func GlobFiles(pattern string, maxHits int) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("empty glob pattern")
	}
	if maxHits <= 0 || maxHits > maxGlobMatches {
		maxHits = maxGlobMatches
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if err := isPathAllowed(cwd); err != nil {
		return "", err
	}

	var matches []string
	walkErr := filepath.Walk(cwd, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil {
			return nil
		}
		if fi.IsDir() {
			if noiseDirNames[fi.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(cwd, p)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if matchGlob(pattern, relSlash) {
			matches = append(matches, relSlash)
			if len(matches) >= maxHits {
				return errListLimit
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != errListLimit {
		return "", walkErr
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no files matching %q", pattern), nil
	}
	out := strings.Join(matches, "\n")
	if walkErr == errListLimit {
		out += fmt.Sprintf("\n... (truncated at %d matches)", maxHits)
	}
	return out, nil
}

// matchGlob supports * and ? via filepath.Match, and ** for "any path segments".
func matchGlob(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, path)
		if ok {
			return true
		}
		// Also allow matching basename-only patterns like "*.go"
		ok, _ = filepath.Match(pattern, filepath.Base(path))
		return ok
	}
	// Convert ** globs into a simple segment-aware matcher.
	parts := strings.Split(pattern, "**")
	if len(parts) == 1 {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	// Require prefix match on first part and suffix on last; middle is flexible.
	rest := path
	for i, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		if i == 0 {
			// prefix
			segs := strings.Split(part, "/")
			pathSegs := strings.Split(rest, "/")
			if len(pathSegs) < len(segs) {
				return false
			}
			for j, s := range segs {
				ok, _ := filepath.Match(s, pathSegs[j])
				if !ok {
					return false
				}
			}
			rest = strings.Join(pathSegs[len(segs):], "/")
			continue
		}
		if i == len(parts)-1 {
			// suffix: try matching at any alignment ending at end
			return matchGlobSuffix(part, rest)
		}
		// middle: find earliest occurrence then continue
		idx := indexGlob(part, rest)
		if idx < 0 {
			return false
		}
		consumed := idx + len(strings.Split(part, "/"))
		segs := strings.Split(rest, "/")
		if consumed >= len(segs) {
			rest = ""
		} else {
			rest = strings.Join(segs[consumed:], "/")
		}
	}
	return true
}

func matchGlobSuffix(part, rest string) bool {
	segs := strings.Split(part, "/")
	pathSegs := strings.Split(rest, "/")
	if len(pathSegs) < len(segs) {
		return false
	}
	offset := len(pathSegs) - len(segs)
	for i, s := range segs {
		ok, _ := filepath.Match(s, pathSegs[offset+i])
		if !ok {
			return false
		}
	}
	return true
}

func indexGlob(part, rest string) int {
	segs := strings.Split(part, "/")
	pathSegs := strings.Split(rest, "/")
	for i := 0; i+len(segs) <= len(pathSegs); i++ {
		ok := true
		for j, s := range segs {
			m, _ := filepath.Match(s, pathSegs[i+j])
			if !m {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func isProbablyText(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip",
		".gz", ".tar", ".bz2", ".xz", ".7z", ".rar", ".exe", ".dll", ".so",
		".dylib", ".bin", ".o", ".a", ".class", ".jar", ".apk", ".aab",
		".woff", ".woff2", ".ttf", ".otf", ".mp3", ".mp4", ".webm", ".sqlite",
		".db", ".wasm":
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return true
	}
	buf = buf[:n]
	if strings.Contains(string(buf), "\x00") {
		return false
	}
	return utf8.Valid(buf) || mostlyPrintable(buf)
}

func mostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	printable := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 32 && c < 127) {
			printable++
		}
	}
	return printable*100/len(b) >= 85
}
