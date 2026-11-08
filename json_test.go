package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/prism/report"
)

// recapJSON runs recap --json, checks the envelope's shape, that nothing
// went to stderr and that every ref in it (and every candidate) is one
// ref.Parse reads back as is, and decodes the result.
func recapJSON(t *testing.T, args ...string) (*report.Decoded, report.Recap, int) {
	t.Helper()
	out, errs, code := recapAll(t, append([]string{"--json"}, args...)...)
	if errs != "" {
		t.Errorf("recap --json %v wrote to stderr: %s", args, errs)
	}
	d, err := report.Check([]byte(out))
	if err != nil {
		t.Fatalf("recap --json %v: %v\n%s", args, err, out)
	}
	if d.Tool != "recap" || d.Version != version || !reflect.DeepEqual(d.Args, append([]string{"-A", "--json"}, args...)) {
		t.Errorf("recap --json %v: tool %q, version %q, args %q", args, d.Tool, d.Version, d.Args)
	}
	refs, err := report.Refs([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if d.Error != nil {
		refs = append(refs, d.Error.Candidates...)
	}
	for _, r := range refs {
		// removed symbols are from the old version: the ref must parse, not resolve now
		if p := ref.Parse(r, nil); p.Path == "" || p.String() != r {
			t.Errorf("recap --json %v: ref %q doesn't read back (%+v)", args, r, p)
		}
	}
	var res report.Recap
	if d.Error == nil {
		if err := json.Unmarshal(d.Result, &res); err != nil {
			t.Fatalf("recap --json %v: result isn't a report.Recap: %v\n%s", args, err, out)
		}
	}
	return d, res, code
}

// git runs git in the test repository and returns its output.
func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func fileByPath(t *testing.T, files []report.RecapFile, path string) report.RecapFile {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file %s in %+v", path, files)
	return report.RecapFile{}
}

// changesByRef maps every change in the files by its symbol's ref.
func changesByRef(files []report.RecapFile) map[string]report.RecapChange {
	m := map[string]report.RecapChange{}
	for _, f := range files {
		for _, ch := range f.Changes {
			m[ch.Symbol.Ref] = ch
		}
	}
	return m
}

func defRefs(f report.RecapFile) []string {
	var out []string
	for _, d := range f.Defs {
		out = append(out, d.Ref)
	}
	return out
}

func TestJSONOverview(t *testing.T) {
	write, commit := repo(t)
	write("gpu.go", gpuV1)
	write("README.md", "# demo\n\n## Install\n\ngo install\n\n## Usage\n\nrun it\n")
	commit("first")
	write("gpu.go", gpuV2)
	write("README.md", "# demo\n\n## Install\n\ngo install ./...\n\n## Usage\n\nrun it\n")
	write("notes.txt", "todo\n")
	d, res, code := recapJSON(t)
	if code != 0 || d.Error != nil {
		t.Fatalf("exit %d, error %+v", code, d.Error)
	}
	if !d.Repo.Git || d.Repo.Root == "" || d.Repo.Branch != "main" || !d.Repo.Dirty || d.Truncated != nil {
		t.Errorf("envelope: repo %+v, truncated %+v", d.Repo, d.Truncated)
	}
	if res.Old != "HEAD" || res.New != "working tree" || res.Commit != nil || res.Commits != nil || res.Show != nil {
		t.Errorf("sides and parts: %+v", res)
	}
	if len(res.Files) != 3 {
		t.Fatalf("want gpu.go, README.md and notes.txt, got %+v", res.Files)
	}
	gpu := fileByPath(t, res.Files, "gpu.go")
	if gpu.Status != "modified" || gpu.Added != 9 || gpu.Removed != 6 || gpu.Lines != 26 || gpu.Outside == nil || *gpu.Outside != (report.LineCounts{Added: 2}) {
		t.Errorf("gpu.go: %+v (outside %+v)", gpu, gpu.Outside)
	}
	if n := fileByPath(t, res.Files, "notes.txt"); n.Status != "untracked" || n.Lines != 1 || n.Changes == nil {
		t.Errorf("notes.txt: %+v", n)
	}
	ch := changesByRef(res.Files)
	if c := ch["gpu.go:CreateDevice"]; c.What != "changed" || c.OldSig != "func CreateDevice(ctx *Context) error" ||
		c.NewSig != "func CreateDevice(ctx *Context, debug bool) error" || !c.Public || c.Symbol.Kind != "func" ||
		c.Symbol.Line != 7 || c.Symbol.End != 10 || !strings.HasPrefix(c.Symbol.Sig, "func CreateDevice(ctx *Context, debug bool) error") {
		t.Errorf("CreateDevice: %+v", c)
	}
	if c := ch["gpu.go:UploadScene"]; c.What != "renamed" || c.OldRef != "gpu.go:Upload" {
		t.Errorf("UploadScene: %+v", c)
	}
	if c := ch["gpu.go:Readback"]; c.What != "changed" || c.Added != 2 || c.Removed != 1 || c.OldSig != "" {
		t.Errorf("Readback: %+v", c)
	}
	if c := ch["gpu.go:Upscale"]; c.What != "added" || c.Symbol.Line != 24 || c.Added != 3 || c.Removed != 0 {
		t.Errorf("Upscale: %+v", c)
	}
	if c := ch["gpu.go:Dispatch"]; c.What != "removed" || c.Symbol.Line != 17 || c.Symbol.End != 19 || c.Removed != 3 {
		t.Errorf("Dispatch (lines of the old version): %+v", c)
	}
	if c := ch["README.md:Install"]; c.What != "changed" || c.Symbol.Kind != "h2" {
		t.Errorf("## Install: %+v in %v", c, ch)
	}
	if _, ok := ch["README.md:Usage"]; ok {
		t.Errorf("an unchanged section is listed")
	}
	// the same in a clean tree: no files, but a result
	commit("second")
	if _, res, code := recapJSON(t); code != 0 || res.Files == nil || len(res.Files) != 0 {
		t.Errorf("clean tree: exit %d, %+v", code, res)
	}
	// staged changes: the new side is the index
	write("gpu.go", gpuV1)
	gitOut(t, "add", "gpu.go")
	if _, res, _ := recapJSON(t, "--staged"); res.Old != "HEAD" || res.New != "index" || len(res.Files) != 1 {
		t.Errorf("--staged: %+v", res)
	}
}

// Every ref in the JSON is the text's (with "#N"), and --show takes it.
func TestJSONRefs(t *testing.T) {
	refsRepo(t)
	_, res, code := recapJSON(t, "HEAD~1", "HEAD")
	if code != 0 || res.Old != "HEAD~1" || res.New != "HEAD" {
		t.Fatalf("exit %d, %+v", code, res)
	}
	ch := changesByRef(res.Files)
	for ref, what := range map[string]string{
		"internal/group/group.go:Group.Add":  "changed",
		"internal/group/group.go:Group.Size": "renamed",
		"lib.cpp:parse#2":                    "changed",
		"lib.cpp:Foo::baz":                   "changed",
		"shape.rs:Circle.area":               "changed",
		"nest.py:outer.inner":                "changed",
		"nest.py:fresh":                      "added",
		"README.md:Contact @me":              "changed",
	} {
		if ch[ref].What != what {
			t.Errorf("%s: want %s, got %+v", ref, what, ch[ref])
		}
	}
	if c := ch["internal/group/group.go:Group.Size"]; c.OldRef != "internal/group/group.go:Group.Count" || c.Symbol.Line != 9 {
		t.Errorf("rename: %+v", c)
	}
	if c := ch["nest.py:fresh"]; c.Note != "with 1 inside" || !c.Public || c.Added != 4 {
		t.Errorf("an added function with a nested one: %+v", c)
	}
	legacy := fileByPath(t, res.Files, "legacy.go")
	if legacy.Status != "deleted" || legacy.Lines != 9 || len(legacy.Changes) != 0 ||
		!reflect.DeepEqual(defRefs(legacy), []string{"legacy.go:Gone", "legacy.go:Other"}) || legacy.Defs[0].Line != 3 {
		t.Errorf("a deleted file lists its definitions: %+v", legacy)
	}
	// each ref (and the old name of a rename, and a deleted file's
	// definitions) is a target for --show, which reports the same change
	var targets []string
	for r, c := range ch {
		targets = append(targets, r)
		if c.OldRef != "" {
			targets = append(targets, c.OldRef)
		}
	}
	targets = append(targets, defRefs(legacy)...)
	for _, target := range targets {
		_, s, code := recapJSON(t, "HEAD~1", "HEAD", "--show", target)
		if code != 0 || len(s.Show) != 1 || len(s.Files) != 0 {
			t.Errorf("--show %s: exit %d, %+v", target, code, s)
			continue
		}
		got := s.Show[0].Change
		if got.Symbol.Ref != target && got.OldRef != target {
			t.Errorf("--show %s reported %s (was %s)", target, got.Symbol.Ref, got.OldRef)
		}
		if c, ok := ch[got.Symbol.Ref]; ok && !reflect.DeepEqual(c, got) {
			t.Errorf("--show %s: the change differs from the overview's:\n%+v\n%+v", target, got, c)
		}
	}
	// impl blocks share their type's name: the second Circle is Circle#2
	_, res, _ = recapJSON(t, "show", "HEAD~1")
	shape := fileByPath(t, res.Files, "shape.rs")
	if shape.Status != "new" || !reflect.DeepEqual(defRefs(shape)[:3], []string{"shape.rs:Circle", "shape.rs:Circle#2", "shape.rs:Circle.area"}) ||
		shape.Defs[1].Kind != "impl" {
		t.Errorf("shape.rs: %+v", shape.Defs)
	}
	if res.Commit == nil || res.Commit.Parents != nil || res.Old != "nothing" {
		t.Errorf("a root commit: old %q, %+v", res.Old, res.Commit)
	}
}

func TestJSONShow(t *testing.T) {
	write, commit := repo(t)
	write("gpu.go", gpuV1)
	commit("first")
	write("gpu.go", gpuV2)
	_, res, code := recapJSON(t, "--show", "gpu.go:Readback")
	if code != 0 || len(res.Show) != 1 || len(res.Files) != 0 {
		t.Fatalf("exit %d, %+v", code, res)
	}
	s := res.Show[0]
	want := []string{" func Readback(ctx *Context) int {", "-\treturn ctx.n", "+\tfmt.Println(\"readback\")", "+\treturn ctx.n + 1", " }"}
	if s.Change.Symbol.Ref != "gpu.go:Readback" || s.Change.What != "changed" || !reflect.DeepEqual(s.Diff, want) {
		t.Errorf("--show gpu.go:Readback: %+v", s)
	}
	// a whole file: every line of its diff (the 23 old lines, and the 9 added)
	_, res, _ = recapJSON(t, "--show", "gpu.go")
	if len(res.Show) != 1 || res.Show[0].Change.Symbol.Kind != "file" || res.Show[0].Change.Symbol.Ref != "gpu.go" ||
		len(res.Show[0].Diff) != 23+9 || res.Show[0].Diff[0] != " package gpu" {
		t.Errorf("--show gpu.go: %+v", res.Show)
	}
	for _, s := range res.Show {
		for _, l := range s.Diff {
			if l == "" || !strings.ContainsRune(" -+", rune(l[0])) {
				t.Errorf("diff line %q doesn't start with ' ', '-' or '+'", l)
			}
		}
	}
}

// Moved and extracted code says where it came from, as refs.
func TestJSONMovesAndExtractions(t *testing.T) {
	write, commit := repo(t)
	write("sort.go", "package fsx\n\nfunc Compare(a, b string) int {\n\tla := fold(a)\n\tlb := fold(b)\n\tif la < lb {\n\t\treturn -1\n\t}\n\treturn 1\n}\n")
	write("cart.py", "class Cart:\n    pass\n\n\ndef apply_coupon(cart, code):\n    total = cart.total()\n    if code:\n        total *= 0.9\n    return total\n")
	write("api.ts", "export function fetchUser(id: number) {\n  return id;\n}\n\nexport function fetchTeam(id: number) {\n  return id;\n}\n\nexport class Client {\n  get() {}\n}\n")
	commit("first")
	write("sort.go", "package fsx\n\nfunc Compare(a, b string) int {\n\treturn compare(a, b)\n}\n\nfunc compare(a, b string) int {\n\tla := fold(a)\n\tlb := b\n\tif la < lb {\n\t\treturn -1\n\t}\n\treturn 1\n}\n")
	write("cart.py", "class Cart:\n    pass\n")
	write("discounts.py", "def apply_coupon(cart, code):\n    total = cart.total()\n    if code:\n        total *= 0.9\n    return total\n")
	os.Remove("api.ts")
	write("net/client.ts", "export function fetchUser(id: number, signal?: AbortSignal) {\n  return id + 0;\n}\n\nexport function fetchTeam(id: number) {\n  return id * 1;\n}\n\nexport class Client {\n  get() { return 1; }\n}\n")
	_, res, _ := recapJSON(t)
	ch := changesByRef(res.Files)
	if c := ch["sort.go:compare"]; c.What != "added" || c.From != "sort.go:Compare" || c.Note != "1 line differs" {
		t.Errorf("an extracted helper: %+v", c)
	}
	if c := ch["cart.py:apply_coupon"]; c.What != "removed" || c.MovedTo != "discounts.py" || c.Note != "unchanged" || !c.Public {
		t.Errorf("a function moved to another file: %+v", c)
	}
	if d := fileByPath(t, res.Files, "discounts.py"); d.Status != "untracked" || !reflect.DeepEqual(defRefs(d), []string{"discounts.py:apply_coupon"}) {
		t.Errorf("discounts.py: %+v", d)
	}
	if n := fileByPath(t, res.Files, "net/client.ts"); n.Status != "moved" || n.OldPath != "api.ts" || len(n.Changes) == 0 {
		t.Errorf("a file moved and edited: %+v", n)
	}
	// --show: the diff from where the body came from
	_, res, _ = recapJSON(t, "--show", "compare")
	if len(res.Show) != 1 || res.Show[0].Change.From != "sort.go:Compare" ||
		!contains(res.Show[0].Diff, "-\tlb := fold(b)") || !contains(res.Show[0].Diff, "+\tlb := b") {
		t.Errorf("--show compare: %+v", res.Show)
	}
	_, res, _ = recapJSON(t, "--show", "apply_coupon")
	if len(res.Show) != 1 || res.Show[0].Change.Symbol.Ref != "discounts.py:apply_coupon" || res.Show[0].Change.From != "cart.py:apply_coupon" ||
		len(res.Show[0].Diff) != 5 || res.Show[0].Diff[0] != " def apply_coupon(cart, code):" {
		t.Errorf("--show of a moved function, once, against its old copy: %+v", res.Show)
	}
	// committed, the moved file's signature change is in its changes
	commit("refactor")
	_, res, _ = recapJSON(t, "HEAD~1", "HEAD")
	if c := changesByRef(res.Files)["net/client.ts:fetchUser"]; c.OldSig != "export function fetchUser(id: number)" {
		t.Errorf("signature change in the moved file: %+v", c)
	}
}

func contains(ls []string, s string) bool {
	for _, l := range ls {
		if l == s {
			return true
		}
	}
	return false
}

func TestJSONShowCommitAndLog(t *testing.T) {
	write, commit := repo(t)
	if _, res, code := recapJSON(t, "log"); code != 0 || len(res.Commits) != 0 || res.Files == nil {
		t.Errorf("log without commits: exit %d, %+v", code, res)
	}
	if d, _, code := recapJSON(t, "show"); code != 2 || d.Error == nil || d.Error.Code != report.CodeFailed {
		t.Errorf("show without commits: exit %d, %+v", code, d.Error)
	}
	write("gpu.go", gpuV1)
	commit("first")
	write("gpu.go", gpuV2)
	commit("second\n\nWhy it changed.")
	id, parent := gitOut(t, "rev-parse", "HEAD"), gitOut(t, "rev-parse", "HEAD~1")
	when, err := time.Parse(time.RFC3339, gitOut(t, "log", "-1", "--format=%aI"))
	if err != nil {
		t.Fatal(err)
	}
	d, res, code := recapJSON(t, "show")
	if code != 0 || res.Commit == nil {
		t.Fatalf("exit %d, %+v", code, res)
	}
	c := res.Commit
	if c.Rev != id || c.Author != "Ada" || c.Subject != "second" || c.Body != "Why it changed." ||
		!reflect.DeepEqual(c.Parents, []string{parent}) || !c.Date.Equal(when) {
		t.Errorf("commit: %+v (want %s, parent %s, date %s)", c, id, parent, when)
	}
	if res.Old != id[:7]+"^" || res.New != id[:7] || changesByRef(res.Files)["gpu.go:UploadScene"].OldRef != "gpu.go:Upload" {
		t.Errorf("show: old %q, new %q, %+v", res.Old, res.New, res.Files)
	}
	if d.Repo.Head != id || d.Repo.Dirty {
		t.Errorf("repo: %+v", d.Repo)
	}
	// the date is RFC 3339 with the offset, as git has it
	var raw struct {
		Result struct {
			Commit struct{ Date string }
		}
	}
	if out, _ := recap(t, "show", "--json"); json.Unmarshal([]byte(out), &raw) != nil || !strings.HasPrefix(raw.Result.Commit.Date, when.Format("2006-01-02T15:04:05")) {
		t.Errorf("date %q, git says %s", raw.Result.Commit.Date, when)
	}

	_, res, code = recapJSON(t, "log", "-n", "5")
	if code != 0 || res.Files == nil || len(res.Files) != 0 || len(res.Commits) != 2 {
		t.Fatalf("log: exit %d, %+v", code, res)
	}
	second, first := res.Commits[0], res.Commits[1]
	if second.Commit.Rev != id || second.Commit.Subject != "second" || !second.Commit.Date.Equal(when) ||
		changesByRef(second.Files)["gpu.go:Dispatch"].What != "removed" {
		t.Errorf("log, newest first: %+v", second)
	}
	if first.Commit.Rev != parent || first.Commit.Parents != nil || len(first.Files) != 1 || first.Files[0].Status != "new" ||
		!reflect.DeepEqual(defRefs(first.Files[0]), []string{"gpu.go:Context", "gpu.go:CreateDevice", "gpu.go:Upload", "gpu.go:Dispatch", "gpu.go:Readback"}) {
		t.Errorf("log, the root commit: %+v", first)
	}
	// --show with @rev is recap show, commit included
	_, res, _ = recapJSON(t, "--show", "gpu.go:Readback@HEAD")
	if res.Commit == nil || res.Commit.Rev != id || len(res.Show) != 1 {
		t.Errorf("--show gpu.go:Readback@HEAD: %+v", res)
	}
}

func TestJSONErrors(t *testing.T) {
	refsRepo(t)
	fails := func(code string, args ...string) *report.Error {
		t.Helper()
		d, _, exit := recapJSON(t, args...)
		if exit != 2 || d.Error == nil || d.Error.Code != code {
			t.Errorf("recap --json %v: exit %d, want code %s, got %+v", args, exit, code, d.Error)
			return &report.Error{}
		}
		return d.Error
	}
	all := []string{"README.md:Contact @me", "internal/group/group.go:Group.Add", "internal/group/group.go:Helper", "internal/group/group.go:Group.Size",
		"legacy.go:Gone", "legacy.go:Other", "lib.cpp:parse#2", "lib.cpp:Foo::baz", "nest.py:outer.inner", "nest.py:fresh", "shape.rs:Circle.area"}
	if e := fails(report.CodeNoMatch, "HEAD~1", "HEAD", "--show", "Nope"); !reflect.DeepEqual(e.Candidates, all) ||
		!strings.HasPrefix(e.Message, `no changed symbol matches "Nope"; changed: `) {
		t.Errorf("--show Nope: %+v", e)
	}
	if e := fails(report.CodeNoMatch, "HEAD~1", "HEAD", "--show", "internal/group/group.go:18"); !reflect.DeepEqual(e.Candidates, all[1:4]) {
		t.Errorf("a location in an unchanged symbol: %+v", e)
	}
	if e := fails(report.CodeNoMatch, "HEAD~1", "HEAD", "--show", "nope.go:12"); !reflect.DeepEqual(e.Candidates, all) {
		t.Errorf("a file that didn't change: %+v", e)
	}
	if e := fails(report.CodeNoMatch, "HEAD~1", "HEAD", "--show", "lib.cpp:parse#1"); !reflect.DeepEqual(e.Candidates, all[6:8]) {
		t.Errorf("an unchanged duplicate: %+v", e)
	}
	if e := fails(report.CodeNoMatch, "--show", "Nope"); e.Candidates != nil {
		t.Errorf("nothing changed at all: %+v", e)
	}
	fails(report.CodeUsage, "HEAD~1", "HEAD", "--show", "internal/group/group.go@HEAD~1")
	fails(report.CodeUsage, "show", "HEAD~1", "--show", "parse@HEAD")
	fails(report.CodeUsage, "--bogus")
	fails(report.CodeUsage, "nosuchbranch")
	fails(report.CodeUsage, "log", "nosuchbranch")
	fails(report.CodeUsage, "--staged", "HEAD~1..HEAD")
	fails(report.CodeUsage, "-n", "3")
	if e := fails(report.CodeUsage, "-p"); !strings.Contains(e.Message, "--json") {
		t.Errorf("-p with --json: %+v", e)
	}
	// --json after the bad flag counts too
	if out, errs, code := recapAll(t, "--bogus", "--json"); code != 2 || errs != "" || !strings.Contains(out, `"code": "usage"`) {
		t.Errorf("--bogus --json: exit %d, stderr %q\n%s", code, errs, out)
	}
	// outside a repository: the directory stands in for it
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if d, _, code := recapJSON(t); code != 2 || d.Error == nil || d.Error.Code != report.CodeFailed || d.Repo.Git || d.Repo.Root == "" {
		t.Errorf("outside git: exit %d, %+v, %+v", code, d.Error, d.Repo)
	}
}

// More than eight matches is ambiguous; JSON lists every one.
func TestJSONAmbiguous(t *testing.T) {
	write, commit := repo(t)
	var v1, v2 strings.Builder
	for i := range 9 {
		fmt.Fprintf(&v1, "class C%d:\n    def run(self):\n        return %d\n\n\n", i, i)
		fmt.Fprintf(&v2, "class C%d:\n    def run(self):\n        return %d\n\n\n", i, i+1)
	}
	write("jobs.py", v1.String())
	commit("first")
	write("jobs.py", v2.String())
	d, _, code := recapJSON(t, "--show", "run")
	if code != 2 || d.Error == nil || d.Error.Code != report.CodeAmbiguous || len(d.Error.Candidates) != 9 || d.Error.Candidates[8] != "jobs.py:C8.run" {
		t.Errorf("exit %d, %+v", code, d.Error)
	}
	if _, errs, _ := recapAll(t, "--show", "run"); !strings.Contains(errs, "matches 9 symbols; be more specific, e.g. jobs.py:C0.run") || strings.Contains(errs, "C8.run") {
		t.Errorf("text: %s", errs)
	}
}

// The text budget doesn't apply to JSON unless -b is given.
func TestJSONLimits(t *testing.T) {
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
	d, res, _ := recapJSON(t)
	if b := fileByPath(t, res.Files, "big.py"); len(b.Changes) != 400 || d.Truncated != nil {
		t.Errorf("without -b: %d changes, truncated %+v", len(b.Changes), d.Truncated)
	}
	d, res, _ = recapJSON(t, "-b", "5")
	if b := fileByPath(t, res.Files, "big.py"); len(b.Changes) != 5 || len(fileByPath(t, res.Files, "small.py").Changes) != 1 ||
		d.Truncated == nil || *d.Truncated != (report.Truncated{Shown: 5, Total: 400}) {
		t.Errorf("-b 5: %d changes, truncated %+v", len(b.Changes), d.Truncated)
	}
}

func TestJSONStatuses(t *testing.T) {
	write, commit := repo(t)
	if _, res, _ := recapJSON(t); res.Old != "nothing" || res.New != "working tree" {
		t.Errorf("no commits yet: %+v", res)
	}
	long := "package m\n\n" + strings.Repeat("// a line that keeps the file similar\n", 20) + "func Keep() int {\n\treturn 1\n}\n"
	write("old.go", long)
	write("m.go", "package m\n\nfunc Tidy() int {\n\treturn 1\n}\n")
	write("logo.png", "\x89PNG\x00\x00\x01\n\x02\n")
	write("go.sum", "a v1 h1:x\n")
	commit("first")
	os.Remove("old.go")
	write("new.go", strings.Replace(long, "return 1", "return 2", 1))
	write("go.sum", "a v1 h1:y\n")
	commit("rename")
	_, res, _ := recapJSON(t, "show")
	if f := fileByPath(t, res.Files, "new.go"); f.Status != "renamed" || f.OldPath != "old.go" || changesByRef(res.Files)["new.go:Keep"].What != "changed" {
		t.Errorf("a renamed file: %+v", f)
	}
	if f := fileByPath(t, res.Files, "go.sum"); f.Generated != "lock file" {
		t.Errorf("a lock file: %+v", f)
	}
	_, res, _ = recapJSON(t, "show", "HEAD~1")
	if f := fileByPath(t, res.Files, "logo.png"); !f.Binary || f.Status != "new" {
		t.Errorf("a binary file: %+v", f)
	}
	// a merge conflict
	gitOut(t, "checkout", "-q", "-b", "other", "HEAD~1")
	write("m.go", "package m\n\nfunc Tidy() int {\n\treturn 2\n}\n")
	commit("two")
	gitOut(t, "checkout", "-q", "main")
	write("m.go", "package m\n\nfunc Tidy() int {\n\treturn 3\n}\n")
	commit("three")
	merge := exec.Command("git", "-c", "user.name=A", "-c", "user.email=a@b", "merge", "-q", "other")
	merge.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	merge.Run() // exits 1: the conflict
	if _, res, _ = recapJSON(t); fileByPath(t, res.Files, "m.go").Status != "conflict" {
		t.Errorf("a conflict: %+v", res.Files)
	}
}
