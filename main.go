// recap summarises a git diff by symbol: which functions, types and
// sections changed, were added, removed or renamed, instead of raw hunks.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/Vaspyyy/glint/outline"
	"github.com/Vaspyyy/recap/internal/analyze"
	"github.com/Vaspyyy/recap/internal/gitx"
	"github.com/Vaspyyy/recap/internal/render"
)

const version = "0.1.0"

const usage = `recap summarises a git diff by symbol: which functions, types and sections
changed, were added, removed or renamed.

usage: recap [flags] [<rev> | <rev> <rev> | <a>..<b> | <a>...<b>] [-- <path>...]
       recap log [-n N] [flags] [<revision range>] [-- <path>...]

  recap                    working tree (and untracked files) compared with HEAD
  recap --staged           what's staged for the next commit
  recap HEAD~3             the working tree compared with HEAD~3
  recap main...feature     what feature changed since it branched off main
  recap log -n 5           the last five commits, one summary each
  recap --show gpu.rs:readback    the real diff of one symbol

flags:
  -s, --staged          compare the index (staged changes) with HEAD
      --show TARGET     print the diff of one changed symbol: file:name, name, or a file
  -p, --patch           print the overview, then the full diff
  -n N                  recap log: how many commits (default 10)
  -b, --budget N        roughly how many lines the overview may use (default 150)
  -A, --agent           plain output without colors (default when not a terminal)
      --no-untracked    leave untracked files out
  -h, --help            show this help
      --version         show the version
`

type opts struct {
	staged, patch, agent, noUntracked bool
	show                              string
	budget, n                         int
	revs, paths                       []string
	log                               bool
}

func main() { os.Exit(run(os.Args[1:])) }

func fail(msg string) int {
	fmt.Fprintln(os.Stderr, "recap:", msg)
	return 2
}

func parse(args []string) (*opts, int, string) {
	o := &opts{budget: 150, n: 10}
	// "log" as the first argument that isn't a flag: recap -A log -n 3
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == "log" {
			o.log = true
			args = append(append([]string(nil), args[:i]...), args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		if a == "-b" || a == "--budget" || a == "-n" || a == "--max-count" || a == "--show" {
			break // their value comes next; keep it simple and don't look further
		}
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
			return nil, 0, usage
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
		case a == "--show":
			o.show, msg = value(&i, "--show")
		case strings.HasPrefix(a, "--show="):
			o.show = strings.TrimPrefix(a, "--show=")
		case a == "-b" || a == "--budget":
			if v, msg = value(&i, a); msg == "" {
				o.budget, msg = number(v, a)
			}
		case strings.HasPrefix(a, "--budget="):
			o.budget, msg = number(strings.TrimPrefix(a, "--budget="), "--budget")
		case a == "-n" || a == "--max-count":
			if v, msg = value(&i, a); msg == "" {
				o.n, msg = number(v, a)
			}
		case strings.HasPrefix(a, "-n") && len(a) > 2:
			o.n, msg = number(a[2:], "-n")
		case strings.HasPrefix(a, "-") && a != "-":
			msg = "unknown flag " + a + " (see recap --help)"
		default:
			o.revs = append(o.revs, a)
		}
		if msg != "" {
			return nil, 2, msg
		}
	}
	if o.show != "" && o.log {
		return nil, 2, "--show works on a diff, not on recap log"
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
	if o.log {
		return runLog(out, o, color)
	}

	comp, err := gitx.Compare(o.revs, o.paths, o.staged)
	if err != nil {
		return fail(err.Error())
	}
	files, contents, err := collect(top, comp, !o.noUntracked && comp.New.Worktree)
	if err != nil {
		return fail(err.Error())
	}
	if o.show != "" {
		return show(out, comp, files, contents, o.show, color)
	}
	head := "recap: " + comp.Describe() + ": "
	if len(files) == 0 {
		fmt.Fprintln(out, head+"no changes")
		return 0
	}
	fmt.Fprintln(out, bold(head, color)+render.Summary(files, color))
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
	blobs, err := gitx.OpenBlobs()
	if err != nil {
		return nil, nil, err
	}
	defer blobs.Close()
	contents := map[string]pair{}
	var files []*analyze.File
	for _, f := range changed {
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
		files = append(files, analyze.Analyze(f, old, new, false))
	}
	if untracked {
		paths, err := gitx.Untracked(comp.Paths)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range paths {
			data, err := os.ReadFile(top + "/" + p)
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
	return files, contents, nil
}

func runLog(out *bufio.Writer, o *opts, color bool) int {
	commits, err := gitx.Log(o.n, o.revs, o.paths)
	if err != nil {
		return fail(err.Error())
	}
	top, _ := gitx.Toplevel()
	budget := o.budget
	for k, cm := range commits {
		if k > 0 {
			fmt.Fprintln(out)
		}
		merge := ""
		if len(cm.Parents) > 1 {
			merge = " (merge: compared with its first parent)"
		}
		fmt.Fprintf(out, "%s  %s  %s  %s%s\n", bold(cm.Short, color), cm.Age, cm.Author, cm.Subject, merge)
		files, _, err := collect(top, gitx.CommitComparison(cm, o.paths), false)
		if err != nil {
			return fail(err.Error())
		}
		per := max(3, budget/max(1, len(commits))-2)
		fmt.Fprint(out, render.Compact(files, render.Options{Color: color, Budget: per, Indent: "  "}))
	}
	if len(commits) == 0 {
		fmt.Fprintln(out, "recap log: no commits")
	}
	return 0
}

var hunkHdrRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// show prints the diff of one changed symbol (or one file).
func show(out *bufio.Writer, comp *gitx.Comparison, files []*analyze.File, contents map[string]pair, target string, color bool) int {
	filepart, sym, hasSym := strings.Cut(target, ":")
	if !hasSym {
		filepart, sym = "", target
	}
	var cands []*analyze.File
	for _, f := range files {
		if fileMatches(f.Path, filepart) || (f.OldPath != "" && fileMatches(f.OldPath, filePart(filepart))) {
			cands = append(cands, f)
		}
	}
	// "recap --show path": the whole file
	if !hasSym {
		for _, f := range files {
			if f.Path == target || strings.HasSuffix(f.Path, "/"+target) {
				return showFile(out, comp, f, contents, color)
			}
		}
	}
	type hit struct {
		f  *analyze.File
		ch analyze.Change
	}
	var hits []hit
	for _, f := range cands {
		for _, ch := range f.Changes {
			if nameMatches(ch.Name, sym) || (ch.OldName != "" && nameMatches(ch.OldName, sym)) {
				hits = append(hits, hit{f, ch})
			}
		}
		if f.Status == 'A' || f.Untracked || f.Status == 'D' {
			data := contents[f.Path].new
			sign := "+"
			if f.Status == 'D' {
				data, sign = contents[f.Path].old, "-"
			}
			syms := outline.Parse(f.Path, data, 5000)
			for i, s := range syms {
				if nameMatches(outline.Qualified(syms, i), sym) {
					printLines(out, f.Path, data, s, sign, color)
					return 0
				}
			}
		}
	}
	switch {
	case len(hits) == 0:
		var names []string
		for _, f := range cands {
			for _, ch := range f.Changes {
				names = append(names, f.Path+":"+ch.Name)
			}
		}
		if len(names) == 0 {
			return fail("nothing changed matches " + strconv.Quote(target))
		}
		if len(names) > 12 {
			names = append(names[:12], fmt.Sprintf("… +%d more", len(names)-12))
		}
		return fail("no changed symbol matches " + strconv.Quote(target) + "; changed: " + strings.Join(names, ", "))
	case len(hits) > 1 && !allSameFile(len(hits), func(i int) string { return hits[i].f.Path }):
		var names []string
		for _, h := range hits {
			names = append(names, h.f.Path+":"+h.ch.Name)
		}
		return fail(strconv.Quote(target) + " matches several symbols; pick one: " + strings.Join(names, ", "))
	}
	for _, h := range hits {
		where := fmt.Sprintf("%s L%d-%d", h.f.Path, h.ch.Line, h.ch.End)
		switch h.ch.What {
		case "removed":
			where = fmt.Sprintf("%s, was L%d-%d", h.f.Path, h.ch.OldLine, h.ch.OldEnd)
		case "changed", "renamed", "moved":
			if h.ch.OldLine != h.ch.Line || h.ch.OldEnd != h.ch.End {
				where += fmt.Sprintf(", was L%d-%d", h.ch.OldLine, h.ch.OldEnd)
			}
		}
		fmt.Fprintf(out, "%s%s %s%s  (%s)\n", boldOn(color), h.ch.What, render.Label(h.ch.Kind, h.ch.Name), boldOff(color), where)
		for _, l := range symbolDiff(h.f, h.ch, contents[h.f.Path]) {
			fmt.Fprintln(out, colorize(l, color))
		}
	}
	return 0
}

func fileMatches(p, want string) bool {
	return want == "" || p == want || strings.HasSuffix(p, "/"+want)
}

func filePart(s string) string { return s }

func nameMatches(name, want string) bool {
	return name == want || strings.HasSuffix(name, "."+want)
}

func allSameFile(n int, path func(int) string) bool {
	for i := 1; i < n; i++ {
		if path(i) != path(0) {
			return false
		}
	}
	return true
}

func boldOn(c bool) string {
	if c {
		return "\x1b[1m"
	}
	return ""
}

func boldOff(c bool) string {
	if c {
		return "\x1b[0m"
	}
	return ""
}

// symbolDiff is the diff of one symbol: its changed lines with up to three
// unchanged lines around them, cut to the symbol's own lines.
func symbolDiff(f *analyze.File, ch analyze.Change, c pair) []string {
	oldLines := strings.Split(string(c.old), "\n")
	newLines := strings.Split(string(c.new), "\n")
	lineAt := func(ls []string, n int) string {
		if n >= 1 && n <= len(ls) {
			return ls[n-1]
		}
		return ""
	}
	switch ch.What {
	case "removed":
		var out []string
		for n := ch.OldLine; n <= ch.OldEnd; n++ {
			out = append(out, "-"+lineAt(oldLines, n))
		}
		return out
	case "added":
		var out []string
		for n := ch.Line; n <= ch.End; n++ {
			out = append(out, "+"+lineAt(newLines, n))
		}
		return out
	case "moved":
		return []string{fmt.Sprintf("  (unchanged; moved from L%d-%d to L%d-%d)", ch.OldLine, ch.OldEnd, ch.Line, ch.End)}
	}
	// walk the symbol's new lines; the -U0 hunks inside it say what changed
	var seq []string
	ctx := []bool{}
	cur := ch.Line
	emitContext := func(to int) {
		for ; cur <= to && cur <= ch.End; cur++ {
			seq = append(seq, " "+lineAt(newLines, cur))
			ctx = append(ctx, true)
		}
	}
	for _, h := range f.Hunks {
		oldTo := h.OldStart + max(h.OldLines, 1) - 1
		newFrom, newTo := h.NewStart, h.NewStart+h.NewLines-1
		if h.NewLines == 0 {
			newFrom, newTo = h.NewStart+1, h.NewStart // a deletion sits after line NewStart
		}
		inOld := overlaps(h.OldStart, oldTo, ch.OldLine, ch.OldEnd) && h.OldLines > 0
		inNew := h.NewLines > 0 && overlaps(newFrom, newTo, ch.Line, ch.End)
		if !inOld && !inNew {
			continue
		}
		if newFrom > ch.End+1 || newTo < ch.Line-1 {
			continue // a removal elsewhere (for renames: the old copy)
		}
		emitContext(newFrom - 1)
		for _, l := range h.Removed {
			seq = append(seq, "-"+l)
			ctx = append(ctx, false)
		}
		for _, l := range h.Added {
			seq = append(seq, "+"+l)
			ctx = append(ctx, false)
		}
		cur = max(cur, newTo+1)
	}
	emitContext(ch.End)
	// keep three lines of context around changes, mark what's left out
	var out []string
	for i := 0; i < len(seq); i++ {
		if !ctx[i] {
			out = append(out, seq[i])
			continue
		}
		near := false
		for d := -3; d <= 3; d++ {
			if j := i + d; j >= 0 && j < len(seq) && !ctx[j] {
				near = true
				break
			}
		}
		if i == 0 && ctx[0] {
			near = true // the symbol's first line names it
		}
		if near {
			out = append(out, seq[i])
		} else if len(out) == 0 || out[len(out)-1] != "  ⋮" {
			out = append(out, "  ⋮")
		}
	}
	return out
}

func overlaps(a, b, c, d int) bool { return c <= d && a <= d && c <= b }

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

func printLines(out *bufio.Writer, path string, data []byte, s outline.Symbol, sign string, color bool) {
	what := "added"
	if sign == "-" {
		what = "removed"
	}
	fmt.Fprintf(out, "%s%s %s%s  (%s, L%d-%d)\n", boldOn(color), what, render.Label(s.Kind, s.Name), boldOff(color), path, s.Line, s.End)
	lines := strings.Split(string(data), "\n")
	for i := s.Line - 1; i < s.End && i < len(lines); i++ {
		fmt.Fprintln(out, colorize(sign+lines[i], color))
	}
}

func showFile(out *bufio.Writer, comp *gitx.Comparison, f *analyze.File, contents map[string]pair, color bool) int {
	if f.Untracked {
		for _, l := range strings.Split(strings.TrimSuffix(string(contents[f.Path].new), "\n"), "\n") {
			fmt.Fprintln(out, colorize("+"+l, color))
		}
		return 0
	}
	paths := []string{f.Path}
	if f.OldPath != "" {
		paths = append(paths, f.OldPath)
	}
	patch, err := gitx.FullDiff(comp, 3, color, paths)
	if err != nil {
		return fail(err.Error())
	}
	out.Write(patch)
	return 0
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func count(s string) int {
	if s == "" {
		return 1
	}
	return atoi(s)
}
