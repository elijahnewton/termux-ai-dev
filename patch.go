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

// resolveSymlinkAware resolves target's symlinks. If target doesn't exist
// yet — as when write_file is about to create a brand-new file or a new
// subdirectory — plain EvalSymlinks would just error, so this walks up to
// the nearest existing ancestor, resolves that instead, and rejoins the
// missing suffix. A symlinked parent still can't be used to escape the
// allow-list check below.
func resolveSymlinkAware(target string) (string, error) {
    if resolved, err := filepath.EvalSymlinks(target); err == nil {
        return resolved, nil
    }
    dir := filepath.Dir(target)
    if dir == target {
        return "", fmt.Errorf("cannot resolve path %s", target)
    }
    resolvedDir, err := resolveSymlinkAware(dir)
    if err != nil {
        return "", err
    }
    return filepath.Join(resolvedDir, filepath.Base(target)), nil
}

// isPathAllowed verifies that target — after symlink resolution — lies
// strictly inside one of the allowed roots. The separator-aware prefix
// prevents /home/user-evil from passing a /home/user check, and
// resolveSymlinkAware prevents symlink escape (for both existing and
// not-yet-created paths).
func isPathAllowed(target string) error {
    resolved, err := resolveSymlinkAware(target)
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

// resolveTarget cleans path (joining it against cwd first if relative) and
// verifies it's inside an allowed root. Shared by ApplySearchReplace here
// and by write_file/read_file/list_directory in files.go so every
// filesystem-touching tool enforces the exact same boundary.
func resolveTarget(path string) (string, error) {
    if path == "" {
        return "", errors.New("empty path")
    }
    var target string
    if filepath.IsAbs(path) {
        target = filepath.Clean(path)
    } else {
        cwd, err := os.Getwd()
        if err != nil {
            return "", err
        }
        target = filepath.Join(cwd, filepath.Clean(path))
    }
    if err := isPathAllowed(target); err != nil {
        return "", err
    }
    return target, nil
}

func ApplySearchReplace(path, search, replace string) error {
    target, err := resolveTarget(path)
    if err != nil {
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

// FileBlock is a "### path" header followed by a fenced code block,
// the plain-text fallback for a whole-file write (new file, or full
// rewrite) when the model emits text instead of calling write_file.
type FileBlock struct {
    File    string
    Content string
}

// fileBlockRe matches "### path\n```lang\ncontent\n```". Built by
// concatenation (rather than one raw string) only because a raw string
// can't itself contain the backtick fence.
var fileBlockRe = regexp.MustCompile(`(?m)^### ([^\n]+)\n` + "```" + `[a-zA-Z0-9_+-]*\n([\s\S]*?)\n` + "```")

func ExtractFileBlocks(text string) []FileBlock {
    var blocks []FileBlock
    for _, m := range fileBlockRe.FindAllStringSubmatch(text, -1) {
        file := strings.TrimSpace(m[1])
        if file == "" || len(strings.Fields(file)) != 1 {
            continue // skip stray "### ..." headers the model used as prose, not a real path
        }
        blocks = append(blocks, FileBlock{File: file, Content: m[2]})
    }
    return blocks
}