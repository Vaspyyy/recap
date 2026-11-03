// Package render prints recap's overview, plain for agents or in color.
package render

import (
	"fmt"
	"strconv"
	"strings"

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

// Counts is "+12 −3" (with a real minus sign, as git's --stat doesn't).
func Counts(plus, minus int, c pal) string {
	return c.green + "+" + strconv.Itoa(plus) + c.reset + " " + c.red + "−" + strconv.Itoa(minus) + c.reset
}

// Summary is the totals line: "3 files changed, +120 −45".
func Summary(files []*analyze.File, color bool) string {
	c := palette(color)
	plus, minus, untracked := 0, 0, 0
	for _, f := range files {
		plus += f.Added
		minus += f.Deleted
		if f.Untracked {
			untracked++
		}
	}
	s := plural(len(files)-untracked, "file") + " changed, " + Counts(plus, minus, c)
	if untracked > 0 {
		s += fmt.Sprintf(", %d untracked", untracked)
	}
	return s
}

// Files prints the overview of files within the budget: every file gets
// its line first, then the symbol changes fill what's left.
func Files(files []*analyze.File, o Options) string {
	w := &writer{c: palette(o.Color)}
	room := max(o.Budget, 10)
	shownFiles := min(len(files), max(room, 30))
	room -= shownFiles
	// share the rest out in file order: each file's changes, while they fit
	quota := make([]int, shownFiles)
	for i, f := range files[:shownFiles] {
		need := 0
		for _, ch := range f.Changes {
			need += changeLines(ch)
		}
		if f.TopPlus+f.TopMinus > 0 && len(f.Changes) > 0 {
			need++
		}
		quota[i] = min(need, max(room, 0))
		room -= quota[i]
	}
	for i, f := range files[:shownFiles] {
		w.file(f, quota[i], o.Indent)
	}
	if n := len(files) - shownFiles; n > 0 {
		var names []string
		for _, f := range files[shownFiles:min(len(files), shownFiles+8)] {
			names = append(names, f.Path)
		}
		more := ""
		if n > len(names) {
			more = fmt.Sprintf(" +%d more", n-len(names))
		}
		w.line("%s… and %s not shown: %s%s (raise -b to see them)%s", o.Indent, plural(n, "more file"), strings.Join(names, ", "), more, w.c.reset)
	}
	return w.b.String()
}

// Compact prints one line per file: what happened to which symbols.
func Compact(files []*analyze.File, o Options) string {
	w := &writer{c: palette(o.Color)}
	shown := min(len(files), max(o.Budget, 1))
	for _, f := range files[:shown] {
		w.line("%s", o.Indent+compactLine(f, w.c))
	}
	if n := len(files) - shown; n > 0 {
		w.line("%s%s… and %s%s", o.Indent, w.c.faint, plural(n, "more file"), w.c.reset)
	}
	return w.b.String()
}

func compactLine(f *analyze.File, c pal) string {
	var b strings.Builder
	b.WriteString(c.bold + f.Path + c.reset + "  ")
	switch {
	case f.Binary && f.Untracked:
		b.WriteString("untracked, binary")
	case f.Binary && f.Status == 'A':
		b.WriteString(c.green + "new" + c.reset + ", binary")
	case f.Binary && f.Status == 'D':
		b.WriteString(c.red + "deleted" + c.reset + ", binary")
	case f.Untracked:
		b.WriteString("untracked, " + plural(f.Lines, "line"))
	case f.Status == 'A':
		b.WriteString(c.green + "new" + c.reset + ", " + plural(f.Lines, "line"))
	case f.Status == 'D':
		b.WriteString(c.red + "deleted" + c.reset + ", " + plural(f.Lines, "line"))
	case f.Binary:
		b.WriteString("binary")
	default:
		if f.Status == 'R' {
			b.WriteString(c.yellow + "renamed from " + f.OldPath + c.reset + ", ")
		}
		b.WriteString(Counts(f.Added, f.Deleted, c))
	}
	if f.Generated != "" {
		b.WriteString(", " + f.Generated)
	}
	var groups []string
	for _, what := range []string{"changed", "renamed", "moved", "added", "removed"} {
		var names []string
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
			if ch.OldSig != "" && what == "changed" {
				n += " (signature)"
			}
			names = append(names, n)
		}
		if len(names) > 6 {
			names = append(names[:6], fmt.Sprintf("+%d more", len(names)-6))
		}
		if len(names) > 0 {
			groups = append(groups, what+" "+strings.Join(names, ", "))
		}
	}
	if len(f.Symbols) > 0 {
		syms := f.Symbols
		if len(syms) > 6 {
			syms = append(syms[:6:6], fmt.Sprintf("+%d more", len(syms)-6))
		}
		groups = append(groups, strings.Join(syms, ", "))
	}
	if f.TopPlus+f.TopMinus > 0 && len(f.Changes) > 0 && f.Imports() {
		groups = append(groups, "imports")
	}
	if len(groups) > 0 {
		b.WriteString(": " + strings.Join(groups, "; "))
	}
	return b.String()
}

func changeLines(ch analyze.Change) int {
	if ch.OldSig != "" {
		return 3
	}
	return 1
}

func (w *writer) file(f *analyze.File, quota int, ind string) {
	c := w.c
	head := ind + c.bold + f.Path + c.reset
	var status string
	switch {
	case f.Untracked:
		status = c.green + "untracked" + c.reset
	case f.Status == 'A':
		status = c.green + "new" + c.reset
	case f.Status == 'D':
		status = c.red + "deleted" + c.reset
	case f.Status == 'R':
		status = c.yellow + fmt.Sprintf("renamed from %s (%d%% same)", f.OldPath, f.Similarity) + c.reset
	case f.Status == 'C':
		status = c.yellow + fmt.Sprintf("copied from %s (%d%% same)", f.OldPath, f.Similarity) + c.reset
	case f.Status == 'T':
		status = c.yellow + "type changed (file, symlink or submodule)" + c.reset
	case f.Status == 'U':
		status = c.red + "unmerged (conflict)" + c.reset
	default:
		status = c.yellow + "modified" + c.reset
	}
	var tail []string
	switch {
	case f.Submodule:
		tail = append(tail, "submodule")
	case f.Binary:
		tail = append(tail, "binary")
	case f.Status == 'A' || f.Status == 'D' || f.Untracked:
		tail = append(tail, plural(f.Lines, "line"))
	default:
		tail = append(tail, Counts(f.Added, f.Deleted, c))
	}
	if f.Generated != "" {
		tail = append(tail, f.Generated)
	}
	if f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode && f.OldMode != "000000" && f.NewMode != "000000" && f.Status != 'T' {
		tail = append(tail, "mode "+f.OldMode+" → "+f.NewMode)
	}
	line := head + "  " + status + ", " + strings.Join(tail, ", ")
	if len(f.Symbols) > 0 {
		syms := f.Symbols
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

	used := 0
	shown := 0
	for _, ch := range f.Changes {
		if used+changeLines(ch) > quota {
			break
		}
		w.change(ch, ind)
		used += changeLines(ch)
		shown++
	}
	topShown := false
	if f.TopPlus+f.TopMinus > 0 && len(f.Changes) > 0 && used < quota {
		what := "lines outside definitions"
		if f.Imports() {
			what = "imports"
		}
		detail := ""
		if len(f.TopLines) > 0 {
			detail = ": " + c.faint + strings.Join(clipAll(f.TopLines, 80), "  ") + c.reset
		}
		w.line("%s  %s%-8s%s %s  %s%s", ind, c.faint, "also", c.reset, what, Counts(f.TopPlus, f.TopMinus, c), detail)
		topShown = true
	}
	if n := len(f.Changes) - shown; n > 0 {
		w.line("%s  %s… +%s (recap --show %s:<name>, or raise -b)%s", ind, c.faint, plural(n, "more change"), f.Path, c.reset)
	} else if !topShown && f.TopPlus+f.TopMinus > 0 && len(f.Changes) > 0 {
		w.line("%s  %s… and lines outside definitions %s%s", ind, c.faint, Counts(f.TopPlus, f.TopMinus, pal{}), c.reset)
	}
}

func (w *writer) change(ch analyze.Change, ind string) {
	c := w.c
	color := map[string]string{"changed": c.yellow, "added": c.green, "removed": c.red, "renamed": c.blue, "moved": c.blue}[ch.What]
	name := Label(ch.Kind, ch.Name)
	if ch.What == "renamed" {
		name = Label(ch.Kind, ch.OldName) + " → " + ch.Name
	}
	var where string
	switch ch.What {
	case "removed":
		where = fmt.Sprintf("was L%s", span(ch.Line, ch.End))
	default:
		where = "L" + span(ch.Line, ch.End)
	}
	extra := ""
	if ch.Plus+ch.Minus > 0 && ch.What != "added" && ch.What != "removed" {
		extra = "  " + Counts(ch.Plus, ch.Minus, c)
	}
	if ch.Members > 0 {
		extra += c.faint + fmt.Sprintf("  (with %d inside)", ch.Members) + c.reset
	}
	w.line("%s  %s%-8s%s %s  %s%s%s%s", ind, color, ch.What, c.reset, c.bold+name+c.reset, c.cyan, where, c.reset, extra)
	if ch.OldSig != "" {
		w.line("%s           %swas: %s%s", ind, c.faint, ch.OldSig, c.reset)
		w.line("%s           %snow: %s%s", ind, c.faint, ch.NewSig, c.reset)
	}
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

// Palette is exported for main.
func Palette(on bool) pal { return palette(on) }
