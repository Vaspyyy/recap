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

// marks are the context marks of a symbol recap prints, by its refs (a
// rename's new ref and its old one), counted as shown. On the focus's own
// comparison [focus] is left out: every symbol there is in the focus, so
// the mark would say nothing.
func (c *cli) marks(refs ...string) []string {
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
	st.Show(out...)
	return out
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
// of new files. Removed ones aren't there any more.
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
		for _, ch := range f.Changes {
			if ch.What == "changed" || ch.What == "added" || ch.What == "renamed" {
				add(ch.Refs(f.Path)[0])
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

// focusComparison is the comparison the focus records, by its commits.
func focusComparison(f *focus.Focus, paths []string) *gitx.Comparison {
	c := &gitx.Comparison{Old: gitx.Side{Rev: f.Old, Label: f.Old[:min(len(f.Old), 7)]}, New: gitx.Side{Rev: f.New, Label: f.New[:min(len(f.New), 7)]},
		DiffArgs: []string{f.Old, f.New}, Paths: paths}
	sides := func(sep string) (string, string) {
		a, b, _ := strings.Cut(f.Range, sep)
		if a == "" {
			a = "HEAD"
		}
		if b == "" {
			b = "HEAD"
		}
		return a, b
	}
	switch {
	case strings.Contains(f.Range, "..."):
		a, b := sides("...")
		c.Old.Label, c.Old.Base, c.New.Label = "the branch point", true, b
		c.Desc = fmt.Sprintf("%s since it branched off %s (at %s)", b, a, f.Old[:min(len(f.Old), 9)])
	case strings.Contains(f.Range, ".."):
		c.Old.Label, c.New.Label = sides("..")
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

// suggest returns the next: lines for a result: seek for its most
// significant changed symbol that exists in the working tree (seek
// searches the working tree, so a symbol that's gone there would find
// nothing), and with diffs set, recap --show for the most significant one
// when the overview can't show its changed lines inline.
func suggest(top string, o *opts, commit *gitx.Commit, ts []target, diffs bool) []report.Next {
	ranked := significant(ts)
	var next []report.Next
	have := map[string][]string{}
	for i, t := range ranked {
		if i == 20 {
			break
		}
		if r := t.ref(); inWorktree(top, t.f.Path, r, have) {
			next = append(next, report.Next{Cmd: report.Cmd("seek", r), Why: "definition and uses by name"})
			break
		}
	}
	if !diffs {
		return next
	}
	for _, t := range ranked {
		if hasDiff(t.ch) && !shownInline(t.ch) {
			args := append(append([]string{"recap"}, revArgs(o, commit)...), "--show", t.ref())
			next = append(next, report.Next{Cmd: report.Cmd(args...), Why: "its diff"})
			break
		}
	}
	return next
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

// shownInline reports whether the overview shows all of a change: its
// changed lines inline (analyze keeps them only when they all fit), or
// only its signature changed, which the was: and now: lines show.
func shownInline(ch analyze.Change) bool {
	if len(ch.Detail) > 0 {
		return true
	}
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
