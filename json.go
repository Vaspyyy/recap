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
		Lines: f.Lines, Generated: f.Generated, Binary: f.Binary, LineEndings: arrowless(f.LineEndings),
		Submodule: f.Submodule, Symlink: f.NewMode == "120000" || f.OldMode == "120000", Untracked: f.Untracked,
		Changes: []report.RecapChange{}}
	if (f.Status == 'R' || f.Status == 'C') && f.Similarity > 0 { // -1: a pair git didn't see
		rf.Similarity = f.Similarity
	}
	// as the text says it: a type change and a paired move say it otherwise
	if f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode && f.OldMode != "000000" && f.NewMode != "000000" &&
		f.Status != 'T' && f.PairedFrom == nil {
		rf.ModeChange = f.OldMode + " to " + f.NewMode
	}
	for _, ch := range capped(&c.lim, f.Changes) {
		rc := change(f, ch, all)
		rc.Symbol.Marks = c.marks(ch.Refs(f.Path)...)
		rf.Changes = append(rf.Changes, rc)
	}
	switch {
	case f.TopPlus+f.TopMinus > 0:
		rf.Outside = &report.RecapOutside{Added: f.TopPlus, Removed: f.TopMinus, Imports: f.TopImports, Lines: f.TopLines}
	case !f.Outlined && (f.TopReal || f.TopBlank > 0) && f.Status != 'A' && f.Status != 'D':
		// no outline rules for it: every changed line is outside a definition
		rf.Outside = &report.RecapOutside{Added: f.Added, Removed: f.Deleted, Imports: f.TopImports, Lines: f.TopLines}
	}
	if len(f.Defs) > 0 { // a new or deleted file: Defs follow its outline one to one
		syms := f.NewSyms
		if f.Status == 'D' {
			syms = f.OldSyms
		}
		for i, sym := range capped(&c.lim, report.Symbols(f.Path, syms)) {
			d := f.Defs[i]
			sym.Marks = c.marks(sym.Ref)
			rd := report.RecapDef{Symbol: sym, MovedTo: d.MovedTo, Differs: d.Differs, Public: d.Public}
			if d.From != "" {
				rd.From = movedFrom(f.Path, d.From, d.Kind, d.Name, all)
			}
			rf.Defs = append(rf.Defs, rd)
		}
	}
	return rf
}

// arrowless writes analyze's "LF → CRLF" as "LF to CRLF".
func arrowless(s string) string { return strings.ReplaceAll(s, " → ", " to ") }

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
		MovedTo: ch.MovedTo, Note: ch.Note, Public: ch.Public, Differs: ch.Differs, Members: ch.Members, Detail: ch.Detail}
	if ch.What != "removed" { // a removed symbol's own lines are the old ones
		rc.OldLine, rc.OldEnd = ch.OldLine, ch.OldEnd
	}
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
		rc.From = movedFrom(f.Path, ch.FromFile, ch.Kind, ch.Name, all)
	case ch.From != "":
		rc.From = extractedFrom(f, ch)
	}
	return rc
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

// movedFrom is the ref of the definition in file from that the definition
// kind name in path moved from (analyze.Link paired them).
func movedFrom(path, from, kind, name string, all []*analyze.File) string {
	for _, g := range all {
		if g.Path != from {
			continue
		}
		for _, gc := range g.Changes {
			if gc.What == "removed" && gc.Kind == kind && gc.Name == name && gc.MovedTo == path {
				return ref.Symbol(g.Path, gc.Name, gc.Ordinal).String()
			}
		}
		for _, d := range g.Defs {
			if g.Status == 'D' && d.Kind == kind && d.Name == name && d.MovedTo == path {
				return ref.Symbol(g.Path, d.Name, d.Ordinal).String()
			}
		}
	}
	return ref.Symbol(from, name, 0).String()
}

// shows maps what --show selected: each symbol's change and its whole diff
// (the text keeps three lines of context; programs get every line), or
// each file's. Notes says what the text says besides.
func (c *cli) shows(sel selection, files []*analyze.File, contents map[string]pair) (shows []report.RecapShow, notes []string, f *failure) {
	shows = []report.RecapShow{}
	if sel.note != "" {
		notes = append(notes, strings.TrimPrefix(sel.note, "note: "))
	}
	for _, t := range sel.hits {
		ch := change(t.f, t.ch, files)
		ch.Symbol.Marks = c.marks(t.ch.Refs(t.f.Path)...)
		shows = append(shows, report.RecapShow{Change: &ch, Diff: capped(&c.lim, diffLines(t.ch))})
	}
	for _, f := range capped(&c.lim, sel.whole) {
		if f.LineEndings != "" {
			notes = append(notes, fmt.Sprintf("%s: line endings only (%s), no other change", f.Path, arrowless(f.LineEndings)))
			shows = append(shows, report.RecapShow{File: f.Path, Diff: []string{}})
			continue
		}
		ops, fl := fileDiff(f, contents)
		if fl != nil {
			return nil, nil, fl
		}
		diff := []string{}
		for _, o := range ops {
			diff = append(diff, string(o.Kind)+o.Text)
		}
		shows = append(shows, report.RecapShow{File: f.Path, Diff: capped(&c.lim, diff)})
	}
	return shows, notes, nil
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
