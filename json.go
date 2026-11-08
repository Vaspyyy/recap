package main

import (
	"fmt"
	"strings"

	"github.com/Vaspyyy/prism/outline"
	"github.com/Vaspyyy/prism/ref"
	"github.com/Vaspyyy/prism/report"
	"github.com/Vaspyyy/recap/internal/analyze"
	"github.com/Vaspyyy/recap/internal/gitx"
)

// --json prints what the text reports as Prism's report.Recap
// (docs/output.md in Prism): the same files, changes and refs, complete
// rather than cut to the text budget.

// limiter caps each list of the JSON output (report.MaxItems, or -b when
// given) and records what it cut.
type limiter struct {
	max int
	cut *report.Truncated // shown and total summed over the lists it cut
}

func capped[T any](l *limiter, xs []T) []T {
	if len(xs) <= l.max {
		return xs
	}
	if l.cut == nil {
		l.cut = &report.Truncated{}
	}
	l.cut.Shown += l.max
	l.cut.Total += len(xs)
	return xs[:l.max]
}

// recap starts the result of a comparison.
func (c *cli) recap(comp *gitx.Comparison, commit *gitx.Commit) *report.Recap {
	res := &report.Recap{Old: sideName(comp.Old), New: sideName(comp.New), Files: []report.RecapFile{}}
	if commit != nil {
		cm := commitOf(*commit)
		res.Commit = &cm
	}
	return res
}

// sideName is how the JSON names one side of a comparison: as it was given
// ("HEAD~3", "b681ef1^"), "working tree", "index", "nothing" (the empty
// tree), or the commit id of an A...B range's merge base.
func sideName(s gitx.Side) string {
	switch {
	case s.Index:
		return "index"
	case s.Worktree:
		return "working tree"
	case s.Empty:
		return "nothing"
	case s.Base:
		return s.Rev
	}
	return s.Label
}

func commitOf(cm gitx.Commit) report.Commit {
	return report.Commit{Rev: cm.ID, Author: cm.Author, Date: cm.Time, Subject: cm.Subject, Body: cm.Body, Parents: cm.Parents}
}

// files maps the analysed files; all is every file of the comparison, for
// definitions that moved between files.
func (c *cli) files(files, all []*analyze.File) []report.RecapFile {
	out := []report.RecapFile{}
	for _, f := range capped(&c.lim, files) {
		out = append(out, c.file(f, all))
	}
	return out
}

func (c *cli) file(f *analyze.File, all []*analyze.File) report.RecapFile {
	rf := report.RecapFile{Path: f.Path, OldPath: f.OldPath, Status: fileStatus(f), Added: f.Added, Removed: f.Deleted,
		Lines: f.Lines, Generated: f.Generated, Binary: f.Binary, Changes: []report.RecapChange{}}
	for _, ch := range capped(&c.lim, f.Changes) {
		rf.Changes = append(rf.Changes, change(f, ch, all))
	}
	if f.TopPlus+f.TopMinus > 0 {
		rf.Outside = &report.LineCounts{Added: f.TopPlus, Removed: f.TopMinus}
	}
	if len(f.Defs) > 0 { // a new or deleted file: Defs follow its outline one to one
		syms := f.NewSyms
		if f.Status == 'D' {
			syms = f.OldSyms
		}
		rf.Defs = capped(&c.lim, report.Symbols(f.Path, syms))
	}
	return rf
}

// fileStatus is a file's status in one word or two.
func fileStatus(f *analyze.File) string {
	switch {
	case f.Conflict || f.Status == 'U':
		return "conflict"
	case f.PairedFrom != nil:
		return "moved" // git saw a deleted and a new file; recap paired them by their definitions
	case f.Untracked:
		return "untracked"
	case f.Status == 'A':
		return "new"
	case f.Status == 'D':
		return "deleted"
	case f.Status == 'R':
		return "renamed"
	case f.Status == 'C':
		return "copied"
	case f.Status == 'T':
		return "type changed"
	}
	return "modified"
}

// change maps one changed symbol. Its ref is the one the text prints: the
// file's path and the name with "#N" (the old version's, for removed).
func change(f *analyze.File, ch analyze.Change, all []*analyze.File) report.RecapChange {
	syms := f.NewSyms
	if ch.What == "removed" {
		syms = f.OldSyms
	}
	sym := report.Symbol{Ref: ref.Symbol(f.Path, ch.Name, ch.Ordinal).String(), Path: f.Path, Name: ch.Name, Kind: ch.Kind,
		Line: ch.Line, End: max(ch.End, ch.Line), Sig: outlineSig(syms, ch.Kind, ch.Name, ch.Line)}
	rc := report.RecapChange{What: ch.What, Symbol: sym, OldSig: ch.OldSig, NewSig: ch.NewSig, Added: ch.Plus, Removed: ch.Minus,
		MovedTo: ch.MovedTo, Note: changeNote(ch), Public: ch.Public}
	switch ch.What {
	case "renamed":
		rc.OldRef = ref.Symbol(f.Path, ch.OldName, ch.OldOrdinal).String()
	case "added": // all of its lines are new (the text leaves the count out)
		rc.Added = sym.End - sym.Line + 1
	case "removed":
		rc.Removed = sym.End - sym.Line + 1
	}
	switch {
	case ch.FromFile != "":
		rc.From = movedFrom(f, ch, all)
	case ch.From != "":
		rc.From = extractedFrom(f, ch)
	}
	return rc
}

// changeNote is what the text says in parentheses that has no field of its
// own: whether a moved or extracted body differs, members, recap's note.
func changeNote(ch analyze.Change) string {
	var notes []string
	if ch.FromFile != "" || ch.MovedTo != "" || ch.From != "" {
		switch ch.Differs {
		case 0:
			notes = append(notes, "unchanged")
		case 1:
			notes = append(notes, "1 line differs")
		default:
			notes = append(notes, fmt.Sprintf("%d lines differ", ch.Differs))
		}
	}
	if ch.Members > 0 {
		notes = append(notes, fmt.Sprintf("with %d inside", ch.Members))
	}
	if ch.Note != "" {
		notes = append(notes, ch.Note)
	}
	return strings.Join(notes, "; ")
}

// outlineSig is the outline's signature (the defining line, trimmed) of the
// symbol named name at line, as report.Symbols gives it.
func outlineSig(syms []outline.Symbol, kind, name string, line int) string {
	for i, s := range syms {
		if s.Line == line && s.Kind == kind && outline.Qualified(syms, i) == name {
			return s.Sig
		}
	}
	return ""
}

// extractedFrom is the ref of the symbol, in the old version of the same
// file, that an added symbol took its body from.
func extractedFrom(f *analyze.File, ch analyze.Change) string {
	ts := ref.Targets(f.Path, f.OldSyms)
	refs := ref.Refs(ts)
	for k, t := range ts {
		if t.Name == ch.From && t.Line == ch.OldLine {
			return refs[k].String()
		}
	}
	return ref.Symbol(f.Path, ch.From, 0).String()
}

// movedFrom is the ref of the definition in another file that an added one
// moved from (analyze.Link paired them).
func movedFrom(f *analyze.File, ch analyze.Change, all []*analyze.File) string {
	for _, g := range all {
		if g.Path != ch.FromFile {
			continue
		}
		for _, gc := range g.Changes {
			if gc.What == "removed" && gc.Kind == ch.Kind && gc.Name == ch.Name && gc.MovedTo == f.Path {
				return ref.Symbol(g.Path, gc.Name, gc.Ordinal).String()
			}
		}
		for _, d := range g.Defs {
			if g.Status == 'D' && d.Kind == ch.Kind && d.Name == ch.Name && d.MovedTo == f.Path {
				return ref.Symbol(g.Path, d.Name, d.Ordinal).String()
			}
		}
	}
	return ref.Symbol(ch.FromFile, ch.Name, 0).String()
}

// shows maps what --show selected: each symbol's change and its whole diff
// (the text keeps three lines of context; programs get every line).
func (c *cli) shows(sel selection, files []*analyze.File, contents map[string]pair) ([]report.RecapShow, *failure) {
	out := []report.RecapShow{}
	for _, t := range sel.hits {
		out = append(out, report.RecapShow{Change: change(t.f, t.ch, files), Diff: capped(&c.lim, diffLines(t.ch))})
	}
	for _, f := range capped(&c.lim, sel.whole) {
		s, fl := wholeFile(f, contents)
		if fl != nil {
			return nil, fl
		}
		s.Diff = capped(&c.lim, s.Diff)
		out = append(out, s)
	}
	return out, nil
}

// diffLines is a symbol's diff against its old version, or against where
// its body came from: every line, starting with ' ', '-' or '+'.
func diffLines(ch analyze.Change) []string {
	lines := []string{}
	switch {
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
		}
		for _, o := range ops {
			lines = append(lines, string(o.Kind)+o.Text)
		}
	}
	return lines
}

// wholeFile is "recap --show path" for one file. report.RecapShow has no
// form for a file, so its change is the file's: kind "file", its path as
// the ref, its status as what happened to it.
func wholeFile(f *analyze.File, contents map[string]pair) (report.RecapShow, *failure) {
	what := "changed"
	switch {
	case f.Untracked && f.PairedFrom == nil, f.Status == 'A':
		what = "added"
	case f.Status == 'D':
		what = "removed"
	case f.OldPath != "":
		what = "renamed"
	}
	ch := report.RecapChange{What: what, Symbol: report.Symbol{Ref: f.Path, Path: f.Path, Kind: "file", Line: 1, End: max(f.Lines, 1)},
		OldRef: f.OldPath, Added: f.Added, Removed: f.Deleted}
	if f.LineEndings != "" {
		ch.Note = "line endings only (" + f.LineEndings + ")"
		return report.RecapShow{Change: ch, Diff: []string{}}, nil
	}
	ops, fl := fileDiff(f, contents)
	if fl != nil {
		return report.RecapShow{}, fl
	}
	diff := []string{}
	for _, o := range ops {
		diff = append(diff, string(o.Kind)+o.Text)
	}
	return report.RecapShow{Change: ch, Diff: diff}, nil
}
