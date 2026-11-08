package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Vaspyyy/prism/focus"
	"github.com/Vaspyyy/prism/outline"
	"github.com/Vaspyyy/prism/ref"
	prismrepo "github.com/Vaspyyy/prism/repo"
	"github.com/Vaspyyy/prism/report"
	"github.com/Vaspyyy/prism/work"
	"github.com/Vaspyyy/recap/internal/analyze"
	"github.com/Vaspyyy/recap/internal/gitx"
	"github.com/Vaspyyy/recap/internal/render"
)

// Context, as Prism's docs/context.md describes it: recap reads the focus
// and crux's last run, and writes the focus. It marks changed symbols that
// failed in the last run, fills an empty --show from the focus, and says so
// on a context: line. --no-context (or PRISM_NO_CONTEXT=1) turns all of it
// off. The next: lines come from the result alone, so they're the same
// either way.

// setsFocus reports whether the run shows commits on both sides, given
// explicitly: recap show [<commit>], recap A B, A..B and A...B. Those set
// the focus; the working tree, the index and recap log don't.
func setsFocus(o *opts) bool {
	switch {
	case o.sub == "show":
		return true
	case o.sub != "" || o.staged:
		return false
	}
	return len(o.revs) == 2 || len(o.revs) == 1 && strings.Contains(o.revs[0], "..")
}

// load reads the context the run uses. A run that sets the focus doesn't
// read the old one: it replaces it, so [focus] marks from it would point
// at a record that's gone once the run ends.
func (c *cli) load(o *opts) {
	off := work.Disabled(o.noContext)
	layers := work.Focus | work.LastRun
	if setsFocus(o) {
		layers = work.LastRun
	}
	var info *prismrepo.Info
	if !off {
		info, _ = c.detect()
	}
	c.st = work.Load(info, layers, off)
}

var markOrder = []string{work.MarkChanged, work.MarkBranch, work.MarkFocus, work.MarkFailing}

// peek is the context marks of a symbol, by its refs (a rename's new ref
// and its old one), not counted as shown. On the focus's own comparison
// [focus] is left out: every symbol there is in the focus, so the mark
// would say nothing.
func (c *cli) peek(refs ...string) []string {
	st := c.st
	if st == nil {
		return nil
	}
	f := st.Focus
	own := f != nil && c.cur != nil && c.cur.Old.Rev == f.Old && c.cur.New.Rev == f.New
	var out []string
	for _, r := range refs {
		for _, m := range st.Peek(r) {
			if !slices.Contains(out, m) && !(own && m == work.MarkFocus) {
				out = append(out, m)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return slices.Index(markOrder, a) - slices.Index(markOrder, b) })
	return out
}

// marks is peek for a symbol recap prints: the marks count as shown.
func (c *cli) marks(refs ...string) []string {
	m := c.peek(refs...)
	c.st.Show(m...)
	return m
}

// suffix is marks as printed after a name: " [failing]", or "".
func (c *cli) suffix(refs ...string) string { return work.Suffix(c.marks(refs...)) }

// setFocus records what a run that sets the focus showed: the revisions as
// given, both sides' commits and the refs of the changed symbols on the new
// side. A failed write doesn't fail the run.
func (c *cli) setFocus(o *opts, comp *gitx.Comparison, commit *gitx.Commit, files []*analyze.File) {
	if c.st == nil || !setsFocus(o) || comp.New.Worktree || comp.New.Index {
		return
	}
	syms := focusSymbols(files)
	c.st.Store.SetFocus("recap", focus.Focus{Range: focusRange(o, commit), Old: comp.Old.Rev, New: comp.New.Rev, Symbols: syms, Total: len(syms)})
}

// focusRange is the focus's range (focus.Focus.Range): recap show's commit
// by its short id (a name like HEAD would move on), a range as given, and
// A..B for two revisions (what git diff A B compares).
func focusRange(o *opts, commit *gitx.Commit) string {
	switch {
	case o.sub == "show":
		return commit.Short
	case len(o.revs) == 2:
		return o.revs[0] + ".." + o.revs[1]
	}
	return o.revs[0]
}

// focusSymbols are the refs of the changed symbols on the new side, as
// recap prints them: changed, added and renamed ones, and the definitions
// of new files. Removed ones aren't there any more. An added or renamed
// symbol brings the definitions inside it (a class's methods): they're new
// too, or renamed with it, and seek and crux name a location by the
// innermost symbol containing it.
func focusSymbols(files []*analyze.File) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for _, f := range files {
		var refs []ref.Ref // the new side's refs, when a symbol needs its members
		for _, ch := range f.Changes {
			if ch.What != "changed" && ch.What != "added" && ch.What != "renamed" {
				continue
			}
			add(ch.Refs(f.Path)[0])
			if ch.What == "changed" {
				continue // its changed members are changes of their own
			}
			if refs == nil {
				refs = ref.Refs(ref.Targets(f.Path, f.NewSyms))
			}
			for _, j := range members(f.NewSyms, ch.Name, ch.Line) {
				add(refs[j].String())
			}
		}
		if f.Status != 'D' {
			for _, d := range f.Defs {
				add(d.Ref(f.Path))
			}
		}
	}
	return out
}

// members are the indexes of the definitions inside the one named name at
// line in syms, nested ones included.
func members(syms []outline.Symbol, name string, line int) []int {
	top := -1
	for i, s := range syms {
		if s.Line == line && outline.Qualified(syms, i) == name {
			top = i
			break
		}
	}
	var out []int
	if top < 0 {
		return out
	}
	for j := range syms {
		for p := syms[j].Parent; p >= 0; p = syms[p].Parent {
			if p == top {
				out = append(out, j)
				break
			}
		}
	}
	return out
}

// focusComparison is the comparison the focus records, by its commits. Its
// sides are named by their short ids: a range's names may have moved on
// since the focus was written.
func focusComparison(f *focus.Focus, paths []string) *gitx.Comparison {
	c := &gitx.Comparison{Old: gitx.Side{Rev: f.Old, Label: f.Old[:min(len(f.Old), 7)]}, New: gitx.Side{Rev: f.New, Label: f.New[:min(len(f.New), 7)]},
		DiffArgs: []string{f.Old, f.New}, Paths: paths}
	switch {
	case strings.Contains(f.Range, "..."):
		c.Old.Base = true // the merge base
	case strings.Contains(f.Range, ".."):
	case f.Old == gitx.EmptyTree():
		c.Old.Label, c.Old.Empty = "nothing", true
	default:
		c.Old.Label = c.New.Label + "^"
	}
	return c
}

// view is what a run looks at: a comparison and its analysed files.
type view struct {
	comp     *gitx.Comparison
	commit   *gitx.Commit
	files    []*analyze.File
	contents map[string]pair
}

// fill is what --show prints when nothing in the working tree matches:
// the match in the focus, if it has one (rule 3 of Prism's
// docs/context.md: fill only what's empty, and say so). Only plain recap
// --show is filled: a revision or --staged is explicit, and a location's
// line numbers are the working tree's.
func (c *cli) fill(top string, o *opts, info *prismrepo.Info, r ref.Ref, want string) (view, selection, bool) {
	var f *focus.Focus
	if c.st != nil {
		f = c.st.Focus
	}
	if f == nil || o.sub != "" || len(o.revs) > 0 || o.staged || r.IsLocation() {
		return view{}, selection{}, false
	}
	v := view{comp: focusComparison(f, o.paths)}
	var err error
	if v.files, v.contents, err = collect(top, v.comp, false); err != nil {
		return view{}, selection{}, false
	}
	sel, fl := pick(v.comp, v.files, info, r, want)
	if fl != nil {
		return view{}, selection{}, false
	}
	if !strings.Contains(f.Range, "..") {
		if cm, err := gitx.ShowCommit(f.New); err == nil {
			v.commit = &cm
		}
	}
	c.st.Use(work.Fill)
	c.st.Note("nothing matches in the working tree; showing focus " + f.Name()) // the context: line's words
	return v, sel, true
}

// seekNext is the next: line for seek on the most significant of the
// ranked symbols that exists in the working tree: seek searches the
// working tree, so a symbol that's gone there would find nothing.
func seekNext(top string, ranked []target) []report.Next {
	have := map[string][]string{}
	for i, t := range ranked {
		if i == 20 {
			break
		}
		if r := t.ref(); inWorktree(top, t.f.Path, r, have) {
			return []report.Next{{Cmd: report.Cmd("seek", r), Why: "definition and uses by name"}}
		}
	}
	return nil
}

// showNext is the next: line for recap --show on the most significant of
// the ranked symbols whose change the overview didn't show: its changed
// lines weren't printed inline (inline holds inlineKey of those that were).
func showNext(o *opts, commit *gitx.Commit, ranked []target, inline map[string]bool) []report.Next {
	for _, t := range ranked {
		if hasDiff(t.ch) && !inline[inlineKey(t.f.Path, t.ch)] {
			args := append(append([]string{"recap"}, revArgs(o, commit)...), "--show", t.ref())
			return []report.Next{{Cmd: report.Cmd(args...), Why: "its diff"}}
		}
	}
	return nil
}

func inlineKey(path string, ch analyze.Change) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", path, ch.What, ch.Name, ch.Line)
}

// hasDiff reports whether --show prints a diff for the symbol, rather than
// just its lines: it changed, or it's code that came from elsewhere and
// differs from where it came from.
func hasDiff(ch analyze.Change) bool {
	switch ch.What {
	case "changed", "renamed":
		return ch.Note != "whitespace only"
	case "added":
		return (ch.FromFile != "" || ch.From != "") && ch.Differs > 0
	}
	return false
}

// sigOnly reports whether only a change's signature changed, which its
// was: and now: lines show in full.
func sigOnly(ch analyze.Change) bool {
	if ch.OldSig == "" {
		return false
	}
	ops := analyze.LineDiffBy(ch.OldText, ch.NewText, func(l string) string { return strings.Join(strings.Fields(l), " ") })
	if ops == nil {
		return false
	}
	for _, op := range ops {
		l := strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(op.Text), "{")), ",")
		if op.Kind != ' ' && l != "" && !strings.Contains(ch.OldSig, l) && !strings.Contains(ch.NewSig, l) {
			return false
		}
	}
	return true
}

// overview renders the overview, with room for next next: lines besides
// it, and says which changes it showed in full: with their changed lines,
// a signature-only change with its was: and now: lines, or a rename that
// changed nothing else. record counts the marks it prints as shown.
func (c *cli) overview(files []*analyze.File, o *opts, color bool, next int, record bool) (string, map[string]bool) {
	inline := map[string]bool{}
	ro := render.Options{Color: color, Budget: o.budget - 2, Marks: c.peek, Printed: func(path string, ch analyze.Change, detail bool) {
		if detail || sigOnly(ch) || ch.RenameOnly() {
			inline[inlineKey(path, ch)] = true
		}
	}}
	if next > 0 {
		ro.Extra = 1 + next // a blank line, then the next: lines
	}
	if record {
		ro.Shown = func(m []string) { c.st.Show(m...) }
	}
	return render.Files(files, ro), inline
}

// suggestions are the overview's next: lines, and its text with room for
// them. The --show line depends on what the overview could show inline,
// and its room on the lines: with a --show line the overview is planned
// once more, which can only show less inline, so the line stays.
func (c *cli) suggestions(top string, o *opts, commit *gitx.Commit, files []*analyze.File, color, record bool) ([]report.Next, string) {
	ranked := significant(newSide(files))
	next := seekNext(top, ranked)
	text, inline := c.overview(files, o, color, len(next), record)
	if show := showNext(o, commit, ranked, inline); show != nil {
		text, inline = c.overview(files, o, color, len(next)+1, record)
		next = append(next, showNext(o, commit, ranked, inline)...)
	}
	return next, text
}

// significant orders the targets worth a suggestion, most significant
// first: symbols in source files before tests and docs, then a changed
// signature, then the most changed lines (all of an added symbol's).
// Removed symbols, old names, Markdown sections and generated or fixture
// files aren't worth one.
func significant(ts []target) []target {
	var out []target
	for _, t := range ts {
		k := t.ch.Kind
		heading := len(k) == 2 && k[0] == 'h' && k[1] >= '1' && k[1] <= '6'
		if t.old || t.name != t.ch.Name || heading || render.Rank(t.f) >= 5 {
			continue
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := render.Rank(a.f), render.Rank(b.f); ra != rb {
			return ra < rb
		}
		if ta, tb := tier(a.ch), tier(b.ch); ta != tb {
			return ta < tb
		}
		if sa, sb := size(a.ch), size(b.ch); sa != sb {
			return sa > sb
		}
		return a.ch.What != "added" && b.ch.What == "added"
	})
	return out
}

// tier ranks what happened to a symbol: its signature changed (callers may
// need to follow), its code changed, or nothing much (moved, copied
// unchanged, whitespace or doc comment only).
func tier(ch analyze.Change) int {
	switch {
	case ch.What == "moved", ch.Note == "whitespace only", ch.Note == "doc comment only",
		(ch.FromFile != "" || ch.From != "") && ch.Differs == 0:
		return 2
	case ch.OldSig != "":
		return 0
	}
	return 1
}

// size is how many of a symbol's lines changed.
func size(ch analyze.Change) int {
	switch {
	case ch.FromFile != "" || ch.From != "":
		return ch.Differs
	case ch.What == "added":
		return max(ch.End, ch.Line) - ch.Line + 1
	}
	return ch.Plus + ch.Minus
}

// inWorktree reports whether the working tree's copy of path has a symbol
// with exactly the ref r. have caches each file's refs.
func inWorktree(top, path, r string, have map[string][]string) bool {
	refs, ok := have[path]
	if !ok {
		if data, err := os.ReadFile(filepath.Join(top, path)); err == nil {
			for _, x := range ref.Refs(ref.Targets(path, outline.Parse(path, data, 5000))) {
				refs = append(refs, x.String())
			}
		}
		have[path] = refs
	}
	return slices.Contains(refs, r)
}

// revArgs are the arguments that make recap compare what this run did:
// the revisions as given, and recap show's commit by its short id when it
// wasn't given.
func revArgs(o *opts, commit *gitx.Commit) []string {
	switch {
	case o.sub == "show" && len(o.revs) == 1:
		return []string{"show", o.revs[0]}
	case o.sub == "show":
		return []string{"show", commit.Short}
	case o.staged:
		return append([]string{"--staged"}, o.revs...)
	}
	return o.revs
}

// newSide lists the changed symbols on the new side of the files, for
// suggestions: changes but removals, and the definitions of new files.
func newSide(files []*analyze.File) []target {
	var out []target
	for _, f := range files {
		for _, ch := range f.Changes {
			if ch.What != "removed" {
				out = append(out, target{f: f, ch: ch, name: ch.Name, ord: ch.Ordinal, line: ch.Line})
			}
		}
		if f.Status == 'D' {
			continue
		}
		for _, d := range f.Defs {
			ch := analyze.Change{What: "added", Kind: d.Kind, Name: d.Name, Ordinal: d.Ordinal, Line: d.Line, End: d.End, FromFile: d.From, Differs: d.Differs}
			out = append(out, target{f: f, ch: ch, name: d.Name, ord: d.Ordinal, line: d.Line})
		}
	}
	return out
}
