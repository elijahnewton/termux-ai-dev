package main

import (
    "os"
    "strings"
    "testing"
)

func TestApplySearchReplace(t *testing.T) {
    dir := t.TempDir()
    orig, err := os.Getwd()
    if err != nil {
        t.Fatal(err)
    }
    if err := os.Chdir(dir); err != nil {
        t.Fatal(err)
    }
    defer os.Chdir(orig)

    if err := os.WriteFile("a.txt", []byte("hello\nworld\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := ApplySearchReplace("a.txt", "world", "there"); err != nil {
        t.Fatalf("apply: %v", err)
    }
    b, _ := os.ReadFile("a.txt")
    if string(b) != "hello\nthere\n" {
        t.Fatalf("got %q", b)
    }
    if err := ApplySearchReplace("a.txt", "nope", "x"); err == nil {
        t.Fatal("expected error for missing search block")
    }
    if err := ApplySearchReplace("a.txt", "", "x"); err == nil {
        t.Fatal("expected error for empty search block")
    }

    // CRLF in, LF out.
    os.WriteFile("crlf.txt", []byte("one\r\ntwo\r\n"), 0o644)
    if err := ApplySearchReplace("crlf.txt", "two", "TWO"); err != nil {
        t.Fatalf("crlf: %v", err)
    }
    b, _ = os.ReadFile("crlf.txt")
    if string(b) != "one\nTWO\n" {
        t.Fatalf("crlf got %q", b)
    }
}

func TestApplySearchReplaceRejectsOutsideRoots(t *testing.T) {
    home, _ := os.UserHomeDir()
    f, err := os.CreateTemp("", "termux-agent-outside-*")
    if err != nil {
        t.Skip(err)
    }
    name := f.Name()
    f.Close()
    defer os.Remove(name)
    if strings.HasPrefix(name, home) {
        t.Skip("temp dir is under HOME; cannot test denial")
    }
    if err := ApplySearchReplace(name, "x", "y"); err == nil {
        t.Fatalf("expected denial for %s", name)
    }
}

func TestExtractPatches(t *testing.T) {
    text := "### a.txt\n<<<<<<< SEARCH\none\n=======\ntwo\n>>>>>>> REPLACE\n"
    ps := ExtractPatches(text)
    if len(ps) != 1 || ps[0].File != "a.txt" || ps[0].Search != "one" || ps[0].Replace != "two" {
        t.Fatalf("got %+v", ps)
    }
}