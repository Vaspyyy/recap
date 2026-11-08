// recap summarises a git diff by symbol: which functions, types and
// sections changed, were added, removed, renamed or moved, instead of raw
// hunks.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/Vaspyyy/prism/ref"
	prismrepo "github.com/Vaspyyy/prism/repo" // "repo" is the tests' helper
	"github.com/Vaspyyy/prism/report"
	"github.com/Vaspyyy/prism/work"
	"github.com/Vaspyyy/recap/internal/analyze"
	"github.com/Vaspyyy/recap/internal/gitx"
	"github.com/Vaspyyy/recap/internal/render"
)

const version = "0.4.0"

type opts struct {
	staged, patch, agent, noUntracked bool
	json, noContext                   bool
	show                              string
	showSet                           bool
	budget, n                         int
	budgetSet, nSet                   bool
	revs, paths                       []string
	sub                               string // "", "log" or "show"
}

func main() { os.Exit(run(os.Args[1:])) }

// failure is why a run failed: printed to stderr, or with --json as the
// envelope's error.
type failure struct {
	code       string // report.CodeUsage, CodeNoMatch, CodeAmbiguous or CodeFailed
	msg        string
	candidates []string // refs a program can pick from instead (no_match, ambiguous)
}

func usage(msg string) *failure  { return &failure{code: report.CodeUsage, msg: msg} }
func failed(msg string) *failure { return &failure{code: report.CodeFailed, msg: msg} }

func noMatch(msg string, candidates []string) *failure {
	return &failure{code: report.CodeNoMatch, msg: msg, candidates: candidates}
}

// gitFailure is a failure for an error from gitx: a usage error when it's
// about the arguments (an unknown revision), else a failure.
func gitFailure(err error) *failure {
	var arg *gitx.ArgError
	if errors.As(err, &arg) {
		return usage(err.Error())
	}
	return failed(err.Error())
}

// jsonFlag reports whether args ask for --json, read before parsing so that
// a usage error is reported as JSON too. Like parse, it skips flags' values
// and stops at "--".
func jsonFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			return false
		case "-b", "--budget", "-n", "--max-count", "--show":
			i++
		case "--json":
			return true
		}
	}
	return false
}

func parse(args []string) (*opts, int, string) {
	o := &opts{budget: 150, n: 10}
	// a subcommand is the first argument that isn't a flag (or a flag's value)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "-b" || a == "--budget" || a == "-n" || a == "--max-count" || a == "--show" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		if a == "log" || a == "show" {
			o.sub = a
			args = append(append([]string(nil), args[:i]...), args[i+1:]...)
		}
		break
	}
	value := func(i *int, name string) (string, string) {
		if *i+1 >= len(args) {
			return "", name + " needs a value"
		}
		*i++
		return args[*i], ""
	}
	number := func(s, name string) (int, string) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, name + " wants a positive number, got " + strconv.Quote(s)
		}
		return n, ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var msg, v string
		switch {
		case a == "--":
			o.paths = append(o.paths, args[i+1:]...)
			i = len(args)
		case a == "-h" || a == "--help":
			return nil, 0, helpText(wantsColor(os.Stdout))
		case a == "--version":
			return nil, 0, "recap " + version + "\n"
		case a == "-s" || a == "--staged" || a == "--cached":
			o.staged = true
		case a == "-p" || a == "--patch":
			o.patch = true
		case a == "-A" || a == "--agent":
			o.agent = true
		case a == "--json":
			o.json = true
		case a == "--no-untracked":
			o.noUntracked = true
		case a == "--no-context":
			o.noContext = true
		case (a == "--show" || strings.HasPrefix(a, "--show=")) && o.showSet:
			msg = "--show takes one target; run it once per symbol"
		case a == "--show":
			o.show, msg = value(&i, "--show")
			o.showSet = true
		case strings.HasPrefix(a, "--show="):
			o.show, o.showSet = strings.TrimPrefix(a, "--show="), true
		case a == "-b" || a == "--budget":
			if v, msg = value(&i, a); msg == "" {
				o.budget, msg = number(v, a)
				o.budgetSet = true
			}
		case strings.HasPrefix(a, "--budget="):
			o.budget, msg = number(strings.TrimPrefix(a, "--budget="), "--budget")
			o.budgetSet = true
		case a == "-n" || a == "--max-count":
			if v, msg = value(&i, a); msg == "" {
				o.n, msg = number(v, a)
				o.nSet = true
			}
		case strings.HasPrefix(a, "-n") && len(a) > 2:
			o.n, msg = number(a[2:], "-n")
			o.nSet = true
		case strings.HasPrefix(a, "-") && a != "-":
			msg = "unknown flag " + a + " (see recap --help)"
		default:
			o.revs = append(o.revs, a)
		}
		if msg != "" {
			return nil, 2, msg
		}
	}
	switch {
	case o.nSet && o.sub != "log":
		return nil, 2, "-n only applies to recap log (for the last N commits: recap log -n N)"
	case o.sub == "log" && (o.staged || o.patch):
		return nil, 2, "recap log summarises commits; --staged and -p don't apply"
	case o.show != "" && o.sub == "log":
		return nil, 2, "--show works on a diff or one commit (recap show <commit> --show name), not on recap log"
	case o.sub == "show" && len(o.revs) > 1:
		return nil, 2, "recap show takes one commit"
	case o.sub == "show" && o.staged:
		return nil, 2, "recap show is about a commit; --staged doesn't apply"
	case o.showSet && strings.TrimSpace(o.show) == "":
		return nil, 2, "--show needs a target: file:name, file:line, name, or a file"
	case o.json && o.patch:
		return nil, 2, "-p prints git's patch as text, which --json can't hold; for one symbol's diff lines use --json --show TARGET"
	}
	return o, -1, ""
}

// cli is one run's output: text, or with --json one envelope.
type cli struct {
	args   []string
	json   bool
	out    *bufio.Writer // into body
	body   bytes.Buffer  // the output, until finish prints it
	detect func() (*prismrepo.Info, error)
	lim    limiter // --json: the lists' cap, and what it cut

	st   *work.State      // the run's context; nil before it's loaded
	cur  *gitx.Comparison // the comparison whose symbols are being marked
	next []report.Next    // the suggestions: next: lines, or the envelope's next
}

// finish prints the output. The text's context: line comes first, but it
// can only be written once the body has handed out its marks and notes;
// the next: lines come last.
func (c *cli) finish(stdout *bufio.Writer) {
	c.out.Flush()
	if !c.json && c.body.Len() > 0 {
		if line := c.st.Line(); line != "" {
			stdout.WriteString(line + "\n")
		}
	}
	stdout.Write(c.body.Bytes())
	if !c.json && len(c.next) > 0 {
		stdout.WriteString("\n")
		for _, l := range report.NextLines(c.next) {
			stdout.WriteString(l + "\n")
		}
	}
	stdout.Flush()
}

// fail reports a failure and returns the exit code.
func (c *cli) fail(f *failure) int {
	c.next = nil
	if !c.json {
		fmt.Fprintln(os.Stderr, "recap:", f.msg)
		return 2
	}
	e := c.envelope()
	l := limiter{max: c.lim.max} // the candidates are all the error shows
	e.Fail(f.code, f.msg, capped(&l, f.candidates)...)
	e.Truncated, e.Context = l.cut, c.st.Context()
	report.Write(c.out, e)
	return 2
}

// emit prints a result as the envelope.
func (c *cli) emit(res *report.Recap) int {
	e := c.envelope()
	e.Result, e.Truncated, e.Context = res, c.lim.cut, c.st.Context()
	if c.next != nil {
		e.Next = c.next
	}
	report.Write(c.out, e)
	return 0
}

func (c *cli) envelope() *report.Envelope {
	info, err := c.detect()
	if err != nil {
		cwd, _ := os.Getwd()
		info = &prismrepo.Info{Root: cwd} // paths must be absolutable anyway
	}
	return report.New("recap", version, c.args, info)
}

func run(args []string) int {
	o, code, msg := parse(args)
	if code == 0 {
		fmt.Print(msg)
		return 0
	}
	c := &cli{args: args, json: jsonFlag(args), lim: limiter{max: report.MaxItems}}
	c.out = bufio.NewWriter(&c.body)
	defer c.finish(bufio.NewWriter(os.Stdout))
	c.detect = sync.OnceValues(func() (*prismrepo.Info, error) { return prismrepo.Detect(".") })
	if code > 0 {
		return c.fail(usage(msg))
	}
	if c.json || !work.Disabled(o.noContext) {
		// the envelope names the repository, and context lives in its git
		// dir: find it alongside the comparison's git commands
		go c.detect()
	}
	if c.json && o.budgetSet { // the text budget applies to JSON only when given
		c.lim.max = min(o.budget, report.MaxItems)
	}
	top, err := gitx.Toplevel()
	if err != nil {
		return c.fail(failed("not inside a git repository"))
	}
	human := !o.agent && !c.json && term.IsTerminal(int(os.Stdout.Fd()))
	color := human && os.Getenv("NO_COLOR") == ""
	out := c.out
	if o.sub == "log" {
		c.load(o)
		return c.runLog(top, o, color)
	}
	var showRef ref.Ref
	if o.showSet {
		// --show needs the repository to read paths: find it alongside the
		// comparison's git commands, and wait for it only to tell whether
		// the text after an '@' is a revision
		go c.detect()
		showRef = ref.Parse(o.show, func(rev string) bool {
			info, err := c.detect()
			return err == nil && info.IsRev(rev)
		})
		if msg := atRev(o, showRef); msg != "" {
			return c.fail(usage(msg))
		}
	}
	c.load(o) // after atRev: "--show name@rev" is recap show rev

	var comp *gitx.Comparison
	var commit *gitx.Commit
	if o.sub == "show" {
		rev := "HEAD"
		if len(o.revs) == 1 {
			rev = o.revs[0]
		}
		if !gitx.HasCommits() {
			return c.fail(failed("no commits yet"))
		}
		cm, err := gitx.ShowCommit(rev)
		if err != nil {
			return c.fail(gitFailure(err))
		}
		commit = &cm
		comp = gitx.CommitComparison(cm, o.paths)
	} else {
		if comp, err = gitx.Compare(o.revs, o.paths, o.staged); err != nil {
			return c.fail(gitFailure(err))
		}
	}
	c.cur = comp
	files, contents, err := collect(top, comp, !o.noUntracked && comp.New.Worktree)
	if err != nil {
		return c.fail(failed(err.Error()))
	}
	if o.showSet {
		info, err := c.detect()
		if err != nil {
			return c.fail(failed(err.Error()))
		}
		return c.show(top, o, view{comp, commit, files, contents}, info, showRef, color)
	}
	var overview string // planned with the next: lines, which can only take room from detail
	if len(files) > 0 {
		c.next, overview = c.suggestions(top, o, commit, files, color, !c.json)
	}
	if c.json {
		res := c.recap(comp, commit)
		res.Files = c.files(files, files)
		if commit != nil && len(commit.Parents) > 1 {
			res.Notes = append(res.Notes, "a merge: compared with its first parent")
		}
		if comp.New.Worktree || comp.New.Index {
			if m := gitx.MergeHead(); m != "" {
				res.Notes = append(res.Notes, "merge in progress: merging "+m+"; resolve the conflicts, then commit (or git merge --abort)")
			}
		}
		c.setFocus(o, comp, commit, files)
		return c.emit(res)
	}

	if commit != nil {
		fmt.Fprintf(out, "%s  %s  %s\n", bold("commit "+commit.Short, color), commit.Author, commit.Date)
		if msg, err := gitx.Message(commit.ID); err == nil {
			for _, l := range strings.Split(msg, "\n") {
				fmt.Fprintln(out, "    "+l)
			}
		}
		if len(commit.Parents) > 1 {
			fmt.Fprintln(out, "    (a merge: compared with its first parent)")
		}
		fmt.Fprintln(out)
	}
	head := "recap: " + comp.Describe() + ": "
	if len(o.paths) > 0 {
		head = "recap: " + comp.Describe() + ", only " + strings.Join(o.paths, " ") + ": "
	}
	if len(files) == 0 {
		fmt.Fprintln(out, head+"no changes")
		c.setFocus(o, comp, commit, files)
		return 0
	}
	fmt.Fprintln(out, bold(head, color)+render.Summary(files, color))
	if comp.New.Worktree || comp.New.Index {
		if m := gitx.MergeHead(); m != "" {
			fmt.Fprintf(out, "merge in progress: merging %s; resolve the conflicts, then commit (or git merge --abort)\n", m)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, overview)
	if o.patch {
		patch, err := gitx.FullDiff(comp, 3, color, nil)
		if err != nil {
			return c.fail(failed(err.Error()))
		}
		fmt.Fprintln(out)
		out.Write(patch)
	}
	c.setFocus(o, comp, commit, files)
	return 0
}

// atRev reads "--show main.go:run@b681ef1" as "recap show b681ef1 --show
// main.go:run". It returns an error when the command names other revisions.
func atRev(o *opts, r ref.Ref) string {
	if r.Rev == "" {
		return ""
	}
	plain := r
	plain.Rev = ""
	switch {
	case o.sub == "show" && len(o.revs) == 1:
		a, errA := gitx.Resolve(o.revs[0])
		b, errB := gitx.Resolve(r.Rev)
		if errA == nil && errB == nil && a != b {
			return fmt.Sprintf("--show %s is about commit %s, but recap show was given %s; give one commit (recap show %s --show %s)",
				o.show, r.Rev, o.revs[0], r.Rev, render.ShellArg(plain.String()))
		}
	case o.sub == "show":
		o.revs = []string{r.Rev}
	case len(o.revs) > 0 || o.staged:
		other := strings.Join(o.revs, " ")
		if o.staged {
			other = strings.TrimSpace("--staged " + other)
		}
		return fmt.Sprintf("--show %s means recap show %s --show %s, which can't be combined with %s; drop one of them",
			o.show, r.Rev, render.ShellArg(plain.String()), other)
	default:
		o.sub, o.revs = "show", []string{r.Rev}
	}
	return ""
}

func bold(s string, color bool) string {
	if !color {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

type pair struct{ old, new []byte }

// collect reads and analyses every changed file (and untracked ones).
func collect(top string, comp *gitx.Comparison, untracked bool) ([]*analyze.File, map[string]pair, error) {
	changed, err := gitx.Files(comp)
	if err != nil {
		return nil, nil, err
	}
	var conflicts map[string]bool
	if comp.New.Worktree || comp.New.Index {
		conflicts = gitx.Conflicts(comp.Paths)
	}
	blobs, err := gitx.OpenBlobs()
	if err != nil {
		return nil, nil, err
	}
	defer blobs.Close()
	contents := map[string]pair{}
	var files []*analyze.File
	for _, f := range changed {
		if conflicts[f.Path] && comp.New.Index {
			// nothing of it is staged yet: the index holds the conflict's sides
			a := analyze.Analyze(&gitx.File{Status: f.Status, Path: f.Path}, nil, nil, false)
			a.Conflict = true
			files = append(files, a)
			continue
		}
		var old, new []byte
		oldPath := f.Path
		if f.OldPath != "" {
			oldPath = f.OldPath
		}
		if f.Status != 'A' && !comp.Old.Empty {
			if old, err = gitx.Content(blobs, top, comp.Old, oldPath); err != nil {
				return nil, nil, err
			}
		}
		if f.Status != 'D' {
			if new, err = gitx.Content(blobs, top, comp.New, f.Path); err != nil {
				return nil, nil, err
			}
		}
		contents[f.Path] = pair{old, new}
		a := analyze.Analyze(f, old, new, false)
		a.Conflict = conflicts[f.Path]
		files = append(files, a)
	}
	if untracked {
		paths, err := gitx.Untracked(comp.Paths)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range paths {
			data, err := os.ReadFile(filepath.Join(top, p))
			if err != nil {
				continue // a socket, a broken link, a file that went away
			}
			f := &gitx.File{Status: '?', Path: p, Added: bytes.Count(data, []byte{'\n'})}
			if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
				f.Binary, f.Added = true, 0
			}
			contents[p] = pair{nil, data}
			files = append(files, analyze.Analyze(f, nil, data, true))
		}
	}
	files = pairMoves(files, contents)
	analyze.Link(files)
	return files, contents, nil
}

// pairMoves finds deleted files that reappear as new files git didn't call
// a rename (moved and edited too much), by the definitions they share, and
// compares them as one file.
func pairMoves(files []*analyze.File, contents map[string]pair) []*analyze.File {
	used := map[*analyze.File]bool{}
	for _, d := range files {
		if d.Status != 'D' || len(d.Defs) < 2 {
			continue
		}
		var best *analyze.File
		bestN := 0
		for _, a := range files {
			if used[a] || !(a.Status == 'A' || a.Untracked) || len(a.Defs) < 2 || ext(a.Path) != ext(d.Path) {
				continue
			}
			if n := analyze.SharedDefs(d, a); n > bestN {
				best, bestN = a, n
			}
		}
		if best == nil || bestN*2 < min(len(d.Defs), len(best.Defs)) {
			continue
		}
		old, new := contents[d.Path].old, contents[best.Path].new
		hunks, added, deleted, err := gitx.DiffContents(old, new)
		if err != nil {
			continue
		}
		f := &gitx.File{Status: 'R', OldPath: d.Path, Path: best.Path, Similarity: -1, Added: added, Deleted: deleted, Hunks: hunks,
			OldMode: d.OldMode, NewMode: best.NewMode}
		merged := analyze.Analyze(f, old, new, false) // as a pair, not as a new file
		merged.Untracked = best.Untracked
		merged.Conflict = best.Conflict
		merged.PairedFrom, merged.PairedNew = d.File, best.File
		used[d], used[best] = true, true
		contents[best.Path] = pair{old, new}
		for i, x := range files {
			if x == best {
				files[i] = merged
			}
		}
	}
	var out []*analyze.File
	for _, f := range files {
		if !(used[f] && f.Status == 'D' && f.PairedFrom == nil) {
			out = append(out, f)
		}
	}
	return out
}

func ext(p string) string {
	if i := strings.LastIndexByte(p, '.'); i > strings.LastIndexByte(p, '/') {
		return p[i:]
	}
	return ""
}

func (c *cli) runLog(top string, o *opts, color bool) int {
	out := c.out
	if !gitx.HasCommits() && len(o.revs) == 0 {
		if c.json {
			return c.emit(&report.Recap{Files: []report.RecapFile{}, Commits: []report.RecapLog{}, Range: "HEAD"})
		}
		fmt.Fprintln(out, "recap log: no commits yet")
		return 0
	}
	commits, err := gitx.Log(o.n, o.revs, o.paths)
	if err != nil {
		return c.fail(gitFailure(err))
	}
	if c.json { // a log has no sides: old and new stay empty
		res := &report.Recap{Files: []report.RecapFile{}, Commits: []report.RecapLog{}, Range: "HEAD"}
		if len(o.revs) > 0 {
			res.Range = strings.Join(o.revs, " ")
		}
		for _, cm := range capped(&c.lim, commits) {
			c.cur = gitx.CommitComparison(cm, o.paths)
			files, _, err := collect(top, c.cur, false)
			if err != nil {
				return c.fail(failed(err.Error()))
			}
			res.Commits = append(res.Commits, report.RecapLog{Commit: commitOf(cm), Files: c.files(files, files)})
		}
		return c.emit(res)
	}
	for k, cm := range commits {
		if k > 0 {
			fmt.Fprintln(out)
		}
		merge := ""
		if len(cm.Parents) > 1 {
			merge = " (merge: compared with its first parent)"
		}
		fmt.Fprintf(out, "%s  %s  %s  %s%s\n", bold(cm.Short, color), cm.Date, cm.Author, cm.Subject, merge)
		if para, _, _ := strings.Cut(cm.Body, "\n\n"); para != "" { // the first paragraph says why
			ls := strings.Split(para, "\n")
			for k, l := range ls[:min(len(ls), 6)] {
				if k == 5 && len(ls) > 6 {
					l += fmt.Sprintf(" … (+%d lines: recap show %s)", len(ls)-6, cm.Short)
				}
				fmt.Fprintln(out, "    "+l)
			}
		}
		c.cur = gitx.CommitComparison(cm, o.paths)
		files, _, err := collect(top, c.cur, false)
		if err != nil {
			return c.fail(failed(err.Error()))
		}
		per := max(3, o.budget/max(1, len(commits))-2) // no detail to give up: a context: line goes on top
		fmt.Fprint(out, render.Compact(files, render.Options{Color: color, Budget: per, Indent: "  ", Marks: c.peek,
			Shown: func(m []string) { c.st.Show(m...) }}))
	}
	if len(commits) == 0 {
		fmt.Fprintln(out, "recap log: no commits")
	}
	return 0
}

// a symbol --show can print: a change, or a definition of a new or deleted file
type target struct {
	f    *analyze.File
	ch   analyze.Change
	name string // the name it's found by: the change's, or the old one of a rename
	ord  int    // the name's ordinal in its file ("#N"), 0 if it's unique there
	old  bool   // name, ord and line belong to the old version
	line int
}

// ref is the target's ref as recap prints it: path:name, with "#N" if needed.
func (t target) ref() string { return ref.Symbol(t.f.Path, t.name, t.ord).String() }

// candidates lists everything --show can print, by each of its names.
func candidates(files []*analyze.File) []target {
	var all []target
	for _, f := range files {
		for _, ch := range f.Changes {
			all = append(all, target{f, ch, ch.Name, ch.Ordinal, ch.What == "removed", ch.Line})
			if ch.OldName != "" {
				all = append(all, target{f, ch, ch.OldName, ch.OldOrdinal, true, ch.OldLine})
			}
		}
		for _, d := range f.Defs {
			ch := analyze.Change{What: "added", Kind: d.Kind, Name: d.Name, Ordinal: d.Ordinal, Line: d.Line, End: d.End, NewText: d.Text, FromFile: d.From}
			if f.Status == 'D' {
				ch = analyze.Change{What: "removed", Kind: d.Kind, Name: d.Name, Ordinal: d.Ordinal, Line: d.Line, End: d.End, OldLine: d.Line, OldEnd: d.End,
					OldText: d.Text, MovedTo: d.MovedTo}
			}
			if d.From != "" { // the other copy, for a real diff of the move
				for _, g := range files {
					for _, gc := range g.Changes {
						if g.Path == d.From && gc.What == "removed" && gc.Name == d.Name {
							ch.OldText, ch.OldLine, ch.OldEnd = gc.OldText, gc.OldLine, gc.OldEnd
						}
					}
					for _, gd := range g.Defs {
						if g.Path == d.From && gd.Name == d.Name && g.Status == 'D' {
							ch.OldText, ch.OldLine, ch.OldEnd = gd.Text, gd.Line, gd.End
						}
					}
				}
			}
			all = append(all, target{f, ch, d.Name, d.Ordinal, f.Status == 'D', d.Line})
		}
	}
	return all
}

// show prints the diff of the changed symbols a ref names, or of a file.
// When nothing in the working tree matches, it shows the match in the
// focus instead (fill).
func (c *cli) show(top string, o *opts, v view, info *prismrepo.Info, r ref.Ref, color bool) int {
	want := o.show
	sel, f := pick(v.comp, v.files, info, r, want)
	if f != nil && f.code == report.CodeNoMatch {
		if fv, fsel, ok := c.fill(top, o, info, r, want); ok {
			v, sel, f = fv, fsel, nil
			c.cur = v.comp
		}
	}
	if f != nil {
		return c.fail(f)
	}
	ts := sel.hits
	if sel.whole != nil {
		ts = newSide(sel.whole)
	}
	c.next = seekNext(top, significant(ts))
	if c.json {
		res := c.recap(v.comp, v.commit)
		if res.Show, res.Notes, f = c.shows(sel, v.files, v.contents); f != nil {
			return c.fail(f)
		}
		c.setFocus(o, v.comp, v.commit, v.files)
		return c.emit(res)
	}
	out := c.out
	if sel.note != "" {
		fmt.Fprintln(out, sel.note)
	}
	if sel.whole != nil {
		if code := c.showFiles(sel.whole, v.contents, want, color); code != 0 {
			return code
		}
		c.setFocus(o, v.comp, v.commit, v.files)
		return 0
	}
	if len(sel.hits) > 1 {
		fmt.Fprintf(out, "%d matches for %s\n\n", len(sel.hits), strconv.Quote(want))
	}
	for k, t := range sel.hits {
		if k > 0 {
			fmt.Fprintln(out)
		}
		printSymbol(out, t, c.suffix(t.ch.Refs(t.f.Path)...), color)
	}
	c.setFocus(o, v.comp, v.commit, v.files)
	return 0
}

// selection is what --show prints: changed symbols, or whole files.
type selection struct {
	hits  []target
	whole []*analyze.File // "recap --show path"
	note  string          // printed first: the path also names another changed file
}

// pick finds what a --show ref names, or says why nothing (or too much) matches.
func pick(comp *gitx.Comparison, files []*analyze.File, info *prismrepo.Info, r ref.Ref, want string) (selection, *failure) {
	cwd, _ := os.Getwd()
	matched, note := changedFiles(info, r.Path, cwd, files)
	if !r.HasTarget || r.Name == "" && !r.IsLocation() { // "recap --show path": the whole file
		if len(matched) > 0 {
			return selection{whole: matched, note: note}, nil
		}
		r = r.AsName() // a bare name: "Readback", "Repo.open"
	}
	scope, missed := files, ""
	switch {
	case !r.HasTarget || r.Path == "":
	case len(matched) > 0:
		scope = matched
	case r.IsLocation():
		all := candidates(files)
		return selection{}, noMatch(fileMiss(info, r.Path, cwd, comp)+changedList(all, "changed"), changedRefs(all))
	default: // a name with ':' in it (C++ "Foo::bar", Lua "obj:method"), or a file that didn't change
		missed, r = r.Path, r.AsName()
	}
	in := map[*analyze.File]bool{}
	for _, f := range scope {
		in[f] = true
	}
	var all []target
	for _, t := range candidates(files) {
		if in[t.f] {
			all = append(all, t)
		}
	}
	var hits []target
	if r.IsLocation() {
		var why string
		var near []string
		if hits, why, near = located(r, scope, all, comp); len(hits) == 0 {
			return selection{}, noMatch(why, near)
		}
	} else {
		hits = named(r, scope, all)
		if len(hits) == 0 && r.Ordinal == 0 { // a class or type name finds its changed members
			for _, t := range all {
				if strings.HasPrefix(t.name, r.Name+".") {
					hits = append(hits, t)
				}
			}
		}
	}
	// one entry per change (a rename can be found by both names), and a
	// move is one change: show it once, as the diff from the old copy
	var dedup []target
	seen, added := map[string]bool{}, map[string]bool{}
	for _, t := range hits {
		k := fmt.Sprintf("%p\x00%s\x00%s\x00%d", t.f, t.ch.What, t.ch.Name, t.ch.Line)
		if !seen[k] {
			seen[k] = true
			dedup = append(dedup, t)
			if t.ch.What == "added" {
				added[t.f.Path+"\x00"+t.ch.Name] = true
			}
		}
	}
	hits = hits[:0]
	for _, t := range dedup {
		if !(t.ch.What == "removed" && t.ch.MovedTo != "" && added[t.ch.MovedTo+"\x00"+t.ch.Name]) {
			hits = append(hits, t)
		}
	}
	switch {
	case len(hits) == 0:
		list := changedList(all, "changed")
		switch {
		case list == "":
			hint := ""
			if comp.New.Worktree {
				hint = " (--show looks at the working tree unless you give a commit or range: recap main...feature --show " + want + ")"
			}
			return selection{}, noMatch("nothing changed matches "+strconv.Quote(want)+" in "+comp.Describe()+hint, nil)
		case missed != "":
			return selection{}, noMatch(fileMiss(info, missed, cwd, comp)+list, changedRefs(all))
		}
		return selection{}, noMatch("no changed symbol matches "+strconv.Quote(want)+list, changedRefs(all))
	case len(hits) > 8:
		var names []string
		for _, t := range hits {
			names = append(names, t.ref())
		}
		return selection{}, &failure{code: report.CodeAmbiguous, candidates: names,
			msg: fmt.Sprintf("%q matches %d symbols; be more specific, e.g. %s", want, len(hits), strings.Join(names[:8], ", "))}
	}
	return selection{hits: hits, note: note}, nil
}

// changedFiles returns the files of the comparison p names, by their path
// or by the old path of a rename (prism/repo: relative to the root, to the
// working directory, absolute, or a unique suffix). The note says when p
// also names another changed file relative to the working directory.
func changedFiles(info *prismrepo.Info, p, cwd string, files []*analyze.File) ([]*analyze.File, string) {
	var paths []string
	has := map[string]bool{}
	for _, f := range files {
		for _, q := range []string{f.Path, f.OldPath} {
			if q != "" && !has[q] {
				has[q] = true
				paths = append(paths, q)
			}
		}
	}
	names := map[string]bool{}
	for _, n := range info.Match(p, cwd, paths) {
		names[n] = true
	}
	var out []*analyze.File
	for _, f := range files {
		if names[f.Path] || f.OldPath != "" && names[f.OldPath] {
			out = append(out, f)
		}
	}
	note := ""
	if rs := info.Readings(p, cwd); len(rs) > 1 && len(names) == 1 && names[rs[0]] && has[rs[1]] {
		note = fmt.Sprintf("note: %s is the file at the repository root (recap reads paths from there, as it prints them); %s, relative to here, changed too",
			render.Path(rs[0]), render.Path(rs[1]))
	}
	return out, note
}

// showFiles prints the diff of whole files.
func (c *cli) showFiles(files []*analyze.File, contents map[string]pair, want string, color bool) int {
	out := c.out
	if len(files) > 1 {
		fmt.Fprintf(out, "%d matches for %s\n\n", len(files), strconv.Quote(want))
	}
	for k, f := range files {
		if k > 0 {
			fmt.Fprintln(out)
		}
		if fl := showFile(out, f, contents, color); fl != nil {
			return c.fail(fl)
		}
	}
	return 0
}

// named returns the targets a name ref picks (prism/ref: the exact name,
// else a name ending in it at a '.', "#N", a type before its impls). It
// matches against the whole outlines of the files, so "#N" counts every
// definition by that name, changed or not, as the overview does.
func named(r ref.Ref, scope []*analyze.File, all []target) []target {
	type side struct {
		f   *analyze.File
		old bool
	}
	names := map[side]map[string]bool{}
	for _, t := range all {
		s := side{t.f, t.old}
		if names[s] == nil {
			names[s] = map[string]bool{}
		}
		names[s][t.name] = true
	}
	var ts []ref.Target
	var from []side
	for i, f := range scope {
		for _, old := range []bool{false, true} {
			s, syms, key := side{f, old}, f.NewSyms, strconv.Itoa(i)+" new"
			if old {
				syms, key = f.OldSyms, strconv.Itoa(i)+" old"
			}
			for _, t := range ref.Targets(key, syms) {
				if names[s][t.Name] { // all definitions by a changed name, for the ordinals
					ts = append(ts, t)
					from = append(from, s)
				}
			}
		}
	}
	var hits []target
	for _, k := range ref.Match(r, ts, nil) {
		for _, t := range all {
			if t.f == from[k].f && t.old == from[k].old && t.name == ts[k].Name && t.line == ts[k].Line {
				hits = append(hits, t)
			}
		}
	}
	return hits
}

// located returns the changed symbol containing a location ref's line (in
// the new version), or the changed symbols in its range. Without one, why
// says which symbol holds the line, that it didn't change, and what did
// (near: their refs).
func located(r ref.Ref, scope []*analyze.File, all []target, comp *gitx.Comparison) (hits []target, why string, near []string) {
	var whys []string
	for _, f := range scope {
		old := f.Status == 'D' // a deleted file only has its old lines
		syms := f.NewSyms
		if old {
			syms = f.OldSyms
		}
		ts := ref.Targets(f.Path, syms)
		find := func(k int) *target {
			for i, t := range all {
				if t.f == f && t.old == old && t.name == ts[k].Name && t.line == ts[k].Line {
					return &all[i]
				}
			}
			return nil
		}
		j := innermost(ts, f.Path, r.Line, r.End)
		found := false
		// a definition inside an added or renamed one isn't a change of its
		// own: the enclosing one is
		for k := j; k >= 0; k = syms[k].Parent {
			if t := find(k); t != nil {
				if k == j || t.ch.What == "added" || t.ch.What == "renamed" {
					hits, found = append(hits, *t), true
				}
				break
			}
		}
		if !found && r.End > 0 && j < 0 { // a range across definitions: the changed ones in it
			for _, t := range all {
				if t.f == f && t.old == old && t.name == t.ch.Name && t.line <= r.End && max(t.ch.End, t.line) >= r.Line {
					hits, found = append(hits, t), true
				}
			}
		}
		if found {
			continue
		}
		loc := r
		loc.Path, loc.Rev = f.Path, ""
		var w string
		switch {
		case j >= 0:
			w = fmt.Sprintf("%s is in %s (L%s), which didn't change in %s", loc, ref.Refs(ts)[j], render.Span(ts[j].Line, ts[j].End), comp.Describe())
		case r.Line > f.Lines:
			w = fmt.Sprintf("%s is past the end of %s (%s)", loc, render.Path(f.Path), render.Plural(f.Lines, "line"))
		case r.End > 0:
			w = fmt.Sprintf("%s holds no changed symbol", loc)
		default:
			w = fmt.Sprintf("%s isn't inside a symbol", loc)
		}
		var mine []target
		for _, t := range all {
			if t.f == f {
				mine = append(mine, t)
			}
		}
		if list := changedList(mine, "changed in "+render.Path(f.Path)); list != "" {
			w += list
			near = append(near, changedRefs(mine)...)
		} else {
			w += "; no symbol in it changed (recap --show " + render.ShellArg(f.Path) + " shows its diff)"
		}
		whys = append(whys, w)
	}
	return hits, strings.Join(whys, "; "), near
}

// innermost is the innermost target in path containing the line, or the
// whole range when end > 0; -1 if none.
func innermost(ts []ref.Target, path string, line, end int) int {
	if end == 0 {
		return ref.Innermost(ts, path, line)
	}
	best := -1
	for i, t := range ts {
		if t.Path != path || line < t.Line || end > max(t.End, t.Line) {
			continue
		}
		if best < 0 || max(t.End, t.Line)-t.Line < max(ts[best].End, ts[best].Line)-ts[best].Line {
			best = i
		}
	}
	return best
}

// changedRefs are the refs of the targets, by their current names.
func changedRefs(all []target) []string {
	var names []string
	for _, t := range all {
		if t.name == t.ch.Name {
			names = append(names, t.ref())
		}
	}
	return names
}

// changedList is "; changed: a.go:X, b.go:Y" for the targets (by their
// current names), or "" if there are none.
func changedList(all []target, label string) string {
	names := changedRefs(all)
	if len(names) == 0 {
		return ""
	}
	if len(names) > 12 {
		names = append(names[:12], fmt.Sprintf("… +%d more", len(names)-12))
	}
	return "; " + label + ": " + strings.Join(names, ", ")
}

// fileMiss says why a ref's path names no changed file.
func fileMiss(info *prismrepo.Info, p, cwd string, comp *gitx.Comparison) string {
	if res, err := info.Resolve(p, cwd); err == nil && res.Exists {
		return render.Path(res.Rel) + " didn't change in " + comp.Describe()
	}
	return "no changed file matches " + strconv.Quote(p) + " in " + comp.Describe()
}

// printSymbol prints one symbol's diff: against its old version, or against
// where its body came from (a move or an extraction). marks are printed
// after its name.
func printSymbol(out *bufio.Writer, t target, marks string, color bool) {
	ch := t.ch
	where := render.Path(t.f.Path) + " L" + render.Span(ch.Line, ch.End)
	against := ""
	switch {
	case ch.What == "removed" && ch.MovedTo != "":
		where = render.Path(t.f.Path) + ", was L" + render.Span(ch.Line, ch.End)
		against = "the diff to its new copy in " + render.Path(ch.MovedTo)
	case ch.What == "removed":
		where = render.Path(t.f.Path) + ", was L" + render.Span(ch.Line, ch.End)
	case ch.FromFile != "":
		against = "the diff from its old copy in " + render.Path(ch.FromFile) + " L" + render.Span(ch.OldLine, ch.OldEnd)
	case ch.From != "":
		against = "the diff from " + ch.From + " (L" + render.Span(ch.OldLine, ch.OldEnd) + " before), where its body came from"
	case ch.What != "added" && ch.OldLine > 0 && (ch.OldLine != ch.Line || ch.OldEnd != ch.End):
		where += ", was L" + render.Span(ch.OldLine, ch.OldEnd)
	}
	label := render.Label(ch.Kind, ch.RefName())
	if ch.What == "renamed" {
		label = render.Label(ch.Kind, ch.OldRefName()) + " → " + ch.RefName()
	}
	fmt.Fprintf(out, "%s%s %s%s%s  (%s)\n", onOff(color, true), ch.What, label, onOff(color, false), marks, where)
	if against != "" {
		fmt.Fprintln(out, "  "+against+":")
	}
	var lines []string
	switch {
	case ch.What == "moved":
		lines = []string{fmt.Sprintf("  (unchanged; it was at L%s)", render.Span(ch.OldLine, ch.OldEnd))}
	case ch.What == "added" && ch.OldText == nil:
		for _, l := range ch.NewText {
			lines = append(lines, "+"+l)
		}
	case ch.What == "removed" && ch.NewText == nil:
		for _, l := range ch.OldText {
			lines = append(lines, "-"+l)
		}
	default:
		ops := analyze.LineDiff(ch.OldText, ch.NewText)
		if ops == nil { // too large to compare here
			for _, l := range ch.OldText {
				lines = append(lines, "-"+l)
			}
			for _, l := range ch.NewText {
				lines = append(lines, "+"+l)
			}
		} else {
			lines = analyze.Context(ops, 3)
		}
	}
	printLines(out, lines, color)
}

const maxShowLines = 400

// printLines prints diff lines, clipping very long ones (minified code) and
// stopping after maxShowLines.
func printLines(out *bufio.Writer, lines []string, color bool) {
	for k, l := range lines {
		if k == maxShowLines {
			fmt.Fprintf(out, "… %d more lines (use git diff for all of it)\n", len(lines)-k)
			return
		}
		fmt.Fprintln(out, colorize(render.Clip(visibleCR(l), 400), color))
	}
}

// visibleCR shows a line's carriage return, so a CRLF change doesn't look
// like two identical lines.
func visibleCR(l string) string {
	if strings.HasSuffix(l, "\r") {
		return strings.TrimSuffix(l, "\r") + "␍"
	}
	return l
}

func onOff(color, on bool) string {
	switch {
	case !color:
		return ""
	case on:
		return "\x1b[1m"
	}
	return "\x1b[0m"
}

func colorize(l string, color bool) string {
	if !color {
		return l
	}
	switch {
	case strings.HasPrefix(l, "@@"):
		return "\x1b[36m" + l + "\x1b[0m"
	case strings.HasPrefix(l, "+"):
		return "\x1b[32m" + l + "\x1b[0m"
	case strings.HasPrefix(l, "-"):
		return "\x1b[31m" + l + "\x1b[0m"
	}
	return l
}

func showFile(out *bufio.Writer, f *analyze.File, contents map[string]pair, color bool) *failure {
	if f.LineEndings != "" {
		fmt.Fprintf(out, "%s: line endings only (%s), no other change\n", render.Path(f.Path), f.LineEndings)
		return nil
	}
	ops, fl := fileDiff(f, contents)
	if fl != nil {
		return fl
	}
	fmt.Fprintf(out, "%s%s%s  (%s)\n", onOff(color, true), render.Path(f.Path), onOff(color, false), strings.TrimPrefix(render.Summary([]*analyze.File{f}, false), "1 file changed, "))
	printLines(out, analyze.Context(ops, 3), color)
	return nil
}

// fileDiff is a whole file's line diff, for --show path.
func fileDiff(f *analyze.File, contents map[string]pair) ([]analyze.Op, *failure) {
	c := contents[f.Path]
	ops := analyze.LineDiff(splitKeep(c.old), splitKeep(c.new))
	if ops == nil || f.Binary {
		return nil, failed(f.Path + ": too large or binary for --show; use git diff -- " + f.Path)
	}
	return ops, nil
}

func splitKeep(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
