package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTempCwd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	return dir
}

func TestWriteFileCreatesParentDirs(t *testing.T) {
	withTempCwd(t)

	msg, err := WriteFile("src/app/index.html", "<h1>hi</h1>")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(msg, "src/app/index.html") {
		t.Fatalf("unexpected message: %q", msg)
	}
	data, err := os.ReadFile(filepath.Join("src", "app", "index.html"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "<h1>hi</h1>" {
		t.Fatalf("got %q", data)
	}
}

func TestWriteFileOverwritesExisting(t *testing.T) {
	withTempCwd(t)

	if _, err := WriteFile("a.txt", "first"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteFile("a.txt", "second"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, _ := os.ReadFile("a.txt")
	if string(data) != "second" {
		t.Fatalf("got %q, want overwrite to take effect", data)
	}
}

func TestWriteFileRejectsDirectoryTarget(t *testing.T) {
	withTempCwd(t)

	if err := os.Mkdir("adir", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile("adir", "content"); err == nil {
		t.Fatal("expected error writing to a path that is a directory")
	}
}

func TestReadFileRoundTrip(t *testing.T) {
	withTempCwd(t)

	if _, err := WriteFile("notes.txt", "line one\nline two\n"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile("notes.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "line one\nline two\n" {
		t.Fatalf("got %q", got)
	}
}

func TestReadFileRejectsDirectory(t *testing.T) {
	withTempCwd(t)

	if err := os.Mkdir("adir", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile("adir"); err == nil {
		t.Fatal("expected error reading a directory")
	}
}

func TestListDirectorySkipsNoiseDirsAndSorts(t *testing.T) {
	withTempCwd(t)

	for _, p := range []string{"b.txt", "a.txt", "sub/c.txt", "node_modules/junk.js", ".git/HEAD"} {
		if _, err := WriteFile(p, "x"); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	out, err := ListDirectory(".")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "node_modules") || strings.Contains(out, ".git") {
		t.Fatalf("expected noise dirs to be skipped, got:\n%s", out)
	}
	wantOrder := []string{"a.txt", "b.txt", "sub/", "sub/c.txt"}
	for _, w := range wantOrder {
		if !strings.Contains(out, w) {
			t.Fatalf("expected listing to contain %q, got:\n%s", w, out)
		}
	}
}

func TestListDirectoryEmpty(t *testing.T) {
	withTempCwd(t)

	if err := os.Mkdir("empty", 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := ListDirectory("empty")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out != "(empty directory)" {
		t.Fatalf("got %q", out)
	}
}

func TestWriteFileRejectsOutsideRoots(t *testing.T) {
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
	if _, err := WriteFile(name, "nope"); err == nil {
		t.Fatalf("expected denial for %s", name)
	}
}
