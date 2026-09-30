// Command backend-check is shared by local development, pre-commit and CI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	fix := flag.Bool("fix", false, "format Go source files in place")
	fmtOnly := flag.Bool("fmt-only", false, "skip go vet")
	staged := flag.Bool("staged", false, "also check the exact staged Go source")
	flag.Parse()
	if err := run(*fix, *fmtOnly, *staged); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(fix, fmtOnly, staged bool) error {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("find repository: %w", err)
	}
	if err := os.Chdir(strings.TrimSpace(string(root))); err != nil {
		return err
	}
	files, err := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go").Output()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	bad := false
	for _, path := range strings.Split(string(files), "\x00") {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		mode := "-l"
		if fix {
			mode = "-w"
		}
		out, err := exec.Command("gofmt", mode, path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("gofmt %s: %w\n%s", path, err, out)
		}
		if len(bytes.TrimSpace(out)) > 0 {
			fmt.Print(string(out))
			bad = true
		}
	}
	if staged {
		paths, err := exec.Command("git", "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "*.go").Output()
		if err != nil {
			return err
		}
		for _, path := range strings.Split(string(paths), "\x00") {
			if path == "" {
				continue
			}
			content, err := exec.Command("git", "show", ":"+path).Output()
			if err != nil {
				return err
			}
			cmd := exec.Command("gofmt", "-d")
			cmd.Stdin = bytes.NewReader(content)
			out, err := cmd.CombinedOutput()
			if err != nil && !bytes.HasPrefix(out, []byte("diff ")) {
				return fmt.Errorf("staged %s: %w\n%s", path, err, out)
			}
			if len(out) > 0 {
				fmt.Printf("Staged Go source needs formatting: %s\n", path)
				bad = true
			}
		}
	}
	if bad {
		return fmt.Errorf("formatting failed; run: go run ./tools/backend-check -fix -fmt-only, then stage the changes")
	}
	fmt.Println("gofmt: passed")
	if fmtOnly {
		return nil
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("backend lint (go vet) failed: %w", err)
	}
	fmt.Println("go vet: passed")
	return nil
}
