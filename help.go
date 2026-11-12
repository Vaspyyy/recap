package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// help colors, the same 256-color palette as glint's and crux's help
const (
	hTitle   = "\x1b[38;5;183;1m"
	hDim     = "\x1b[38;5;59m"
	hTagline = "\x1b[38;5;146;3m"
	hSection = "\x1b[38;5;111;1m"
	hShort   = "\x1b[38;5;151m"
	hLong    = "\x1b[38;5;117m"
	hText    = "\x1b[38;5;189m"
	hReset   = "\x1b[0m"
)

type helpFlag struct{ short, long, arg, text string }

type helpPair struct{ name, text string }

var helpSections = []struct {
	title string
	flags []helpFlag
}{
	{"WHAT TO COMPARE", []helpFlag{
		{"-s", "--staged", "", "compare the index (staged changes) with HEAD"},
		{"", "--no-untracked", "", "leave untracked files out"},
		{"-n", "", "N", "recap log: how many commits (default 10)"},
	}},
	{"OUTPUT", []helpFlag{
		{"", "--show", "TARGET", "print the diff of changed symbols: file:name, file:line, name, or a file"},
		{"-p", "--patch", "", "print the overview, then git's full diff"},
		{"-b", "--budget", "N", "roughly how many lines the overview may use (default 150)"},
		{"-A", "--agent", "", "plain output without colors (default when not a terminal)"},
		{"", "--json", "", "one JSON document for programs instead (Prism's report.Recap)"},
	}},
	{"OTHER", []helpFlag{
		{"", "--no-context", "", "ignore and don't record context; also PRISM_NO_CONTEXT=1"},
		{"-h", "--help", "", "show this help"},
		{"-V", "--version", "", "show the version"},
	}},
}

var helpExamples = []helpPair{
	{"recap", "the working tree (and untracked files) compared with HEAD"},
	{"recap --staged", "what's staged for the next commit"},
	{"recap HEAD~3", "the working tree compared with HEAD~3"},
	{"recap main...feature", "what feature changed since it branched off main"},
	{"recap show abc123", "what one commit did, with its message (default HEAD)"},
	{"recap log -n 5", "the last five commits, one line per file"},
	{"recap --show gpu.rs:readback", "the diff of one symbol"},
	{"recap --show gpu.rs:120", "the diff of the changed symbol containing line 120"},
	{"recap --show main.go:run@b681ef1", "what commit b681ef1 did to run"},
}

var helpNotes = []helpPair{
	{"changes", "changed, added, removed, renamed and moved symbols, with line ranges"},
	{"refs", "path, path:Name, path:12, @rev and #N name things the same way in glint, seek, recap and crux"},
	{"context", "recap show and A..B record the focus seek and crux use; --show falls back to it; [failing]: failed in crux's last run"},
	{"NO_COLOR", "set it to turn colors off"},
}

// helpText renders the help, colored like glint's when color is true.
func helpText(color bool) string {
	paint := func(code, s string) string {
		if !color || s == "" {
			return s
		}
		return code + s + hReset
	}
	const flagWidth = 24 // "-b, --budget N" and friends, padded to one column
	var b strings.Builder
	section := func(title string) {
		fmt.Fprintf(&b, "\n  %s\n", paint(hSection, title))
	}
	pairs := func(ps []helpPair, w int, nameColor, textColor string) {
		for _, p := range ps {
			w = max(w, len(p.name))
		}
		for _, p := range ps {
			pad := strings.Repeat(" ", w-len(p.name)+2)
			fmt.Fprintf(&b, "    %s%s%s\n", paint(nameColor, p.name), pad, paint(textColor, p.text))
		}
	}

	fmt.Fprintf(&b, "\n  %s %s  %s\n", paint(hTitle, "✦ recap"), paint(hDim, "v"+version),
		paint(hTagline, "a git diff that understands code"))

	section("USAGE")
	fmt.Fprintf(&b, "    recap %s %s %s\n", paint(hDim, "[flags]"),
		paint(hLong, "[<rev> | <rev> <rev> | <a>..<b> | <a>...<b>]"), paint(hDim, "[-- <path>...]"))
	fmt.Fprintf(&b, "    recap %s %s %s %s\n", paint(hShort, "show"), paint(hDim, "[flags]"),
		paint(hLong, "[<commit>]"), paint(hDim, "[-- <path>...]"))
	fmt.Fprintf(&b, "    recap %s %s %s %s\n", paint(hShort, "log"), paint(hDim, "[-n N] [flags]"),
		paint(hLong, "[<revision range>]"), paint(hDim, "[-- <path>...]"))

	for _, s := range helpSections {
		section(s.title)
		for _, f := range s.flags {
			left, plain := "    ", "    "
			if f.short != "" {
				left = paint(hShort, f.short)
				plain = f.short
				if f.long != "" {
					left += paint(hDim, ", ")
					plain += ", "
				}
			}
			if f.long != "" {
				left += paint(hLong, f.long)
				plain += f.long
			}
			if f.arg != "" {
				left += " " + paint(hDim, f.arg)
				plain += " " + f.arg
			}
			fmt.Fprintf(&b, "    %s%s%s\n", left, strings.Repeat(" ", max(flagWidth-len(plain), 2)), paint(hText, f.text))
		}
	}

	section("EXAMPLES")
	pairs(helpExamples, flagWidth-2, hLong, hDim) // descriptions line up with the flags' at least
	section("GOOD TO KNOW")
	pairs(helpNotes, 0, hLong, hDim)
	b.WriteString("\n")
	return b.String()
}

// wantsColor reports whether text written to f may be colored.
func wantsColor(f *os.File) bool {
	return term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
}
