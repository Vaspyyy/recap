package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Vaspyyy/prism/focus"
	prismrepo "github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/prism/report"
)

// contextRepo is repo with context on, in an environment of its own: no
// user or system git config, a fixed author, and no agent session, so
// records are the worktree's shared ones (focus.json, last-run.json).
func contextRepo(t *testing.T) (write func(name, content string), commit func(msg string)) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ada", "GIT_AUTHOR_EMAIL": "ada@example.com", "GIT_COMMITTER_NAME": "Ada", "GIT_COMMITTER_EMAIL": "ada@example.com",
		"PRISM_SESSION": "", "CLAUDE_CODE_SESSION_ID": "", "T3CODE_THREAD_ID": "", "PRISM_NO_CONTEXT": "",
	} {
		t.Setenv(k, v)
	}
	return repo(t)
}

// store is the context store of the repository in the working directory.
func store(t *testing.T) *focus.Store {
	t.Helper()
	info, err := prismrepo.Detect(".")
	if err != nil {
		t.Fatal(err)
	}
	return focus.Open(info)
}

// clearFocus deletes the focus record, so a run can be checked for writing one.
func clearFocus(t *testing.T) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(gitOut(t, "rev-parse", "--absolute-git-dir"), "prism", "focus*.json"))
	for _, m := range matches {
		os.Remove(m)
	}
}

const filesV1 = `
package files

import "sort"

// List returns the names in roots, sorted.
// It skips empty names.
func List(roots []string) []string {
	var out []string
	for _, r := range roots {
		if r != "" {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

func run(args []string) int {
	return len(List(args))
}
`

const filesTest = `
package files

import "testing"

func TestList(t *testing.T) {
	if got := List([]string{"b", "", "a"}); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
`

// filesRepo is the fixture of Prism's v1 acceptance test (docs/roadmap.md):
// a Go module with List, its caller run and TestList, and a commit that
// changes List's body. It returns that commit's id.
func filesRepo(t *testing.T) string {
	t.Helper()
	write, commit := contextRepo(t)
	write("go.mod", "module example.com/fixture\n\ngo 1.22\n")
	write("internal/files/files.go", filesV1)
	write("internal/files/files_test.go", filesTest)
	commit("first")
	write("internal/files/files.go", strings.Replace(filesV1, `if r != "" {`, `if r != "" && r != "." {`, 1))
	commit("List skips dot")
	return gitOut(t, "rev-parse", "HEAD")
}

var symbolLineRe = regexp.MustCompile(`^  (changed|added|removed|renamed|moved) +\S+ (.+?)  (?:was )?L\d`)

// overviewRefs maps each symbol in an overview, as its file's path + ":" +
// its name as shown (marks left off), to what happened to it.
func overviewRefs(out string) map[string]string {
	refs := map[string]string{}
	var path string
	for _, l := range strings.Split(out, "\n") {
		if l != "" && l[0] != ' ' && !strings.HasPrefix(l, "recap:") && !strings.HasPrefix(l, "next:") && !strings.HasPrefix(l, "context:") {
			path, _, _ = strings.Cut(l, "  ")
		} else if m := symbolLineRe.FindStringSubmatch(l); m != nil {
			name, _, _ := strings.Cut(m[2], " [")
			refs[path+":"+name] = m[1]
		}
	}
	return refs
}

func nextLines(out string) []string {
	var next []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "next: ") {
			next = append(next, l)
		}
	}
	return next
}

// Step 1 of Prism's v1 acceptance test: recap show <commit> lists List as
// changed, suggests seek for it and records the focus that seek and crux
// mark.
func TestAcceptanceShowCommit(t *testing.T) {
	id := filesRepo(t)
	out, code := recap(t, "show", id)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if what := overviewRefs(out)["internal/files/files.go:List"]; what != "changed" {
		t.Errorf("internal/files/files.go:List isn't listed as changed (%q):\n%s", what, out)
	}
	if next := nextLines(out); !reflect.DeepEqual(next, []string{"next: seek internal/files/files.go:List   definition and uses by name"}) {
		t.Errorf("next: lines %q in\n%s", next, out)
	}
	if strings.HasPrefix(out, "context:") {
		t.Errorf("nothing in context changed the output, so there's no context: line:\n%s", out)
	}
	f := store(t).Focus()
	if f == nil {
		t.Fatal("recap show wrote no focus")
	}
	if !f.Has("internal/files/files.go:List") || f.Range != id[:7] || f.New != id || f.Old != gitOut(t, "rev-parse", id+"^") || f.By != "recap" || f.Total != 1 {
		t.Errorf("focus %+v", f)
	}
	// seek reads the working tree: once List is renamed there, it isn't suggested
	os.WriteFile("internal/files/files.go", []byte(strings.ReplaceAll(gitOut(t, "show", "HEAD:internal/files/files.go")+"\n", "List(", "ListAll(")), 0o644)
	if out, _ := recap(t, "show", id); len(nextLines(out)) != 0 || overviewRefs(out)["internal/files/files.go:List"] != "changed" {
		t.Errorf("List is gone from the working tree, so no seek line:\n%s", out)
	}
}

func TestFocusIsSetByCommitsOnly(t *testing.T) {
	write, commit := contextRepo(t)
	write("gpu.go", gpuV1)
	commit("first")
	write("gpu.go", gpuV2)
	commit("second")
	head, parent := gitOut(t, "rev-parse", "HEAD"), gitOut(t, "rev-parse", "HEAD~1")
	short := gitOut(t, "rev-parse", "--short", "HEAD")
	gpuSyms := []string{"gpu.go:CreateDevice", "gpu.go:Readback", "gpu.go:UploadScene", "gpu.go:Upscale"} // not the removed Dispatch

	for _, c := range []struct {
		args          []string
		rng, old, new string
		syms          []string
	}{
		{[]string{"show", "HEAD"}, short, parent, head, gpuSyms}, // HEAD moves on; the commit doesn't
		{[]string{"show"}, short, parent, head, gpuSyms},
		{[]string{"HEAD~1", "HEAD"}, "HEAD~1..HEAD", parent, head, gpuSyms},
		{[]string{"HEAD~1..HEAD"}, "HEAD~1..HEAD", parent, head, gpuSyms},
		{[]string{"HEAD~1...HEAD"}, "HEAD~1...HEAD", parent, head, gpuSyms}, // old: the merge base
		{[]string{"--show", "Readback@HEAD"}, short, parent, head, gpuSyms},
		{[]string{"--json", "show", "HEAD"}, short, parent, head, gpuSyms},
		{[]string{"show", "HEAD~1"}, parent[:7], gitOut(t, "hash-object", "-t", "tree", "/dev/null"), parent,
			[]string{"gpu.go:Context", "gpu.go:CreateDevice", "gpu.go:Upload", "gpu.go:Dispatch", "gpu.go:Readback"}}, // a new file: its definitions
	} {
		clearFocus(t)
		if out, code := recap(t, c.args...); code != 0 {
			t.Fatalf("recap %v: exit %d\n%s", c.args, code, out)
		}
		f := store(t).Focus()
		if f == nil {
			t.Errorf("recap %v wrote no focus", c.args)
			continue
		}
		got := slices.Clone(f.Symbols)
		slices.Sort(got)
		want := slices.Clone(c.syms)
		slices.Sort(want)
		if f.Range != c.rng || f.Old != c.old || f.New != c.new || !reflect.DeepEqual(got, want) || f.Total != len(want) {
			t.Errorf("recap %v: focus %+v, want range %q, %s..%s, symbols %q", c.args, f, c.rng, c.old, c.new, want)
		}
	}

	// the working tree, the index and recap log aren't commits on both sides
	write("gpu.go", gpuV1)
	write("staged.go", "package gpu\n\nfunc Staged() {}\n")
	gitOut(t, "add", "staged.go")
	for _, args := range [][]string{{}, {"--staged"}, {"HEAD~1"}, {"log"}, {"--show", "Readback"}, {"--json"}, {"show", "HEAD", "--no-context"}} {
		clearFocus(t)
		recap(t, args...)
		if f := store(t).Focus(); f != nil {
			t.Errorf("recap %v wrote a focus: %+v", args, f)
		}
	}
	// a failed run leaves the focus alone
	clearFocus(t)
	if _, code := recap(t, "show", "HEAD", "--show", "Nope"); code != 2 || store(t).Focus() != nil {
		t.Errorf("a failed recap show wrote a focus (exit %d)", code)
	}
}

func TestShowFillsFromFocus(t *testing.T) {
	id := filesRepo(t)
	short := id[:7]
	recap(t, "show", id)

	note := "context: nothing matches in the working tree; showing focus commit " + short + "\n"
	out, code := recap(t, "--show", "List")
	if code != 0 || !strings.HasPrefix(out, note+"changed func List  (internal/files/files.go L7-16)\n") || !strings.Contains(out, `+		if r != "" && r != "." {`) {
		t.Errorf("--show List on a clean working tree: exit %d\n%s", code, out)
	}
	if next := nextLines(out); len(next) != 1 || next[0] != "next: seek internal/files/files.go:List   definition and uses by name" {
		t.Errorf("next: lines %q", next)
	}
	// the note names the commit: repeating the run with it gives the same diff without context
	if again, _ := recap(t, "show", short, "--show", "List", "--no-context"); again != strings.TrimPrefix(out, note) {
		t.Errorf("recap show %s --show List isn't what the fill-in showed:\n%s\nvs\n%s", short, again, out)
	}
	// a file that didn't change in the working tree
	if out, code := recap(t, "--show", "internal/files/files.go"); code != 0 || !strings.HasPrefix(out, note+"internal/files/files.go  (+1 −1)\n") {
		t.Errorf("--show internal/files/files.go: exit %d\n%s", code, out)
	}
	// something else changed in the working tree, List didn't: still filled
	write := func(name, content string) { os.WriteFile(name, []byte(strings.TrimPrefix(content, "\n")), 0o644) }
	write("internal/files/files_test.go", strings.Replace(filesTest, `"b", "", "a"`, `"b", "", "a", "."`, 1))
	if out, code := recap(t, "--show", "List"); code != 0 || !strings.HasPrefix(out, note) {
		t.Errorf("--show List with another change in the working tree: exit %d\n%s", code, out)
	}
	// List changed in the working tree: that's what's shown
	write("internal/files/files.go", strings.Replace(gitOut(t, "show", "HEAD:internal/files/files.go")+"\n", "sort.Strings(out)", "sort.Sort(sort.Reverse(sort.StringSlice(out)))", 1))
	if out, code := recap(t, "--show", "List"); code != 0 || strings.Contains(out, "nothing matches") ||
		!strings.Contains(out, "\nchanged func List [focus]  (internal/files/files.go L7-16)\n") || !strings.Contains(out, "+\tsort.Sort(") {
		t.Errorf("--show List with List changed in the working tree: exit %d\n%s", code, out)
	}
	gitOut(t, "checkout", "-q", ".")

	// explicit wins: a revision, --staged or a location is never filled, and
	// neither is anything with --no-context
	for _, args := range [][]string{
		{"HEAD", "--show", "List"},
		{"--staged", "--show", "List"},
		{"--show", "internal/files/files.go:12"},
		{"--show", "List", "--no-context"},
	} {
		if out, errs, code := recapAll(t, args...); code != 2 || out != "" || !strings.Contains(errs, "List") && !strings.Contains(errs, "files.go") {
			t.Errorf("recap %v: exit %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
	// nothing in the focus matches either: the working tree's miss stands
	if _, errs, code := recapAll(t, "--show", "Nope"); code != 2 || !strings.HasPrefix(errs, `recap: nothing changed matches "Nope" in HEAD → working tree`) {
		t.Errorf("--show Nope: exit %d, %s", code, errs)
	}

	d, res, code := recapJSON(t, "--show", "List")
	if code != 0 || d.Context == nil || !slices.Contains(d.Context.Applied, "fill") ||
		!reflect.DeepEqual(d.Context.Notes, []string{"nothing matches in the working tree; showing focus commit " + short}) ||
		res.New != short || res.Commit == nil || res.Commit.Rev != id || len(res.Show) != 1 || res.Show[0].Change.Symbol.Ref != "internal/files/files.go:List" {
		t.Errorf("--json --show List: exit %d, context %+v, result %+v", code, d.Context, res)
	}
}

// A fill-in from a range names the range and the commits it resolved to:
// HEAD may have moved on since, and the run must be repeatable from the
// note alone (Prism's docs/context.md, rule 4).
func TestShowFillsFromFocusRange(t *testing.T) {
	filesRepo(t)
	recap(t, "HEAD~1..HEAD")
	old, new := gitOut(t, "rev-parse", "--short", "HEAD~1"), gitOut(t, "rev-parse", "--short", "HEAD")
	os.WriteFile("notes.txt", []byte("later\n"), 0o644) // HEAD moves on; the branch, and so the focus, stay
	gitOut(t, "add", "notes.txt")
	gitOut(t, "-c", "user.name=Ada", "-c", "user.email=ada@example.com", "commit", "-q", "-m", "later")

	out, code := recap(t, "--show", "List")
	note := "context: nothing matches in the working tree; showing focus HEAD~1..HEAD (" + old + ".." + new + ")\n"
	if code != 0 || !strings.HasPrefix(out, note+"changed func List") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if again, _ := recap(t, old+".."+new, "--show", "List", "--no-context"); again != strings.TrimPrefix(out, note) {
		t.Errorf("recap %s..%s --show List isn't what the fill-in showed:\n%s\nvs\n%s", old, new, again, out)
	}
	if _, errs, code := recapAll(t, "HEAD~1..HEAD", "--show", "List", "--no-context"); code != 2 || !strings.Contains(errs, "nothing changed matches") {
		t.Errorf("HEAD~1..HEAD has moved on and doesn't hold List's change any more: exit %d, %s", code, errs)
	}
}

func TestMarks(t *testing.T) {
	id := filesRepo(t)
	if err := store(t).SetLastRun("crux", focus.LastRun{Cmd: "go test ./...", Exit: 1,
		Tests: []string{"internal/files/files_test.go:TestList"}, In: []string{"internal/files/files.go:List"}}); err != nil {
		t.Fatal(err)
	}
	out, _ := recap(t, "show", id)
	if !strings.HasPrefix(out, "context: last run: crux go test ./... failed (just now)\ncommit ") ||
		!strings.Contains(out, "  changed  func List [failing]  L7-16  +1 −1\n") {
		t.Errorf("recap show with a failing last run:\n%s", out)
	}
	_, res, _ := recapJSON(t, "show", id)
	if m := changesByRef(res.Files)["internal/files/files.go:List"].Symbol.Marks; !reflect.DeepEqual(m, []string{"failing"}) {
		t.Errorf("JSON marks %q", m)
	}
	if out, _ := recap(t, "--show", "List@"+id); !strings.HasPrefix(out, "context: last run: crux go test ./... failed (just now)\nchanged func List [failing]  (") {
		t.Errorf("--show List@%s:\n%s", id, out)
	}

	// the focus's own output doesn't mark [focus] (every symbol there is in
	// it); other output does
	if out, _ := recap(t, "--show", "List"); !strings.Contains(out, "changed func List [failing]  (") {
		t.Errorf("the fill-in shows the focus itself:\n%s", out)
	}
	os.WriteFile("internal/files/files.go", []byte(strings.Replace(gitOut(t, "show", "HEAD:internal/files/files.go")+"\n", "return out", "return out[:len(out):len(out)]", 1)), 0o644)
	out, _ = recap(t)
	if !strings.HasPrefix(out, "context: focus: commit "+id[:7]+" (recap, just now); last run: crux go test ./... failed (just now)\n") ||
		!strings.Contains(out, "  changed  func List [focus] [failing]  L") {
		t.Errorf("recap with List changed again:\n%s", out)
	}
	gitOut(t, "checkout", "-q", ".")
	out, _ = recap(t, "log")
	own, first, _ := strings.Cut(out, "\n\n")
	if !strings.Contains(own, "changed List [failing]") || !strings.Contains(first, "func List [focus] [failing], func run") ||
		!strings.Contains(first, "func TestList [failing]") {
		t.Errorf("recap log: the focus commit's own entry has no [focus]; the first commit's List does:\n%s", out)
	}
}

// --no-context gives the same output minus marks and the context: line;
// the next: lines come from the result and stay.
func TestNoContextIsTheSameMinusContext(t *testing.T) {
	id := filesRepo(t)
	recap(t, "show", id) // a focus
	store(t).SetLastRun("crux", focus.LastRun{Cmd: "go test ./...", Exit: 1, In: []string{"internal/files/files.go:List", "internal/files/files.go:run"}})
	os.WriteFile("internal/files/files.go", []byte(strings.Replace(gitOut(t, "show", "HEAD:internal/files/files.go")+"\n", "return len(List(args))", "return len(List(args)) + 0", 1)), 0o644)
	for _, args := range [][]string{{"show", id}, {}, {"log"}, {"HEAD~1"}, {"--show", "run"}, {"--show", "List@" + id}} {
		sameMinusContext(t, args...)
	}
}

var marksRe = regexp.MustCompile(` \[(changed|branch|focus|failing)\]`)

// sameMinusContext checks that a run that used context prints what it
// prints with --no-context, plus a context: line and marks.
func sameMinusContext(t *testing.T, args ...string) {
	t.Helper()
	on, code := recap(t, args...)
	off, codeOff := recap(t, append(args, "--no-context")...)
	if !strings.HasPrefix(on, "context: ") || !marksRe.MatchString(on) {
		t.Errorf("recap %v: no context used, nothing to compare:\n%s", args, on)
	}
	_, rest, _ := strings.Cut(on, "\n")
	if code != codeOff || marksRe.ReplaceAllString(rest, "") != off {
		t.Errorf("recap %v: with context minus the context: line and marks:\n%s\n--no-context:\n%s", args, marksRe.ReplaceAllString(rest, ""), off)
	}
	if d, _, _ := recapJSON(t, append(args, "--no-context")...); d.Context != nil {
		t.Errorf("recap --json %v --no-context: context %+v, want null", args, d.Context)
	}
}

// Context only marks: a marked overload stays merged with its twin (else
// the split pushes another name past the cap), and a name cut into "+N
// more" gets no mark nobody can see, so no context: line names its layer.
func TestMarksDontChangeTheLists(t *testing.T) {
	write, commit := contextRepo(t)
	write("README", "x\n")
	commit("first")
	var cpp, g strings.Builder
	cpp.WriteString("int add(int a, int b) {\n  return a + b;\n}\ndouble add(double a, double b) {\n  return a + b;\n}\n")
	g.WriteString("package a\n\n")
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&cpp, "int f%d() {\n  return %d;\n}\n", i, i)
	}
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&g, "func F%02d() int {\n\treturn %d\n}\n\n", i, i)
	}
	write("a.cpp", cpp.String())
	write("a.go", g.String())
	commit("second")
	failing := func(refs ...string) {
		t.Helper()
		if err := store(t).SetLastRun("crux", focus.LastRun{Cmd: "go test ./...", Exit: 1, In: refs}); err != nil {
			t.Fatal(err)
		}
	}

	failing("a.cpp:add#2")
	if out, _ := recap(t, "show"); !strings.Contains(out, "\na.cpp  new, 27 lines: func add [failing] ×2, func f1, func f2, func f3, func f4, func f5, func f6, func f7\n") {
		t.Errorf("recap show: a marked overload must stay merged, and f7 listed:\n%s", out)
	}
	if out, _ := recap(t, "log", "-n", "1"); !strings.Contains(out, "\n  a.cpp  new, 27 lines: func add [failing] ×2, func f1, func f2, func f3, func f4, func f5, +2 more\n") {
		t.Errorf("recap log: a marked overload must stay merged, and f5 listed:\n%s", out)
	}
	sameMinusContext(t, "show")
	sameMinusContext(t, "log", "-n", "1")

	failing("a.go:F10") // a.go's line names F01 to F08, then "+2 more"
	for _, args := range [][]string{{"show"}, {"log", "-n", "1"}} {
		if out, _ := recap(t, args...); strings.HasPrefix(out, "context:") || strings.Contains(out, "[failing]") {
			t.Errorf("recap %v: F10 isn't printed, so nothing says it failed:\n%s", args, out)
		}
	}
	// JSON lists every definition, F10 with its mark
	d, res, _ := recapJSON(t, "show")
	if m := fileByPath(t, res.Files, "a.go").Defs[9].Symbol; m.Ref != "a.go:F10" || !reflect.DeepEqual(m.Marks, []string{"failing"}) || !slices.Contains(d.Context.Applied, "mark") {
		t.Errorf("JSON: %+v, context %+v", m, d.Context)
	}
}

// Members of a symbol added to an existing file are in the focus with it:
// seek and crux name a location by the innermost symbol containing it.
func TestFocusHasMembers(t *testing.T) {
	write, commit := contextRepo(t)
	shop := "def checkout():\n    return 1\n"
	write("shop.py", shop)
	commit("first")
	cart := shop + "\n\nclass Cart:\n    def total(self):\n        return 2\n\n    def tax(self):\n        return 3\n"
	write("shop.py", cart)
	commit("cart")
	recap(t, "show")
	if f := store(t).Focus(); f == nil || !reflect.DeepEqual(f.Symbols, []string{"shop.py:Cart", "shop.py:Cart.total", "shop.py:Cart.tax"}) || f.Total != 3 {
		t.Errorf("added class: focus %+v", f)
	}
	// renamed with it
	write("shop.py", strings.Replace(cart, "class Cart:", "class Basket:", 1))
	commit("rename")
	out, _ := recap(t, "show")
	if f := store(t).Focus(); f == nil || !reflect.DeepEqual(f.Symbols, []string{"shop.py:Basket", "shop.py:Basket.total", "shop.py:Basket.tax"}) {
		t.Errorf("renamed class: focus %+v", f)
	}
	// the overview says all of a rename that changed nothing else: no --show line
	if next := nextLines(out); !reflect.DeepEqual(next, []string{"next: seek shop.py:Basket   definition and uses by name"}) {
		t.Errorf("next: %q in\n%s", next, out)
	}
}

// Context and next: lines may cost detail (changed lines shown inline),
// never a listed change (Prism's docs/output.md, rule 5). The counts are
// what recap 0.3.0 listed, before context.
func TestContextCostsOnlyDetail(t *testing.T) {
	write, commit := contextRepo(t)
	var v1 strings.Builder
	v1.WriteString("package a\n\n")
	for i := range 20 {
		fmt.Fprintf(&v1, "func F%02d(x int) int {\n\treturn x + %d\n}\n\n", i, i)
	}
	write("a.go", v1.String())
	commit("first")
	store(t).SetLastRun("crux", focus.LastRun{Cmd: "go test ./...", Exit: 1, In: []string{"a.go:F00", "a.go:F01"}})
	count := func(out, what string) int {
		n := 0
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "  "+what+" ") {
				n++
			}
		}
		return n
	}

	// only removals: no detail to give up, so the context: line goes on top
	write("a.go", "package a\n")
	on, _ := recap(t, "-b", "13")
	off, _ := recap(t, "-b", "13", "--no-context")
	if !strings.HasPrefix(on, "context: ") || count(on, "removed") != 10 || count(off, "removed") != 10 {
		t.Errorf("-b 13: %d removals listed with context, %d without, want 10:\n%s", count(on, "removed"), count(off, "removed"), on)
	}

	// small changes: the next: lines and the context: line take the room
	// of their changed lines, and then the overview hasn't shown F00's
	// change, so --show is suggested
	write("a.go", strings.ReplaceAll(v1.String(), "return x + ", "return x - "))
	diff := report.Next{Cmd: "recap --show a.go:F00", Why: "its diff"}
	seek := report.Next{Cmd: "seek a.go:F00", Why: "definition and uses by name"}
	for _, args := range [][]string{{"-b", "16"}, {"-b", "16", "--no-context"}} {
		out, _ := recap(t, args...)
		if count(out, "changed") != 13 || strings.Contains(out, "           - return") || !reflect.DeepEqual(nextLines(out), report.NextLines([]report.Next{seek, diff})) {
			t.Errorf("recap %v: %d changes listed, want 13, and next: seek and --show:\n%s", args, count(out, "changed"), out)
		}
	}
	// with room, every change shows its lines: no --show
	if out, _ := recap(t); count(out, "changed") != 20 || !strings.Contains(out, "+ return x - 19") ||
		!reflect.DeepEqual(nextLines(out), report.NextLines([]report.Next{seek})) {
		t.Errorf("recap: all 20 changes with their lines, next: seek only:\n%s", out)
	}
	// a mark on a change cut by the budget isn't printed, nor named
	store(t).SetLastRun("crux", focus.LastRun{Cmd: "go test ./...", Exit: 1, In: []string{"a.go:F19"}})
	if out, _ := recap(t, "-b", "16"); strings.HasPrefix(out, "context:") || strings.Contains(out, "[failing]") {
		t.Errorf("F19 isn't listed at -b 16, so nothing says it failed:\n%s", out)
	}
}

func TestNextLines(t *testing.T) {
	write, commit := contextRepo(t)
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("lib.go", gpuV1)
	write("lib_test.go", "package gpu\n\nimport \"testing\"\n\nfunc TestUpload(t *testing.T) {\n\tUpload(nil, nil)\n}\n")
	write("README.md", "# lib\n\n## Use\n\nit\n")
	commit("first")
	// a signature change beats a bigger body change; tests and docs come last
	v2 := strings.Replace(strings.Replace(gpuV1, "ctx.n *= 2", "ctx.n *= 2\n\tctx.n += 3\n\tctx.n -= 1\n\tctx.n *= 5", 1),
		"func Readback(ctx *Context) int {", "func Readback(ctx *Context, scale int) int {", 1)
	write("lib.go", v2)
	write("lib_test.go", "package gpu\n\nimport \"testing\"\n\nfunc TestUpload(t *testing.T) {\n\tUpload(nil, nil)\n\tUpload(nil, nil)\n\tUpload(nil, nil)\n\tUpload(nil, nil)\n}\n")
	write("README.md", "# lib\n\n## Use\n\nit\nmore\nand more\n")
	commit("second")
	next := func(args ...string) []string {
		t.Helper()
		out, _ := recap(t, args...)
		return nextLines(out)
	}
	seekLine := func(r string) string { return "next: seek " + r + "   definition and uses by name" }
	// was: and now: show all of Readback's change, and Upload's lines are inline: no --show
	if got := next("show"); !reflect.DeepEqual(got, []string{seekLine("lib.go:Readback")}) {
		t.Errorf("next: lines %q", got)
	}
	// JSON has the same suggestions, with the same commands
	if d, _, _ := recapJSON(t, "show"); len(d.Next) != 1 || d.Next[0].Cmd != "seek lib.go:Readback" || d.Next[0].Why != "definition and uses by name" {
		t.Errorf("JSON next %+v", d.Next)
	}

	// seek reads the working tree: a symbol that's gone there isn't suggested, the next one is
	write("lib.go", strings.Replace(v2, "func Readback(", "func ReadbackAll(", 1))
	if got := next("show"); !reflect.DeepEqual(got, []string{seekLine("lib.go:Upload")}) {
		t.Errorf("Readback is gone from the working tree; next: %q", got)
	}
	os.Remove("lib.go")
	if got := next("show"); !reflect.DeepEqual(got, []string{seekLine("lib_test.go:TestUpload")}) {
		t.Errorf("lib.go is gone; next: %q", got)
	}
	gitOut(t, "checkout", "-q", ".")

	// a change the overview can't show inline gets a --show line, with the
	// revisions as given
	write("lib.go", strings.Replace(v2, "ctx.n *= 5", "ctx.n *= 5\n\tctx.n /= 2\n\tctx.n %= 7\n\tctx.n += 9\n\tctx.n -= 4\n\tctx.n |= 1", 1))
	commit("third")
	short := gitOut(t, "rev-parse", "--short", "HEAD")
	seek := report.Next{Cmd: "seek lib.go:Upload", Why: "definition and uses by name"}
	diff := func(cmd string) report.Next { return report.Next{Cmd: cmd, Why: "its diff"} }
	for _, c := range []struct {
		args []string
		want []report.Next
	}{
		{[]string{"show"}, []report.Next{seek, diff("recap show " + short + " --show lib.go:Upload")}},
		{[]string{"show", "HEAD"}, []report.Next{seek, diff("recap show HEAD --show lib.go:Upload")}},
		{[]string{"HEAD~1", "HEAD"}, []report.Next{seek, diff("recap 'HEAD~1' HEAD --show lib.go:Upload")}},
		{[]string{"HEAD~1"}, []report.Next{seek, diff("recap 'HEAD~1' --show lib.go:Upload")}},
		// the shown symbol's seek line only: --show is what's printed
		{[]string{"show", "--show", "Upload"}, []report.Next{seek}},
	} {
		if got := next(c.args...); !reflect.DeepEqual(got, report.NextLines(c.want)) {
			t.Errorf("recap %v: next: %q, want %q", c.args, got, report.NextLines(c.want))
		}
		if d, _, _ := recapJSON(t, c.args...); !reflect.DeepEqual(d.Next, c.want) {
			t.Errorf("recap --json %v: next %+v, want %+v", c.args, d.Next, c.want)
		}
	}
	write("lib.go", strings.Replace(v2, "ctx.n *= 5", "ctx.n *= 5\n\tctx.n /= 2\n\tctx.n %= 7\n\tctx.n += 9\n\tctx.n -= 4\n\tctx.n |= 1\n\tctx.n &= 3", 1))
	gitOut(t, "add", "lib.go")
	if got := next("--staged"); !reflect.DeepEqual(got, []string{seekLine("lib.go:Upload")}) {
		t.Errorf("--staged: next: %q", got) // one line more is inline again
	}
	gitOut(t, "reset", "-q", "--hard")

	// a removed symbol is never suggested
	write("lib.go", strings.Replace(gitOut(t, "show", "HEAD:lib.go")+"\n", "func Dispatch(ctx *Context) {\n\tctx.n++\n}\n", "", 1))
	if out, _ := recap(t); len(nextLines(out)) != 0 || !strings.Contains(out, "removed  func Dispatch") {
		t.Errorf("only a removal: no next: lines\n%s", out)
	}
}
