// recap summarises a git diff by symbol: which functions, types and
// sections changed, were added, removed, renamed or moved, instead of raw
// hunks.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/Vaspyyy/recap/internal/analyze"
	"github.com/Vaspyyy/recap/internal/gitx"
	"github.com/Vaspyyy/recap/internal/render"
)

const version = "0.1.0"

type opts struct {
	staged, patch, agent, noUntracked bool
	show                              string
	showSet                           bool
	budget, n                         int
	nSet                              bool
	revs, paths                       []string
	sub                               string // "", "log" or "show"
}

func main() { os.Exit(run(os.Args[1:])) }

func fail(msg string) int {
	fmt.Fprintln(os.Stderr, "recap:", msg)
	return 2
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
		case a == "--no-untracked":
			o.noUntracked = true
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
			}
		case strings.HasPrefix(a, "--budget="):
			o.budget, msg = number(strings.TrimPrefix(a, "--budget="), "--budget")
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
		return nil, 2, "--show needs a name: file:name, name, or a file"
	}
	return o, -1, ""
}

func run(args []string) int {
	o, code, msg := parse(args)
	if code == 0 {
		fmt.Print(msg)
		return 0
	}
	if code > 0 {
		return fail(msg)
	}
	top, err := gitx.Toplevel()
	if err != nil {
		return fail("not inside a git repository")
	}
	human := !o.agent && term.IsTerminal(int(os.Stdout.Fd()))
	color := human && os.Getenv("NO_COLOR") == ""
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	if o.sub == "log" {
		return runLog(out, top, o, color)
	}

	var comp *gitx.Comparison
	var commit *gitx.Commit
	if o.sub == "show" {
		rev := "HEAD"
		if len(o.revs) == 1 {
			rev = o.revs[0]
		}
		if !gitx.HasCommits() {
			return fail("no commits yet")
		}
		cm, err := gitx.ShowCommit(rev)
		if err != nil {
			return fail(err.Error())
		}
		commit = &cm
		comp = gitx.CommitComparison(cm, o.paths)
	} else {
		if comp, err = gitx.Compare(o.revs, o.paths, o.staged); err != nil {
			return fail(err.Error())
		}
	}
	files, contents, err := collect(top, comp, !o.noUntracked && comp.New.Worktree)
	if err != nil {
		return fail(err.Error())
	}
	if o.showSet {
		return show(out, comp, files, contents, o.show, color)
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
		return 0
	}
	fmt.Fprintln(out, bold(head, color)+render.Summary(files, color))
	if comp.New.Worktree || comp.New.Index {
		if m := gitx.MergeHead(); m != "" {
			fmt.Fprintf(out, "merge in progress: merging %s; resolve the conflicts, then commit (or git merge --abort)\n", m)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, render.Files(files, render.Options{Color: color, Budget: o.budget - 2}))
	if o.patch {
		patch, err := gitx.FullDiff(comp, 3, color, nil)
		if err != nil {
			return fail(err.Error())
		}
		fmt.Fprintln(out)
		out.Write(patch)
	}
	return 0
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

func runLog(out *bufio.Writer, top string, o *opts, color bool) int {
	if !gitx.HasCommits() && len(o.revs) == 0 {
		fmt.Fprintln(out, "recap log: no commits yet")
		return 0
	}
	commits, err := gitx.Log(o.n, o.revs, o.paths)
	if err != nil {
		return fail(err.Error())
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
		files, _, err := collect(top, gitx.CommitComparison(cm, o.paths), false)
		if err != nil {
			return fail(err.Error())
		}
		per := max(3, o.budget/max(1, len(commits))-2)
		fmt.Fprint(out, render.Compact(files, render.Options{Color: color, Budget: per, Indent: "  "}))
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
	name string
}

// show prints the diff of the changed symbols matching target, or of a file.
func show(out *bufio.Writer, comp *gitx.Comparison, files []*analyze.File, contents map[string]pair, want string, color bool) int {
	filePart, name, hasName := strings.Cut(want, ":")
	if !hasName {
		filePart, name = "", want
		for _, f := range files { // "recap --show path": the whole file
			if f.Path == fromCwd(want) || f.Path == want || strings.HasSuffix(f.Path, "/"+want) {
				return showFile(out, f, contents, color)
			}
		}
	}
	if filePart != "" {
		if p := fromCwd(filePart); p != filePart {
			for _, f := range files {
				if f.Path == p {
					filePart = p
				}
			}
		}
	}
	var all []target
	for _, f := range files {
		if !fileMatches(f.Path, filePart) && !(f.OldPath != "" && fileMatches(f.OldPath, filePart)) {
			continue
		}
		for _, ch := range f.Changes {
			all = append(all, target{f, ch, ch.Name})
			if ch.OldName != "" {
				all = append(all, target{f, ch, ch.OldName})
			}
		}
		for _, d := range f.Defs {
			ch := analyze.Change{What: "added", Kind: d.Kind, Name: d.Name, Line: d.Line, End: d.End, NewText: d.Text, FromFile: d.From}
			if f.Status == 'D' {
				ch = analyze.Change{What: "removed", Kind: d.Kind, Name: d.Name, Line: d.Line, End: d.End, OldLine: d.Line, OldEnd: d.End,
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
			all = append(all, target{f, ch, d.Name})
		}
	}
	// a name matches itself or a qualified name ending in it ("open" finds
	// Repo.open and open alike); a class or type name finds its changed members
	var hits []target
	seen := map[string]bool{}
	add := func(match func(string) bool) {
		for _, t := range all {
			k := t.f.Path + "\x00" + t.ch.What + "\x00" + t.ch.Name
			if !seen[k] && match(t.name) {
				hits = append(hits, t)
				seen[k] = true
			}
		}
	}
	add(func(n string) bool { return n == name || strings.HasSuffix(n, "."+name) })
	if len(hits) == 0 {
		add(func(n string) bool { return strings.HasPrefix(n, name+".") })
	}
	// a move is one change: show it once, as the diff from the old copy
	var dedup []target
	for _, t := range hits {
		if t.ch.What == "removed" && t.ch.MovedTo != "" && seen[t.ch.MovedTo+"\x00added\x00"+t.ch.Name] {
			continue
		}
		dedup = append(dedup, t)
	}
	hits = dedup
	switch {
	case len(hits) == 0:
		var names []string
		for _, t := range all {
			if t.name == t.ch.Name {
				names = append(names, t.f.Path+":"+t.ch.Name)
			}
		}
		if len(names) == 0 {
			hint := ""
			if comp.New.Worktree {
				hint = " (--show looks at the working tree unless you give a commit or range: recap main...feature --show " + want + ")"
			}
			return fail("nothing changed matches " + strconv.Quote(want) + " in " + comp.Describe() + hint)
		}
		if len(names) > 12 {
			names = append(names[:12], fmt.Sprintf("… +%d more", len(names)-12))
		}
		return fail("no changed symbol matches " + strconv.Quote(want) + "; changed: " + strings.Join(names, ", "))
	case len(hits) > 8:
		var names []string
		for _, t := range hits[:8] {
			names = append(names, t.f.Path+":"+t.ch.Name)
		}
		return fail(fmt.Sprintf("%q matches %d symbols; be more specific, e.g. %s", want, len(hits), strings.Join(names, ", ")))
	}
	if len(hits) > 1 {
		fmt.Fprintf(out, "%d matches for %s\n\n", len(hits), strconv.Quote(want))
	}
	for k, t := range hits {
		if k > 0 {
			fmt.Fprintln(out)
		}
		printSymbol(out, t, color)
	}
	return 0
}

// fromCwd turns a path relative to the working directory into one
// relative to the repository root, as git prints them.
func fromCwd(p string) string {
	top, err1 := gitx.Toplevel()
	wd, err2 := os.Getwd()
	if err1 != nil || err2 != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	rel, err := filepath.Rel(top, filepath.Join(wd, p))
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return filepath.ToSlash(rel)
}

func fileMatches(p, want string) bool {
	return want == "" || p == want || strings.HasSuffix(p, "/"+want)
}

// printSymbol prints one symbol's diff: against its old version, or against
// where its body came from (a move or an extraction).
func printSymbol(out *bufio.Writer, t target, color bool) {
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
	label := render.Label(ch.Kind, ch.Name)
	if ch.What == "renamed" {
		label = render.Label(ch.Kind, ch.OldName) + " → " + ch.Name
	}
	fmt.Fprintf(out, "%s%s %s%s  (%s)\n", onOff(color, true), ch.What, label, onOff(color, false), where)
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

func showFile(out *bufio.Writer, f *analyze.File, contents map[string]pair, color bool) int {
	c := contents[f.Path]
	if f.LineEndings != "" {
		fmt.Fprintf(out, "%s: line endings only (%s), no other change\n", render.Path(f.Path), f.LineEndings)
		return 0
	}
	ops := analyze.LineDiff(splitKeep(c.old), splitKeep(c.new))
	if ops == nil || f.Binary {
		return fail(f.Path + ": too large or binary for --show; use git diff -- " + f.Path)
	}
	fmt.Fprintf(out, "%s%s%s  (%s)\n", onOff(color, true), render.Path(f.Path), onOff(color, false), strings.TrimPrefix(render.Summary([]*analyze.File{f}, false), "1 file changed, "))
	printLines(out, analyze.Context(ops, 3), color)
	return 0
}

func splitKeep(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
