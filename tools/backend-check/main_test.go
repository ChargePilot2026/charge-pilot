package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestChecksStagedFormattingAndVetFailures(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "backend-check")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build checker: %v %s", err, out)
	}
	run := func(dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return out
	}
	for _, tc := range []struct {
		name, staged, worktree string
		args                   []string
		want                   string
	}{
		{"staged content", "package main\nfunc main(){println(1)}\n", "package main\n\nfunc main() { println(1) }\n", []string{"-staged", "-fmt-only"}, "Staged Go source needs formatting"},
		{"vet semantic check", "", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Printf(\"%d\", \"wrong type\") }\n", nil, "backend lint (go vet) failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run(dir, "init", "--quiet")
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module checkfixture\n\ngo 1.27.0\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "main.go")
			if tc.staged != "" {
				_ = os.WriteFile(path, []byte(tc.staged), 0600)
				run(dir, "add", "main.go")
			}
			if err := os.WriteFile(path, []byte(tc.worktree), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, tc.args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("checker should block: err=%v output=%s", err, out)
			}
		})
	}
}
