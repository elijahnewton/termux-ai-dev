package main

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "strings"
)

// allowedRoots returns the directories a patch may touch.
func allowedRoots() []string {
    var roots []string
    if cwd, err := os.Getwd(); err == nil {
        roots = append(roots, cwd)
    }
    if home, err := os.UserHomeDir(); err == nil && home != "" {
        roots = append(roots, home)
    }
    return roots
}

// isPathAllowed verifies that target — after symlink resolution — lies
// strictly inside one of the allowed roots. The separator-aware prefix
// prevents /home/user-evil from passing a /home/user check, and
// EvalSymlinks prevents symlink escape.
func isPathAllowed(target string) error {
    resolved, err := filepath.EvalSymlinks(target)
    if err != nil {
        return err
    }
    for _, root := range allowedRoots() {
        rr, err := filepath.EvalSymlinks(root)
        if err != nil {
            rr = root
        }
        if resolved == rr || strings.HasPrefix(resolved, rr+string(filepath.Separator)) {
            return nil
        }
    }
    return fmt.Errorf("path %s escapes allowed directories (must be under cwd or HOME)", target)
}

func ApplySearchReplace(path, search, replace string) error {
    if path == "" {
        return errors.New("empty path")
    }
    var target string
    if filepath.IsAbs(path) {
        target = filepath.Clean(path)
    } else {
        cwd, err := os.Getwd()
        if err != nil {
            return err
        }
        target = filepath.Join(cwd, filepath.Clean(path))
    }

    if err := isPathAllowed(target); err != nil {
        return err
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

    normalize := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
    content := normalize(string(data))
    search = normalize(search)
    replace = normalize(replace)

    if search == "" {
        return errors.New("empty search block")
    }
    if !strings.Contains(content, search) {
        return errors.New("search block not found (exact match failed)")
    }

    newContent := strings.Replace(content, search, replace, 1)

    // Atomic write: temp file in the same directory, then rename, so a
    // crash can never leave a half-written file behind.
    dir := filepath.Dir(target)
    tmp, err := os.CreateTemp(dir, ".termux-agent-patch-*")
    if err != nil {
        return err
    }
    tmpName := tmp.Name()
    if _, err := tmp.Write([]byte(newContent)); err != nil {
        tmp.Close()
        os.Remove(tmpName)
        return err
    }
    if err := tmp.Close(); err != nil {
        os.Remove(tmpName)
        return err
    }
    if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
        os.Remove(tmpName)
        return err
    }
    return os.Rename(tmpName, target)
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
        patches = append(patches, Patch{
            File:    file,
            Search:  text[m[4]:m[5]],
            Replace: text[m[6]:m[7]],
        })
    }
    return patches
}