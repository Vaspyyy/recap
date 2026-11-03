// Package render prints recap's overview, plain for agents or in color.
package render

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Vaspyyy/recap/internal/analyze"
)

type pal struct{ red, green, yellow, blue, cyan, bold, faint, reset string }

func palette(on bool) pal {
	if !on {
		return pal{}
	}
	return pal{"\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[34m", "\x1b[36m", "\x1b[1m", "\x1b[2m", "\x1b[0m"}
}

type Options struct {
	Color  bool
	Budget int // lines, roughly
	Indent string
}

type writer struct {
	b     strings.Builder
	c     pal
	lines int
}

func (w *writer) line(format string, a ...any) {
	fmt.Fprintf(&w.b, format+"\n", a...)
	w.lines++
}

// Counts is "+12 −3".
func Counts(plus, minus int, c pal) string {
	return c.green + "+" + strconv.Itoa(plus) + c.reset + " " + c.red + "−" + strconv.Itoa(minus) + c.reset
}

// Path quotes a path the way git does when it holds control characters or
// quotes, so one file stays one line.
func Path(p string) string {
	if strings.IndexFunc(p, func(r rune) bool { return unicode.IsControl(r) || r == '"' || r == '\\' }) >= 0 {
		return strconv.Quote(p)
	}
	return p
}

// shellArg quotes a word for pasting into a shell.
func shellArg(s string) string {
	if strings.IndexFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./:=@%+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Summary is the totals line, with git's own numbers: "3 files changed, +120 −45".
func Summary(files []*analyze.File, color bool) string {
	c := palette(color)
	n, plus, minus, untracked, conflicts := 0, 0, 0, 0, 0
	for _, f := range files {
		if f.PairedFrom != nil { // git's view: a deleted file and a new one
			n++
			minus += f.PairedFrom.Deleted
			if f.Untracked {
				untracked++
			} else {
				n++
				plus += f.PairedNew.Added
			}
			continue
		}
		if f.Untracked {
			untracked++
			continue // their lines aren't part of the diff
		}
		n++
		plus += f.Added
		minus += f.Deleted
		if f.Conflict {
			conflicts++
		}
	}
	s := plural(n, "file") + " changed, " + Counts(plus, minus, c)
	if untracked > 0 {
		s += fmt.Sprintf(", %d untracked", untracked)
	}
	if conflicts > 0 {
		s += c.red + fmt.Sprintf(", %s with merge conflicts", plural(conflicts, "file")) + c.reset
	}
	return s
}

// importance orders a file's changes for the budget: what changes callers
// first, trivial edits last.
func importance(ch analyze.Change) int {
	switch {
	case ch.OldSig != "":
		return 0
	case ch.What == "renamed", ch.What == "removed":
		return 1
	case ch.What == "added":
		return 2
	case ch.What == "moved":
		return 3
	case ch.Note == "whitespace only":
		return 5
	}
	return 4
}

// Files prints the overview within the budget: every file gets its line,
// then each file a few of its most important changes, then the rest in
// file order.
func Files(files []*analyze.File, o Options) string {
	w := &writer{c: palette(o.Color)}
	room := max(o.Budget, 10)
	shownFiles := min(len(files), max(room, 30))
	room -= shownFiles
	ordered := make([][]analyze.Change, shownFiles)
	need := make([]int, shownFiles)
	for i, f := range files[:shownFiles] {
		ordered[i] = append([]analyze.Change(nil), f.Changes...)
		sort.SliceStable(ordered[i], func(x, y int) bool { return importance(ordered[i][x]) < importance(ordered[i][y]) })
		for _, ch := range ordered[i] {
			need[i] += changeLines(ch)
		}
		if f.TopReal && len(f.Changes) > 0 {
			need[i]++
		}
	}
	quota := make([]int, shownFiles)
	for _, step := range []int{3, 1 << 30} { // a few lines for every file first
		for i := range quota {
			give := min(need[i]-quota[i], step-min(step, quota[i]), max(room, 0))
			if give > 0 {
				quota[i] += give
				room -= give
			}
		}
	}
	// what's left goes to the changed lines of small changes, most important first
	details := map[string]bool{}
	for rank := 0; rank <= 5 && room > 0; rank++ {
		for i := range ordered {
			if quota[i] < need[i] {
				continue // this file's list is already cut: no room for details
			}
			for _, ch := range ordered[i] {
				if importance(ch) == rank && len(ch.Detail) > 0 && len(ch.Detail) <= room {
					details[detailKey(files[i], ch)] = true
					room -= len(ch.Detail)
				}
			}
		}
	}
	for i, f := range files[:shownFiles] {
		w.file(f, ordered[i], quota[i], o.Indent, details)
	}
	if n := len(files) - shownFiles; n > 0 {
		var names []string
		for _, f := range files[shownFiles:min(len(files), shownFiles+8)] {
			names = append(names, Path(f.Path))
		}
		more := ""
		if n > len(names) {
			more = fmt.Sprintf(" +%d more", n-len(names))
		}
		w.line("%s… and %s not shown: %s%s (raise -b to see them)", o.Indent, plural(n, "more file"), strings.Join(names, ", "), more)
	}
	return w.b.String()
}

func changeLines(ch analyze.Change) int {
	if ch.OldSig != "" {
		return 3
	}
	return 1
}

// status is how a file's line starts: "modified, +3 −1".
func status(f *analyze.File, c pal, compact bool) string {
	var s string
	switch {
	case f.Conflict:
		s = c.red + "merge conflict (unresolved)" + c.reset
	case f.PairedFrom != nil:
		s = c.yellow + "moved from " + Path(f.OldPath) + c.reset
		switch {
		case f.Untracked:
			s += " (the new copy is untracked; paired by their definitions)"
		case compact:
			s += " (paired by its definitions)"
		default:
			s += " (git saw a delete and a new file; paired by their definitions)"
		}
	case f.Untracked:
		s = c.green + "untracked" + c.reset
	case f.Status == 'A':
		s = c.green + "new" + c.reset
	case f.Status == 'D':
		s = c.red + "deleted" + c.reset
	case f.Status == 'R':
		s = c.yellow + fmt.Sprintf("renamed from %s (%d%% same)", Path(f.OldPath), f.Similarity) + c.reset
	case f.Status == 'C':
		s = c.yellow + fmt.Sprintf("copied from %s (%d%% same)", Path(f.OldPath), f.Similarity) + c.reset
	case f.Status == 'T':
		s = c.yellow + "type changed (file, symlink or submodule)" + c.reset
	case f.Status == 'U':
		s = c.red + "unmerged (conflict)" + c.reset
	default:
		s = c.yellow + "modified" + c.reset
	}
	var tail []string
	switch {
	case f.Conflict && f.Hunks == nil && !f.Untracked:
	case f.Submodule:
		tail = append(tail, "submodule")
	case f.Binary:
		tail = append(tail, "binary")
	case f.Status == 'A' || f.Status == 'D' || f.Untracked && f.PairedFrom == nil:
		tail = append(tail, plural(f.Lines, "line"))
	default:
		tail = append(tail, Counts(f.Added, f.Deleted, c))
	}
	if f.Generated != "" {
		tail = append(tail, f.Generated)
	}
	if f.LineEndings != "" {
		tail = append(tail, "line endings only ("+f.LineEndings+")")
	}
	if f.NewMode == "120000" || f.OldMode == "120000" {
		tail = append(tail, "symlink")
	}
	if f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode && f.OldMode != "000000" && f.NewMode != "000000" && f.Status != 'T' && f.PairedFrom == nil {
		tail = append(tail, "mode "+f.OldMode+" → "+f.NewMode)
	}
	if f.Conflict && f.Hunks == nil && !f.Untracked {
		tail = append(tail, "nothing staged")
	}
	if len(tail) == 0 {
		return s
	}
	return s + ", " + strings.Join(tail, ", ")
}

func detailKey(f *analyze.File, ch analyze.Change) string {
	return f.Path + "\x00" + ch.What + "\x00" + ch.Name
}

func (w *writer) file(f *analyze.File, changes []analyze.Change, quota int, ind string, details map[string]bool) {
	c := w.c
	line := ind + c.bold + Path(f.Path) + c.reset + "  " + status(f, c, false)
	if syms := f.Symbols(); len(syms) > 0 {
		extra := ""
		if len(syms) > 8 {
			syms, extra = syms[:8], fmt.Sprintf(" +%d more", len(syms)-8)
		}
		line += ": " + c.faint + strings.Join(syms, ", ") + extra + c.reset
	}
	if len(f.Changes) == 0 && len(f.TopLines) > 0 && !f.Untracked && f.Status != 'A' && f.Status != 'D' {
		line += ": " + c.faint + strings.Join(clipAll(f.TopLines, 80), "  ") + c.reset
	}
	w.line("%s", line)

	used, shown := 0, 0
	for _, ch := range changes {
		if used+changeLines(ch) > quota {
			break
		}
		w.change(ch, ind, details[detailKey(f, ch)])
		used += changeLines(ch)
		shown++
	}
	topShown := false
	if f.TopReal && len(f.Changes) > 0 && used < quota {
		what := "lines outside definitions"
		if f.TopImports {
			what = "imports"
		}
		detail := ""
		if f.TopBlank > 0 {
			detail = c.faint + fmt.Sprintf(" (%d blank)", f.TopBlank) + c.reset
		}
		if len(f.TopLines) > 0 {
			detail += ": " + c.faint + strings.Join(clipAll(f.TopLines, 80), "  ") + c.reset
		}
		w.line("%s  %s%-8s%s %s  %s%s", ind, c.faint, "also", c.reset, what, Counts(f.TopPlus, f.TopMinus, c), detail)
		topShown = true
	}
	if rest := changes[shown:]; len(rest) > 0 {
		w.line("%s  %s… +%s (recap --show %s:<name>, or raise -b)%s", ind, c.faint, summarize(rest), shellArg(f.Path), c.reset)
	} else if !topShown && f.TopReal && len(f.Changes) > 0 {
		w.line("%s  %s… and lines outside definitions %s%s", ind, c.faint, Counts(f.TopPlus, f.TopMinus, pal{}), c.reset)
	}
}

// summarize counts left-out changes by kind: "2846 changed (each +1 −1), 10 removed".
func summarize(rest []analyze.Change) string {
	count := map[string]int{}
	var order []string
	same := map[string]string{}
	for _, ch := range rest {
		k := ch.What
		if count[k] == 0 {
			order = append(order, k)
			same[k] = fmt.Sprintf("+%d −%d", ch.Plus, ch.Minus)
		} else if same[k] != fmt.Sprintf("+%d −%d", ch.Plus, ch.Minus) {
			same[k] = ""
		}
		count[k]++
	}
	var parts []string
	for _, k := range order {
		p := fmt.Sprintf("%d %s", count[k], k)
		if count[k] > 1 && same[k] != "" && (k == "changed" || k == "renamed") {
			p += " (each " + same[k] + ")"
		}
		parts = append(parts, p)
	}
	return plural(len(rest), "more change") + ": " + strings.Join(parts, ", ")
}

func (w *writer) change(ch analyze.Change, ind string, withDetail bool) {
	c := w.c
	color := map[string]string{"changed": c.yellow, "added": c.green, "removed": c.red, "renamed": c.blue, "moved": c.blue}[ch.What]
	name := Label(ch.Kind, ch.Name)
	if ch.What == "renamed" {
		name = Label(ch.Kind, ch.OldName) + " → " + ch.Name
	}
	where := "L" + span(ch.Line, ch.End)
	if ch.What == "removed" {
		where = "was L" + span(ch.Line, ch.End)
	}
	var extra []string
	if ch.Plus+ch.Minus > 0 && ch.What != "added" && ch.What != "removed" {
		extra = append(extra, Counts(ch.Plus, ch.Minus, c))
	}
	var notes []string
	if ch.Public && (ch.What == "removed" || ch.What == "renamed" || ch.What == "added" || ch.OldSig != "") {
		notes = append(notes, "public")
	}
	if ch.Members > 0 {
		notes = append(notes, fmt.Sprintf("with %d inside", ch.Members))
	}
	differs := func() string {
		if ch.Differs == 0 {
			return "unchanged"
		}
		return analyzeLines(ch.Differs)
	}
	switch {
	case ch.FromFile != "":
		notes = append(notes, "moved from "+Path(ch.FromFile)+", "+differs())
	case ch.MovedTo != "":
		notes = append(notes, "moved to "+Path(ch.MovedTo)+", "+differs())
	case ch.What == "added" && ch.From != "" && ch.Differs == 0:
		notes = append(notes, "body taken from "+ch.From+", identical")
	case ch.What == "added" && ch.From != "":
		notes = append(notes, c.yellow+"body taken from "+ch.From+", "+analyzeLines(ch.Differs)+c.reset)
	}
	if ch.Note != "" {
		notes = append(notes, ch.Note)
	}
	tail := ""
	if len(extra) > 0 {
		tail += "  " + strings.Join(extra, " ")
	}
	if len(notes) > 0 {
		tail += c.faint + "  (" + strings.Join(notes, "; ") + ")" + c.reset
	}
	w.line("%s  %s%-8s%s %s  %s%s%s%s", ind, color, ch.What, c.reset, c.bold+name+c.reset, c.cyan, where, c.reset, tail)
	if ch.OldSig != "" {
		w.line("%s           %swas: %s%s", ind, c.faint, Clip(ch.OldSig, 200), c.reset)
		w.line("%s           %snow: %s%s", ind, c.faint, Clip(ch.NewSig, 200), c.reset)
	}
	if withDetail {
		for _, l := range ch.Detail {
			col := c.green
			if l[0] == '-' {
				col = c.red
			}
			w.line("%s           %s%s %s%s", ind, col, l[:1], Clip(strings.TrimSpace(l[1:]), 150), c.reset)
		}
	}
}

func analyzeLines(n int) string {
	if n == 1 {
		return "1 line differs"
	}
	return strconv.Itoa(n) + " lines differ"
}

// Compact prints one line per file: what happened to which symbols.
func Compact(files []*analyze.File, o Options) string {
	w := &writer{c: palette(o.Color)}
	shown := min(len(files), max(o.Budget, 1))
	keep := map[*analyze.File]bool{}
	ranked := append([]*analyze.File(nil), files...)
	sort.SliceStable(ranked, func(i, j int) bool { return Rank(ranked[i]) < Rank(ranked[j]) })
	for _, f := range ranked[:shown] {
		keep[f] = true
	}
	var hidden []*analyze.File
	for _, f := range files {
		if keep[f] {
			w.line("%s", o.Indent+compactLine(f, w.c))
		} else {
			hidden = append(hidden, f)
		}
	}
	if n := len(hidden); n > 0 {
		var names []string
		for _, f := range hidden[:min(len(hidden), 6)] {
			names = append(names, Path(f.Path))
		}
		w.line("%s%s… and %s: %s (raise -b)%s", o.Indent, w.c.faint, plural(n, "more file"), strings.Join(names, ", "), w.c.reset)
	}
	return w.b.String()
}

func compactLine(f *analyze.File, c pal) string {
	var b strings.Builder
	b.WriteString(c.bold + Path(f.Path) + c.reset + "  " + status(f, c, true))
	var groups []string
	for _, what := range []string{"changed", "renamed", "moved", "added", "removed"} {
		var names []string
		seen := map[string]bool{}
		for _, ch := range f.Changes {
			if ch.What != what {
				continue
			}
			n := ch.Name
			if strings.HasPrefix(ch.Kind, "h") && len(ch.Kind) == 2 {
				n = Label(ch.Kind, ch.Name)
			}
			if what == "renamed" {
				n = ch.OldName + " → " + ch.Name
			}
			var notes []string
			if ch.OldSig != "" {
				notes = append(notes, "signature")
			}
			switch {
			case ch.FromFile != "":
				notes = append(notes, "from "+Path(ch.FromFile))
			case ch.MovedTo != "":
				notes = append(notes, "to "+Path(ch.MovedTo))
			case ch.From != "":
				notes = append(notes, "body from "+ch.From)
			}
			if ch.Note != "" {
				notes = append(notes, ch.Note)
			}
			if len(notes) > 0 {
				n += " (" + strings.Join(notes, ", ") + ")"
			}
			if seen[n] { // a Rust struct and its impl block share a name
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
		if len(names) > 6 {
			names = append(names[:6], fmt.Sprintf("+%d more", len(names)-6))
		}
		if len(names) > 0 {
			groups = append(groups, what+" "+strings.Join(names, ", "))
		}
	}
	if syms := f.Symbols(); len(syms) > 0 {
		if len(syms) > 6 {
			syms = append(syms[:6:6], fmt.Sprintf("+%d more", len(syms)-6))
		}
		groups = append(groups, strings.Join(syms, ", "))
	}
	if f.TopImports && len(f.Changes) > 0 {
		groups = append(groups, "imports")
	}
	if len(groups) > 0 {
		b.WriteString(": " + strings.Join(groups, "; "))
	}
	return b.String()
}

// Rank orders files by how much a reader needs them when not all fit:
// source with symbol changes, other source, tests, docs, data and fixtures,
// then lock, generated and binary files.
func Rank(f *analyze.File) int {
	p := strings.ToLower(f.Path)
	switch {
	case f.Binary || f.Generated != "" || f.Submodule:
		return 6
	case strings.Contains(p, "testdata/") || strings.Contains(p, "fixtures/") || strings.Contains(p, "snapshots/"):
		return 5
	case strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".txt") || strings.HasSuffix(p, ".rst"):
		return 3
	case strings.Contains(p, "test"):
		return 2
	case f.Outlined && (len(f.Changes) > 0 || len(f.Defs) > 0):
		return 0
	case f.Outlined:
		return 1
	}
	return 4
}

// Label is how a symbol is named in the report: "fn readback", "## Install".
func Label(kind, name string) string {
	if len(kind) == 2 && kind[0] == 'h' && kind[1] >= '1' && kind[1] <= '6' {
		return strings.Repeat("#", int(kind[1]-'0')) + " " + name
	}
	return kind + " " + name
}

func span(a, b int) string {
	if b <= a {
		return strconv.Itoa(a)
	}
	return strconv.Itoa(a) + "-" + strconv.Itoa(b)
}

// Span is exported for main: "12" or "12-30".
func Span(a, b int) string { return span(a, b) }

func clipAll(ls []string, n int) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = Clip(strings.TrimRight(l, " \t\r"), n)
	}
	return out
}

// Clip shortens s to n characters.
func Clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// Plural is exported for main.
func Plural(n int, word string) string { return plural(n, word) }
