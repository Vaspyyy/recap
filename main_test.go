package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	out, _, code := recapAll(t, args...)
	return out, code
}

// recapAll is recap that also returns what went to stderr.
func recapAll(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	capture := func(f **os.File) func() string {
		r, w, _ := os.Pipe()
		old := *f
		*f = w
		done := make(chan string)
		go func() {
			var b bytes.Buffer
			b.ReadFrom(bufio.NewReader(r))
			done <- b.String()
		}()
		return func() string {
			w.Close()
			*f = old
			return <-done
		}
	}
	out, errs := capture(&os.Stdout), capture(&os.Stderr)
	code = run(append([]string{"-A"}, args...))
	return out(), errs(), code
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
	if !strings.Contains(out, "net/client.ts  moved from api.ts (git: deleted + new file; paired by their definitions)") ||
		strings.Contains(out, "api.ts  deleted") {
		t.Errorf("moved file not paired:\n%s", out)
	}
	if !strings.Contains(out, "was: export function fetchUser(id: number)") {
		t.Errorf("signature change inside the moved file missing:\n%s", out)
	}
	// the old path names the moved file too
	if out, _ = recap(t, "HEAD~1", "HEAD", "--show", "api.ts:fetchUser"); !strings.HasPrefix(out, "changed function fetchUser  (net/client.ts L1-3)\n") {
		t.Errorf("--show api.ts:fetchUser (the old path):\n%s", out)
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
		"added    function over#2  L2", // the second of three definitions named over
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
	if out, _ := recap(t, "--show", "a.ts:over#2"); !strings.HasPrefix(out, "added function over#2  (a.ts L2)\n+function over(a: boolean): boolean;") {
		t.Errorf("--show a.ts:over#2:\n%s", out)
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
		// (every differing line is shown, also the first)
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "−9") || strings.Contains(out, "−10") {
		t.Errorf("Compare's count should follow git's (+1 −…), not the moved body:\n%s", out)
	}
}

// Found while recording the preview: the first differing line of an
// extracted body was dropped as if it were the signature.
func TestExtractionShowsEveryDifferingLine(t *testing.T) {
	write, commit := repo(t)
	write("m.go", "package m\n\nfunc Parse(s string) int {\n\ts = trim(s)\n\tn := len(s)\n\tn = n * 2\n\treturn n\n}\n")
	commit("first")
	write("m.go", "package m\n\nfunc Parse(s string) int {\n\treturn parse(trim(s))\n}\n\nfunc parse(s string) int {\n\tn := len(s)\n\tn = n * 3\n\treturn n\n}\n")
	out, _ := recap(t)
	for _, want := range []string{"(body taken from Parse, 2 lines differ)", "- s = trim(s)", "- n = n * 2", "+ n = n * 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// Regressions found by the final review round.
func TestFinalRoundRegressions(t *testing.T) {
	write, commit := repo(t)
	write("r.py", "import functools\n\n\n@functools.cache\ndef load(path):\n    return open(path).read()\n\n\ndef a():\n    return 1\n\n\ndef round_cents(x):\n    return round(x, 2)\n\n\ndef parse_date(s):\n    y, m, d = s.split('-')\n    return int(y), int(m), int(d)\n")
	write("legacy.ts", "export function oldHelper(x: number): number {\n  return x * 2;\n}\n")
	write("tests/test_r.py", "def test_load():\n    pass\n")
	write("item.go", "package m\n\ntype Item struct {\n\tKind  string\n\tTitle string\n\tLoc   string\n}\n")
	commit("first")
	write("r.py", "import functools\n\n\n@functools.lru_cache(maxsize=None)\ndef load(path):\n    return open(path).read()\n\n\ndef a():  # hi\n    return 1\n\n\ndef round_cents(amount):\n    return round(amount + 1e-9, 2)\n\n\ndef parse_data(blob):\n    if not blob:\n        raise ValueError('empty')\n    return blob.decode()\n")
	os.Remove("legacy.ts")
	write("tests/test_r.py", "def test_load():\n    pass\n\n\ndef test_more():\n    pass\n")
	write("item.go", "package m\n\ntype Item struct {\n\tKind   string\n\tTitle  string\n\tLoc    string\n\tDetail []string\n}\n")
	out, _ := recap(t)
	for _, want := range []string{
		"changed  def load  L5-6  +1 −1  (decorators changed)",
		"+ return round(amount + 1e-9, 2)",
		"removed  def parse_date",
		"added    def parse_data",
		"legacy.ts  deleted, 3 lines: function oldHelper (public)",
		"added    def test_more  L5-6\n",
		"changed  type Item  L3-8  +1 −0",
		"+ Detail []string",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, bad := range []string{"doc comment only", "changed  def a  L9-10  +1 −1  (public)", "was: def a", "renamed  def parse_date", "test_more  L5-6  (public"} {
		if strings.Contains(out, bad) {
			t.Errorf("should not contain %q:\n%s", bad, out)
		}
	}
	out, _ = recap(t, "--show", "load")
	if !strings.Contains(out, "+@functools.lru_cache(maxsize=None)") {
		t.Errorf("--show should include the decorator:\n%s", out)
	}
	for _, args := range [][]string{{"log", "--staged"}, {"--show", "a", "--show", "b"}} {
		if _, code := recap(t, args...); code != 2 {
			t.Errorf("recap %v should be an error", args)
		}
	}
}

// Paths in refs are read from the repository root first, as recap prints
// them, then from the working directory, then as a unique suffix.
func TestShowFromASubdirectory(t *testing.T) {
	write, commit := repo(t)
	write("sub/h.py", "def helper():\n    return 1\n")
	write("h.py", "def helper():\n    return 1\n")
	write("sub/only.py", "def only():\n    return 1\n")
	commit("first")
	write("sub/h.py", "def helper():\n    return 2\n")
	write("h.py", "def helper():\n    return 3\n")
	write("sub/only.py", "def only():\n    return 4\n")
	root, _ := recap(t)
	os.Chdir("sub")
	if here, _ := recap(t); here != root || !strings.Contains(here, "\nsub/h.py  modified") {
		t.Errorf("paths must be printed relative to the root, also from sub/:\n%s\nvs from the root:\n%s", here, root)
	}
	out, _ := recap(t, "--show", "h.py:helper")
	if !strings.Contains(out, "+    return 3") || strings.Contains(out, "return 2") ||
		!strings.HasPrefix(out, "note: h.py is the file at the repository root (recap reads paths from there, as it prints them); sub/h.py, relative to here, changed too\n") {
		t.Errorf("h.py from sub/ is the h.py recap prints, with a note on sub/h.py:\n%s", out)
	}
	for _, args := range [][]string{{"--show", "sub/h.py:helper"}, {"--show", "./sub/h.py:3-1"}} {
		if out, _ := recap(t, args...); !strings.Contains(out, "+    return 2") || strings.Contains(out, "note:") {
			t.Errorf("recap %v from sub/:\n%s", args, out)
		}
	}
	out, _ = recap(t, "--show", "../h.py:helper")
	if !strings.Contains(out, "+    return 3") {
		t.Errorf("../h.py from sub/ is h.py:\n%s", out)
	}
	for _, target := range []string{"only.py:only", "sub/only.py:only", "only.py:1", "only.py"} {
		if out, code := recap(t, "--show", target); code != 0 || !strings.Contains(out, "+    return 4") {
			t.Errorf("--show %s from sub/ (relative to here): exit %d\n%s", target, code, out)
		}
	}
}

// Found while using recap on glint and seek.
func TestFoundInUse(t *testing.T) {
	write, commit := repo(t)
	write("internal/rules/rules.go", "package rules\n\nvar (\n\ta = 1\n\tb = 2\n\tc = 3\n)\n\nfunc Parse(s string, hash bool) int {\n\tsc := scan{hash: hash}\n\treturn sc.n\n}\n")
	write("only.go", "package m\n\nvar (\n\tx = 1\n\ty = 2\n\tz = 3\n)\n\nfunc F() {}\n")
	commit("first")
	write("internal/rules/rules.go", "package rules\n\nvar (\n\ta = 1\n\tb = 2\n\tc = 3\n)\n\nfunc Parse(s string, lg *lang) int {\n\tsc := scan{hash: lg.hash}\n\treturn sc.n\n}\n")
	write("only.go", "package m\n\nvar (\n\tx = 10\n\ty = 20\n\tz = 30\n)\n\nfunc F() {}\n")
	out, _ := recap(t)
	expect := []string{
		"was: func Parse(s string, hash bool) int",
		"- sc := scan{hash: hash}",
		"only.go  modified, +3 −3\n  also     lines outside definitions  +3 −3",
	}
	for _, w := range expect {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
	for _, bad := range []string{"- func Parse(", "(public)"} {
		if strings.Contains(out, bad) {
			t.Errorf("should not contain %q (signature twice, or internal/ as public API):\n%s", bad, out)
		}
	}
}

func TestHelpColorsOnlyWhenAsked(t *testing.T) {
	if plain := helpText(false); strings.Contains(plain, "\x1b") || !strings.Contains(plain, "--budget N") {
		t.Errorf("plain help:\n%s", plain)
	}
	if colored := helpText(true); !strings.Contains(colored, hSection+"USAGE"+hReset) {
		t.Errorf("colored help has no colored section titles:\n%s", colored)
	}
}

const groupV1 = `
package group

type Group struct{ n int }

func (g *Group) Add(x int) {
	g.n += x
}

func (g *Group) Count() int {
	return g.n
}

func Helper() int {
	return 1
}

func (g *Group) Reset() {
	g.n = 0
}
`

// refsRepo commits a first version and a second one of a few files, for
// refs: duplicates, impl members, nested functions, a rename, a deleted file.
func refsRepo(t *testing.T) {
	write, commit := repo(t)
	write("internal/group/group.go", groupV1)
	write("lib.cpp", "int parse(int a) {\n  return a;\n}\nint parse(double a) {\n  return 0;\n}\nint Foo::baz() {\n  return 2;\n}\n")
	write("shape.rs", "pub struct Circle {\n    pub r: f64,\n}\n\nimpl Circle {\n    pub fn area(&self) -> f64 {\n        1.0\n    }\n}\n\nimpl Shape for Circle {\n    fn name(&self) -> String {\n        \"c\".into()\n    }\n}\n")
	write("nest.py", "def outer():\n    def inner():\n        return 1\n    return inner\n")
	write("legacy.go", "package old\n\nfunc Gone() int {\n\treturn 1\n}\n\nfunc Other() int {\n\treturn 2\n}\n")
	write("README.md", "# demo\n\n## Contact @me\n\nmail\n")
	commit("first")
	g := strings.Replace(strings.Replace(groupV1, "g.n += x", "g.n += x * 2", 1), "return 1", "return 2", 1)
	write("internal/group/group.go", strings.Replace(g, "Count()", "Size()", 1))
	write("lib.cpp", "int parse(int a) {\n  return a;\n}\nint parse(double a) {\n  return 1;\n}\nint Foo::baz() {\n  return 3;\n}\n")
	write("shape.rs", "pub struct Circle {\n    pub r: f64,\n}\n\nimpl Circle {\n    pub fn area(&self) -> f64 {\n        3.14\n    }\n}\n\nimpl Shape for Circle {\n    fn name(&self) -> String {\n        \"c\".into()\n    }\n}\n")
	write("nest.py", "def outer():\n    def inner():\n        return 2\n    return inner\n\n\ndef fresh():\n    def helper():\n        return 1\n    return helper\n")
	os.Remove("legacy.go")
	write("README.md", "# demo\n\n## Contact @me\n\nmail me\n")
	commit("second")
}

var changeLineRe = regexp.MustCompile(`^  (?:changed|added|removed|renamed|moved) +\S+ (.+?)  (?:was )?L\d`)

// Every symbol in the overview, its file's header path + ":" + its name as
// shown, is a ref --show accepts, also with @rev and from a subdirectory.
func TestOverviewPrintsRefs(t *testing.T) {
	refsRepo(t)
	out, _ := recap(t, "HEAD~1", "HEAD")
	for _, want := range []string{
		"changed  func Group.Add  L5-7",
		"renamed  func Group.Count → Group.Size  L9-11",
		"changed  func parse#2  L4-6",
		"changed  func Foo::baz  L7-9",
		"changed  fn Circle.area  L6-8",
		"changed  def outer.inner  L2-3",
		"added    def fresh  L7-10  (public; with 1 inside)",
		"changed  ## Contact @me  L3-5",
		"legacy.go  deleted, 9 lines: func Gone (public), func Other (public)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	var path string
	var refs []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" && l[0] != ' ' && !strings.HasPrefix(l, "recap:") {
			path, _, _ = strings.Cut(l, "  ")
		} else if m := changeLineRe.FindStringSubmatch(l); m != nil {
			for _, name := range strings.Split(m[1], " → ") {
				refs = append(refs, path+":"+name)
			}
		}
	}
	if len(refs) != 10 {
		t.Fatalf("expected 10 refs (a rename has two), got %q from\n%s", refs, out)
	}
	check := func(where string, args ...string) {
		t.Helper()
		target := args[len(args)-1]
		name := target[strings.IndexByte(target, ':')+1:]
		name = strings.TrimSuffix(name, "@HEAD")
		if out, code := recap(t, args...); code != 0 || strings.Contains(out, "matches for") || !strings.Contains(strings.SplitN(out, "\n", 2)[0], " "+name) {
			t.Errorf("%s: recap %v: exit %d\n%s", where, args, code, out)
		}
	}
	for _, r := range refs {
		check("from the root", "HEAD~1", "HEAD", "--show", r)
		check("from the root", "--show", r+"@HEAD")
	}
	os.Chdir("internal")
	if here, _ := recap(t, "HEAD~1", "HEAD"); here != out {
		t.Errorf("paths must be printed relative to the root, also from internal/:\n%s", here)
	}
	for _, r := range refs {
		check("from internal/", "--show", r+"@HEAD")
	}
	// relative to here, and a unique suffix
	for _, target := range []string{"group/group.go:Helper@HEAD", "group.go:Helper@HEAD", "group.go:14@HEAD"} {
		if out, code := recap(t, "--show", target); code != 0 || !strings.HasPrefix(out, "changed func Helper  (internal/group/group.go L13-15)\n") {
			t.Errorf("--show %s from internal/: exit %d\n%s", target, code, out)
		}
	}
}

func TestShowRefs(t *testing.T) {
	refsRepo(t)
	for _, c := range []struct {
		target string
		want   []string // the first line, then other lines that must be there
	}{
		// a location: the changed symbol containing the line, in the new version
		{"internal/group/group.go:6", []string{"changed func Group.Add  (internal/group/group.go L5-7)", "+\tg.n += x * 2"}},
		{"internal/group/group.go:6:3", []string{"changed func Group.Add  (internal/group/group.go L5-7)"}},
		{"internal/group/group.go:5-7", []string{"changed func Group.Add  (internal/group/group.go L5-7)"}},
		{"internal/group/group.go:10", []string{"renamed func Group.Count → Group.Size  (internal/group/group.go L9-11)"}},
		// inside a nested function of an added one: the added one
		{"nest.py:9", []string{"added def fresh  (nest.py L7-10)", "+    def helper():"}},
		// a deleted file only has its old lines
		{"legacy.go:8", []string{"removed func Other  (legacy.go, was L7-9)", "-\treturn 2"}},
		{"legacy.go:Gone", []string{"removed func Gone  (legacy.go, was L3-5)"}},
		// duplicates: the overview's "#N", counted over every definition
		{"lib.cpp:parse#2", []string{"changed func parse#2  (lib.cpp L4-6)", "+  return 1;"}},
		{"parse", []string{"changed func parse#2  (lib.cpp L4-6)"}},
		// a name with ':' in it, without a path
		{"Foo::baz", []string{"changed func Foo::baz  (lib.cpp L7-9)"}},
		// a name ending in the given one at a '.'; a type shows its changed members
		{"area", []string{"changed fn Circle.area  (shape.rs L6-8)"}},
		{"shape.rs:Circle", []string{"changed fn Circle.area  (shape.rs L6-8)"}},
		{"inner", []string{"changed def outer.inner  (nest.py L2-3)"}},
		// an '@' that isn't a revision is part of the name
		{"README.md:Contact @me", []string{"changed ## Contact @me  (README.md L3-5)"}},
	} {
		out, code := recap(t, "HEAD~1", "HEAD", "--show", c.target)
		first, _, _ := strings.Cut(out, "\n")
		if code != 0 || first != c.want[0] {
			t.Errorf("--show %s: exit %d, want first line %q:\n%s", c.target, code, c.want[0], out)
			continue
		}
		for _, w := range c.want[1:] {
			if !strings.Contains(out, w) {
				t.Errorf("--show %s: missing %q in\n%s", c.target, w, out)
			}
		}
	}
	// the new version of a file in a commit: impl blocks count with their type
	out, _ := recap(t, "show", "HEAD~1")
	if !strings.Contains(out, "shape.rs  new, 15 lines: struct Circle, impl Circle#2, impl Shape for Circle") {
		t.Errorf("an impl shares its type's name:\n%s", out)
	}
	if out, _ = recap(t, "--show", "shape.rs:Circle#2@HEAD~1"); !strings.HasPrefix(out, "added impl Circle#2  (shape.rs L5-9)\n") {
		t.Errorf("--show shape.rs:Circle#2@HEAD~1:\n%s", out)
	}
	// misses say what is there, as refs
	for _, c := range []struct{ target, want string }{
		{"internal/group/group.go:18", "recap: internal/group/group.go:18 is in internal/group/group.go:Group.Reset (L17-19), which didn't change in HEAD~1 → HEAD; " +
			"changed in internal/group/group.go: internal/group/group.go:Group.Add, internal/group/group.go:Helper, internal/group/group.go:Group.Size\n"},
		{"internal/group/group.go:2", "recap: internal/group/group.go:2 isn't inside a symbol; changed in internal/group/group.go: "},
		{"internal/group/group.go:99", "recap: internal/group/group.go:99 is past the end of internal/group/group.go (19 lines); "},
		{"lib.cpp:parse#1", "recap: no changed symbol matches \"lib.cpp:parse#1\"; changed: lib.cpp:parse#2, lib.cpp:Foo::baz\n"},
		{"Nope", "recap: no changed symbol matches \"Nope\"; changed: README.md:Contact @me, internal/group/group.go:Group.Add, " +
			"internal/group/group.go:Helper, internal/group/group.go:Group.Size, legacy.go:Gone, legacy.go:Other, lib.cpp:parse#2, lib.cpp:Foo::baz, " +
			"nest.py:outer.inner, nest.py:fresh, shape.rs:Circle.area\n"},
		{"nope.go:12", "recap: no changed file matches \"nope.go\" in HEAD~1 → HEAD; changed: README.md:Contact @me, internal/group/group.go:Group.Add, "},
		{"internal/group/group.go@HEAD~1", "recap: --show internal/group/group.go@HEAD~1 means recap show HEAD~1 --show internal/group/group.go, " +
			"which can't be combined with HEAD~1 HEAD; drop one of them\n"},
	} {
		if out, errs, code := recapAll(t, "HEAD~1", "HEAD", "--show", c.target); code != 2 || out != "" || !strings.HasPrefix(errs, c.want) {
			t.Errorf("--show %s: exit %d, stdout %q, want stderr starting with\n%s\ngot\n%s", c.target, code, out, c.want, errs)
		}
	}
	// @rev is "recap show rev": the same commit is fine, another one isn't
	if out, _ := recap(t, "show", "HEAD", "--show", "parse@HEAD"); !strings.HasPrefix(out, "changed func parse#2") {
		t.Errorf("recap show HEAD --show parse@HEAD:\n%s", out)
	}
	if _, errs, code := recapAll(t, "show", "HEAD~1", "--show", "parse@HEAD"); code != 2 ||
		errs != "recap: --show parse@HEAD is about commit HEAD, but recap show was given HEAD~1; give one commit (recap show HEAD --show parse)\n" {
		t.Errorf("two commits: exit %d, %s", code, errs)
	}
}
