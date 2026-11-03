package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a git repository in a temp dir, chdirs into it and returns a
// helper that writes files and commits.
func repo(t *testing.T) (write func(name, content string), commit func(msg string)) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	write = func(name, content string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(strings.TrimPrefix(content, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit = func(msg string) {
		git("add", "-A")
		git("commit", "-q", "-m", msg)
	}
	return write, commit
}

// recap runs the tool in-process and returns stdout and the exit code.
func recap(t *testing.T, args ...string) (string, int) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	code := run(append([]string{"-A"}, args...))
	w.Close()
	os.Stdout = old
	var b bytes.Buffer
	b.ReadFrom(bufio.NewReader(r))
	return b.String(), code
}

const gpuV1 = `
package gpu

type Context struct{ n int }

func CreateDevice(ctx *Context) error {
	ctx.n = 1
	return nil
}

func Upload(ctx *Context, data []float32) {
	for _, d := range data {
		ctx.n += int(d)
	}
	ctx.n *= 2
}

func Dispatch(ctx *Context) {
	ctx.n++
}

func Readback(ctx *Context) int {
	return ctx.n
}
`

const gpuV2 = `
package gpu

import "fmt"

type Context struct{ n int }

func CreateDevice(ctx *Context, debug bool) error {
	ctx.n = 1
	return nil
}

func UploadScene(ctx *Context, data []float32) {
	for _, d := range data {
		ctx.n += int(d)
	}
	ctx.n *= 2
}

func Readback(ctx *Context) int {
	fmt.Println("readback")
	return ctx.n + 1
}

func Upscale(ctx *Context) {
	ctx.n *= 4
}
`

func TestOverview(t *testing.T) {
	write, commit := repo(t)
	write("gpu.go", gpuV1)
	write("README.md", "# demo\n\n## Install\n\ngo install\n\n## Usage\n\nrun it\n")
	commit("first")
	write("gpu.go", gpuV2)
	write("README.md", "# demo\n\n## Install\n\ngo install ./...\n\n## Usage\n\nrun it\n")
	write("notes.txt", "todo\n")
	out, code := recap(t)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"recap: HEAD → working tree: 2 files changed, +",
		", 1 untracked",
		"changed  func CreateDevice",
		"was: func CreateDevice(ctx *Context) error",
		"now: func CreateDevice(ctx *Context, debug bool) error",
		"changed  func Readback",
		"renamed  func Upload → UploadScene",
		"added    func Upscale",
		"removed  func Dispatch  was L17-19",
		"imports  +2 −0: +import \"fmt\"",
		"changed  ## Install",
		"notes.txt  untracked, 1 line",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "## Usage") {
		t.Errorf("an unchanged section is listed:\n%s", out)
	}
}

func TestShowPrintsOnlyThatSymbol(t *testing.T) {
	write, commit := repo(t)
	write("gpu.go", gpuV1)
	commit("first")
	write("gpu.go", gpuV2)
	out, _ := recap(t, "--show", "gpu.go:Readback")
	if !strings.Contains(out, "+\treturn ctx.n + 1") || strings.Contains(out, "Upscale") {
		t.Errorf("got:\n%s", out)
	}
	out, code := recap(t, "--show", "Nope")
	if code != 2 || out != "" {
		t.Errorf("unknown symbol: exit %d, stdout %q", code, out)
	}
}

func TestRangesAndLog(t *testing.T) {
	write, commit := repo(t)
	write("gpu.go", gpuV1)
	commit("first")
	write("gpu.go", gpuV2)
	commit("second")
	out, _ := recap(t, "HEAD~1", "HEAD")
	if !strings.Contains(out, "recap: HEAD~1 → HEAD: 1 file changed") || strings.Contains(out, "untracked") {
		t.Errorf("got:\n%s", out)
	}
	out, _ = recap(t, "log", "-n", "5")
	for _, want := range []string{"second", "gpu.go  +", "renamed Upload → UploadScene", "removed Dispatch", "first", "gpu.go  new, 23 lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("log: missing %q in\n%s", want, out)
		}
	}
}

func TestNoChangesAndBadInput(t *testing.T) {
	write, commit := repo(t)
	write("a.go", "package a\n")
	commit("first")
	if out, code := recap(t); code != 0 || !strings.Contains(out, "no changes") {
		t.Errorf("clean tree: exit %d\n%s", code, out)
	}
	if _, code := recap(t, "nosuchbranch"); code != 2 {
		t.Errorf("unknown revision: exit %d", code)
	}
	if _, code := recap(t, "--bogus"); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
}

func TestBinaryFiles(t *testing.T) {
	write, commit := repo(t)
	write("a.go", "package a\n")
	commit("first")
	write("logo.png", "\x89PNG\x00\x00\x01\n\x02\n")
	commit("logo")
	out, _ := recap(t, "log", "-n", "1")
	if !strings.Contains(out, "logo.png  new, binary") {
		t.Errorf("got:\n%s", out)
	}
}

// Found while building the lab: moves and extractions must not look like new code.
func TestMovesAndExtractions(t *testing.T) {
	write, commit := repo(t)
	write("sort.go", `
package fsx

func Compare(a, b string) int {
	la := fold(a)
	lb := fold(b)
	if la < lb {
		return -1
	}
	return 1
}
`)
	write("cart.py", "class Cart:\n    pass\n\n\ndef apply_coupon(cart, code):\n    total = cart.total()\n    if code:\n        total *= 0.9\n    return total\n")
	write("api.ts", "export function fetchUser(id: number) {\n  return id;\n}\n\nexport function fetchTeam(id: number) {\n  return id;\n}\n\nexport class Client {\n  get() {}\n}\n")
	commit("first")
	// an extracted helper with one line changed (a planted bug)
	write("sort.go", `
package fsx

func Compare(a, b string) int {
	return compare(a, b)
}

func compare(a, b string) int {
	la := fold(a)
	lb := b
	if la < lb {
		return -1
	}
	return 1
}
`)
	// a function moved to another file
	write("cart.py", "class Cart:\n    pass\n")
	write("discounts.py", "def apply_coupon(cart, code):\n    total = cart.total()\n    if code:\n        total *= 0.9\n    return total\n")
	// a file moved and edited too much for git to call it a rename
	os.MkdirAll("net", 0o755)
	os.Remove("api.ts")
	write("net/client.ts", "export function fetchUser(id: number, signal?: AbortSignal) {\n  return id + 0;\n}\n\nexport function fetchTeam(id: number) {\n  return id * 1;\n}\n\nexport class Client {\n  get() { return 1; }\n}\n")
	out, _ := recap(t)
	for _, want := range []string{
		"added    func compare  L7-14  (body taken from Compare, 1 line differs)",
		"removed  def apply_coupon  was L5-9  (moved to discounts.py)",
		"discounts.py  untracked, 5 lines: def apply_coupon (from cart.py)",
		"net/client.ts  moved from api.ts (git saw a delete and a new file; paired by their definitions), +4 −4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// committed, the moved file is paired with the deleted one
	commit("refactor")
	out, _ = recap(t, "HEAD~1", "HEAD")
	if !strings.Contains(out, "net/client.ts  moved from api.ts (git saw a delete and a new file; paired by their definitions)") ||
		strings.Contains(out, "api.ts  deleted") {
		t.Errorf("moved file not paired:\n%s", out)
	}
	if !strings.Contains(out, "was: export function fetchUser(id: number)") {
		t.Errorf("signature change inside the moved file missing:\n%s", out)
	}
}

func TestConflictsWhitespaceAndCounts(t *testing.T) {
	write, commit := repo(t)
	write("m.go", "package m\n\nfunc Tidy() int {\n\treturn 1\n}\n\nfunc Other() int {\n\treturn 1\n}\n")
	commit("first")
	write("m.go", "package m\n\nfunc Tidy() int {\n    return 1\n}\n\nfunc Other() int {\n\treturn 1\n}\n")
	write("scratch.txt", "a\nb\nc\nd\n")
	out, _ := recap(t)
	if !strings.Contains(out, "changed  func Tidy  L3-5  +1 −1  (whitespace only)") {
		t.Errorf("whitespace-only change:\n%s", out)
	}
	if !strings.Contains(out, "1 file changed, +1 −1, 1 untracked") {
		t.Errorf("untracked lines must not count in the totals:\n%s", out)
	}
	commit("spaces")
	exec.Command("git", "checkout", "-q", "-b", "other", "HEAD~1").Run()
	write("m.go", "package m\n\nfunc Tidy() int {\n\treturn 2\n}\n")
	commit("two")
	exec.Command("git", "checkout", "-q", "main").Run()
	exec.Command("git", "-c", "user.name=A", "-c", "user.email=a@b", "merge", "-q", "other").Run()
	out, _ = recap(t)
	if !strings.Contains(out, "m.go  merge conflict (unresolved)") || !strings.Contains(out, "1 file with merge conflicts") {
		t.Errorf("conflict not flagged:\n%s", out)
	}
}
