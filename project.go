package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxProjectContextChars = 6000

// ProjectContext gathers lightweight signals about the working tree so the
// agent can build enterprise apps without rediscovering the stack every turn.
type ProjectContext struct {
	CWD          string
	StackHints   []string
	Instruction  string
	ManifestBits []string
}

func DetectProject() ProjectContext {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	pc := ProjectContext{CWD: cwd}

	type hint struct {
		file string
		name string
	}
	hints := []hint{
		{"go.mod", "Go"},
		{"package.json", "Node/JavaScript"},
		{"pnpm-workspace.yaml", "Node monorepo (pnpm)"},
		{"Cargo.toml", "Rust"},
		{"pyproject.toml", "Python"},
		{"requirements.txt", "Python"},
		{"pom.xml", "Java/Maven"},
		{"build.gradle", "Android/Gradle"},
		{"build.gradle.kts", "Android/Gradle"},
		{"Composer.json", "PHP"},
		{"composer.json", "PHP"},
		{"Gemfile", "Ruby"},
		{"mix.exs", "Elixir"},
		{"pubspec.yaml", "Dart/Flutter"},
		{"CMakeLists.txt", "C/C++"},
		{"Makefile", "Make"},
		{"docker-compose.yml", "Docker Compose"},
		{"Dockerfile", "Docker"},
	}
	seen := map[string]bool{}
	for _, h := range hints {
		if fileExists(filepath.Join(cwd, h.file)) && !seen[h.name] {
			pc.StackHints = append(pc.StackHints, h.name)
			seen[h.name] = true
		}
	}

	// Prefer project instruction files (Crush/Claude-style AGENTS.md).
	for _, name := range []string{"AGENTS.md", ".termux-agent.md", "CLAUDE.md", ".cursorrules"} {
		p := filepath.Join(cwd, name)
		if !fileExists(p) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if len(text) > 3500 {
			text = text[:3500] + "\n…[truncated]"
		}
		pc.Instruction = fmt.Sprintf("From %s:\n%s", name, text)
		break
	}

	// Small manifest excerpts help choose build/test commands.
	for _, name := range []string{"go.mod", "package.json", "README.md", "Cargo.toml", "pyproject.toml"} {
		p := filepath.Join(cwd, name)
		if !fileExists(p) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := string(data)
		if len(text) > 800 {
			text = text[:800] + "\n…[truncated]"
		}
		pc.ManifestBits = append(pc.ManifestBits, fmt.Sprintf("--- %s ---\n%s", name, text))
		if len(pc.ManifestBits) >= 2 {
			break
		}
	}
	return pc
}

func (pc ProjectContext) PromptBlock() string {
	var b strings.Builder
	fmt.Fprintf(&b, "WORKING DIRECTORY: %s\n", pc.CWD)
	if len(pc.StackHints) > 0 {
		fmt.Fprintf(&b, "DETECTED STACK: %s\n", strings.Join(pc.StackHints, ", "))
	} else {
		b.WriteString("DETECTED STACK: (unknown — inspect with list_directory / glob_files)\n")
	}
	if pc.Instruction != "" {
		b.WriteString("\nPROJECT INSTRUCTIONS:\n")
		b.WriteString(pc.Instruction)
		b.WriteString("\n")
	}
	if len(pc.ManifestBits) > 0 {
		b.WriteString("\nPROJECT MANIFEST EXCERPTS:\n")
		b.WriteString(strings.Join(pc.ManifestBits, "\n"))
		b.WriteString("\n")
	}
	out := b.String()
	if len(out) > maxProjectContextChars {
		out = out[:maxProjectContextChars] + "\n…[truncated]"
	}
	return out
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// TermuxEnvironment returns a short runtime snapshot for the system prompt.
func TermuxEnvironment() string {
	var parts []string
	if p := os.Getenv("TERMUX_VERSION"); p != "" {
		parts = append(parts, "Termux "+p)
	} else if os.Getenv("TERMUX_PREFIX") != "" {
		parts = append(parts, "Termux")
	}
	if arch := os.Getenv("TERMUX_ARCH"); arch != "" {
		parts = append(parts, "arch="+arch)
	}
	if prefix := os.Getenv("TERMUX_PREFIX"); prefix != "" {
		parts = append(parts, "prefix="+prefix)
	}
	if goos := os.Getenv("GOOS"); goos != "" {
		parts = append(parts, "GOOS="+goos)
	}
	if len(parts) == 0 {
		return "Runtime: generic Unix (Termux-optimized agent)"
	}
	return "Runtime: " + strings.Join(parts, " | ")
}
