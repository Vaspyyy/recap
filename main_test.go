package main

import (
	"bufio"
	"bytes"
	"fmt"
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
		"imports  +2 −0 (1 blank): +import \"fmt\"",
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
	for _, want := range []string{"second", "gpu.go  modified, +", "renamed Upload → UploadScene", "removed Dispatch", "first", "gpu.go  new, 23 lines"} {
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
		"removed  def apply_coupon  was L5-9  (public; moved to discounts.py, unchanged)",
		"discounts.py  untracked, 5 lines: def apply_coupon (from cart.py)",
		"net/client.ts  moved from api.ts (the new copy is untracked; paired by their definitions), +4 −4",
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

// Regressions found by the first review round.
func TestFirstRoundRegressions(t *testing.T) {
	write, commit := repo(t)
	write("cart.py", "class Cart:\n    pass\n\n\ndef apply_coupon(cart, code):\n    if code == 'WELCOME':\n        return cart.total() * 0.9\n    return cart.total()\n")
	write("money.py", "def round_cents(x):\n    return round(x, 2)\n\n\ndef format_price(x):\n    return f'{x:.2f} EUR'\n")
	write("api.ts", "export const formatName = (u: User) => u.name.trim();\n\nexport function other() {\n  return 1;\n}\n")
	write("shapes.rs", "pub struct Circle {\n    pub r: f64,\n}\n\nimpl Circle {\n    pub fn area(&self) -> f64 {\n        3.14 * self.r * self.r\n    }\n\n    pub fn name(&self) -> &str {\n        \"circle\"\n    }\n}\n")
	commit("first")
	// moved to another file with a new parameter: the move must show the signature
	write("cart.py", "class Cart:\n    pass\n")
	write("discounts.py", "def apply_coupon(cart, code, discounts):\n    d = discounts.get(code)\n    return d.apply(cart.total()) if d else cart.total()\n")
	// renamed with a new parameter; renamed with an identical one-line body
	write("money.py", "def round_cents(x):\n    return round(x, 2)\n\n\ndef format_amount(amount, currency='EUR'):\n    return f'{amount:.2f} {currency}'\n")
	write("api.ts", "export const displayName = (u: User) => u.name.trim();\n\nexport function other() {\n  return 1;\n}\n")
	// a doc comment belongs to the function below it; a one-line change counts one line
	write("shapes.rs", "pub struct Circle {\n    pub r: f64,\n}\n\nimpl Circle {\n    pub fn area(&self) -> f64 {\n        std::f64::consts::PI * self.r * self.r\n    }\n\n    /// The shape's name.\n    pub fn name(&self) -> String {\n        \"circle\".to_string()\n    }\n}\n")
	out, _ := recap(t)
	for _, want := range []string{
		"removed  def apply_coupon  was L5-8  (public; moved to discounts.py, 4 lines differ)",
		"def apply_coupon (from cart.py, signature changed)",
		"renamed  def format_price → format_amount  L5-6  +2 −2  (public; rewritten)",
		"was: def format_price(x):",
		"renamed  const formatName → displayName  L1  (public)",
		"changed  fn Circle.area  L6-8  +1 −1",
		"changed  fn Circle.name  L11-13  +3 −2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "lines outside definitions") {
		t.Errorf("the doc comment should belong to Circle.name:\n%s", out)
	}
	// --show: exactly the symbol, and a moved one against its old copy
	out, _ = recap(t, "--show", "Circle.area")
	if !strings.Contains(out, "-        3.14 * self.r * self.r") || strings.Contains(out, "circle") {
		t.Errorf("--show Circle.area bleeds or misses:\n%s", out)
	}
	out, _ = recap(t, "--show", "apply_coupon")
	if strings.Count(out, "apply_coupon  (") != 1 || !strings.Contains(out, "-def apply_coupon(cart, code):") || !strings.Contains(out, "+def apply_coupon(cart, code, discounts):") {
		t.Errorf("--show of a moved function should diff both copies:\n%s", out)
	}
	// -n without log, log and show in an empty repository
	if _, code := recap(t, "-n", "3"); code != 2 {
		t.Errorf("-n without log should be an error")
	}
	commit("second")
	out, _ = recap(t, "show")
	if !strings.Contains(out, "commit ") || !strings.Contains(out, "    second") || !strings.Contains(out, "renamed  def format_price → format_amount") {
		t.Errorf("recap show:\n%s", out)
	}
}

func TestEmptyRepoAndPathQuoting(t *testing.T) {
	write, _ := repo(t)
	if out, _ := recap(t, "log"); !strings.Contains(out, "no commits yet") {
		t.Errorf("log in an empty repo: %q", out)
	}
	write("we ird\ttab.py", "x = 1\n")
	out, _ := recap(t)
	if !strings.Contains(out, `"we ird\ttab.py"  untracked`) {
		t.Errorf("a tab in a file name must be quoted:\n%s", out)
	}
}

func TestBudgetIsFair(t *testing.T) {
	write, commit := repo(t)
	var big, big2 strings.Builder
	for i := range 400 {
		fmt.Fprintf(&big, "def f%d():\n    return %d\n\n\n", i, i)
		if i >= 10 {
			fmt.Fprintf(&big2, "def f%d():\n    return %d\n\n\n", i, i+1)
		}
	}
	write("big.py", big.String())
	write("small.py", "def g():\n    return 1\n")
	commit("first")
	write("big.py", big2.String())
	write("small.py", "def g(x):\n    return x\n")
	out, _ := recap(t)
	if !strings.Contains(out, "changed  def g  L1-2") {
		t.Errorf("a big file starved a small one:\n%s", out)
	}
	if !strings.Contains(out, "removed  def f0") || !strings.Contains(out, "390 changed (each +1 −1)") && !strings.Contains(out, "changed (each +1 −1)") {
		t.Errorf("removals must come first and the rest be summarised:\n%s", out)
	}
}

// Found by DeepSeek's round-1 run: a pure LF → CRLF change looked like a whitespace change.
func TestLineEndings(t *testing.T) {
	write, commit := repo(t)
	write("eol.py", "def g():\n    return 1\n")
	commit("first")
	write("eol.py", "def g():\r\n    return 1\r\n")
	out, _ := recap(t)
	if !strings.Contains(out, "eol.py  modified, +2 −2, line endings only (LF → CRLF)") || strings.Contains(out, "def g") {
		t.Errorf("got:\n%s", out)
	}
	write("eol.py", "def g():\r\n    return 2\r\n")
	if out, _ = recap(t, "--show", "g"); !strings.Contains(out, "+    return 2␍") {
		t.Errorf("--show should make the CR visible:\n%s", out)
	}
}

// Regressions found by the second review round.
func TestSecondRoundRegressions(t *testing.T) {
	write, commit := repo(t)
	write("lib.rs", "pub trait Shape {\n    fn area(&self) -> f64;\n}\n\npub fn total_area(v: &[f64]) -> f64 {\n    v.iter().sum()\n}\n\npub fn round_cents(x: f64) -> f64 {\n    (x * 100.0).round() / 100.0\n}\n")
	write("a.ts", "function over(a: string): string;\nfunction over(a: any) {\n  return a;\n}\n\nexport default function main(\n  a: number,\n  b: string,\n): void {\n  console.log(a, b);\n}\n")
	commit("first")
	// a doc comment above total_area is not a move; a small body change shows its lines;
	// a required trait method is flagged
	write("lib.rs", "pub trait Shape {\n    fn area(&self) -> f64;\n    fn name(&self) -> String;\n}\n\n/// Sum of all areas.\npub fn total_area(v: &[f64]) -> f64 {\n    v.iter().sum()\n}\n\npub fn round_cents(x: f64) -> f64 {\n    (x * 100.0 + 1e-9).round() / 100.0\n}\n")
	// a new overload before the implementation; a multi-line signature gains a parameter
	write("a.ts", "function over(a: string): string;\nfunction over(a: boolean): boolean;\nfunction over(a: any) {\n  return a;\n}\n\nexport default function main(\n  a: number,\n  b: string,\n  c: boolean,\n): void {\n  console.log(a, b);\n}\n")
	out, _ := recap(t)
	for _, want := range []string{
		"changed  fn total_area  L7-9  +1 −0  (doc comment only)",
		"changed  fn round_cents  L11-13  +1 −1",
		"- (x * 100.0).round() / 100.0",
		"+ (x * 100.0 + 1e-9).round() / 100.0",
		"added    fn Shape.name  L3  (public; required: every implementation must add it)",
		"added    function over  L2",
		"was: export default function main(a: number, b: string): void",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, bad := range []string{"moved", "changed  function over"} {
		if strings.Contains(out, bad) {
			t.Errorf("should not contain %q:\n%s", bad, out)
		}
	}
	// --staged with a range, --show with an empty name, a miss on a clean tree
	if _, code := recap(t, "--staged", "HEAD~1..HEAD"); code != 2 {
		t.Errorf("--staged with a range should be an error")
	}
	if _, code := recap(t, "--show", ""); code != 2 {
		t.Errorf("--show with an empty name should be an error")
	}
}

// The extracted-helper case from the lab: the overview itself shows the
// one line that differs, and the counts don't claim 50 removed lines.
func TestExtractionShowsTheDifferingLine(t *testing.T) {
	write, commit := repo(t)
	body := "\tla := fold(a)\n\tlb := fold(b)\n\tif la < lb {\n\t\treturn -1\n\t}\n\tif la > lb {\n\t\treturn 1\n\t}\n\treturn 0\n"
	write("s.go", "package s\n\nfunc Compare(a, b string) int {\n"+body+"}\n")
	commit("first")
	write("s.go", "package s\n\nfunc Compare(a, b string) int {\n\treturn compare(a, b)\n}\n\nfunc compare(a, b string) int {\n"+strings.Replace(body, "lb := fold(b)", "lb := b", 1)+"}\n")
	out, _ := recap(t)
	for _, want := range []string{"(body taken from Compare, 1 line differs)", "- lb := fold(b)", "+ lb := b"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "−9") || strings.Contains(out, "−10") {
		t.Errorf("Compare's count should follow git's (+1 −…), not the moved body:\n%s", out)
	}
}
