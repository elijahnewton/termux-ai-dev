package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func ApplySearchReplace(path, search, replace string) error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	home := os.Getenv("HOME")

	var target string
	if filepath.IsAbs(path) {
		target = filepath.Clean(path)
	} else {
		target = filepath.Join(cwd, filepath.Clean(path))
	}

	allowed := false
	if strings.HasPrefix(target, cwd) {
		allowed = true
	}
	if home != "" && strings.HasPrefix(target, home) {
		allowed = true
	}
	if !allowed {
		return fmt.Errorf("path %s escapes allowed directories (must be under cwd or HOME)", path)
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("cannot patch a directory")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}

	original := string(data)
	normalized := strings.ReplaceAll(original, "
", "
")
	searchNorm := strings.ReplaceAll(search, "
", "
")
	replaceNorm := strings.ReplaceAll(replace, "
", "
")

	if !strings.Contains(normalized, searchNorm) {
		return errors.New("search block not found (exact match failed)")
	}

	newContent := strings.Replace(normalized, searchNorm, replaceNorm, 1)
	if newContent == normalized {
		return errors.New("search block not found")
	}

	if err := os.WriteFile(target, []byte(newContent), info.Mode()); err != nil {
		return err
	}
	return nil
}

type Patch struct {
	File    string
	Search  string
	Replace string
}

func ExtractPatches(text string) []Patch {
	var patches []Patch
	re := regexp.MustCompile(`(?m)(?:^### ([^\n]+)\n)?<<<<<<< SEARCH\n([\s\S]*?)\n=======\n([\s\S]*?)\n>>>>>>> REPLACE`)
	matches := re.FindAllStringSubmatchIndex(text, -1)
	for _, m := range matches {
		file := ""
		if m[2] != -1 && m[3] != -1 {
			file = strings.TrimSpace(text[m[2]:m[3]])
		}
		search := text[m[4]:m[5]]
		replace := text[m[6]:m[7]]
		patches = append(patches, Patch{
			File:    file,
			Search:  search,
			Replace: replace,
		})
	}
	return patches
}
