---
name: recap
description: Summarise git changes by symbol with `recap` instead of reading raw `git diff` or `git log -p` output. It lists which functions, types, classes and sections were changed, added, removed or renamed (with signature changes and line ranges), and `recap --show file:name` prints the diff of just one symbol. Use it whenever you review changes, write a commit message or PR description, check what a branch or commit did, or need to know what changed before reading code.
---

# recap for agents

`recap` reads a git diff and reports it per symbol. Read the overview first, then use `--show` for the few symbols whose actual change you need to see. That is usually a fraction of the text of `git diff`.

```
recap: HEAD → working tree: 2 files changed, +52 −23, 1 untracked

outline/outline.go  modified, +44 −23
  changed  func Parse  L129-187  +3 −2
  changed  func scanner.stripComments  L393-438  +39 −18
           was: func stripComments(line string, inBlock bool) (string, bool)
           now: func (s *scanner) stripComments(line string) string
  added    func Upscale  L24-26
  removed  func Dispatch  was L17-19
  renamed  func Upload → UploadScene  L12-17
  also     imports  +2 −0: +import "fmt"
notes.txt  untracked, 12 lines
```

## What to run

| Question | Command |
| --- | --- |
| What did I change (including new files)? | `recap` |
| What's staged for the commit? | `recap --staged` |
| What changed since a commit, or between two? | `recap HEAD~3`, `recap v1.2 v1.3`, `recap a..b` |
| What did this branch do? | `recap main...feature` (since it branched off) |
| What did one commit do (with its message)? | `recap show <commit>` (default HEAD) |
| What did the last commits do? | `recap log -n 5` (one line per file, with the first paragraph of each message) |
| Show me that one function's change | `recap --show path/file.go:Name` (or just `Name`; every match is shown; a class name shows its changed members) |
| What changed where this error points? | `recap --show path/file.go:312` (the changed symbol containing line 312) |
| What did a commit do to one function? | `recap --show path/file.go:Name@b681ef1` (same as `recap show b681ef1 --show path/file.go:Name`) |
| The overview and then the full diff | `recap -p` |

## Refs

glint, seek, recap and crux all print and accept the same refs: `path`, `path:Name` (qualified: `Repo.Open`, `outer.inner`), `path:12` / `12:3` / `12-40` (a line, with a column, a range; resolved to the innermost symbol containing it), `@rev` (a symbol as it was in a commit) and `#N` (the Nth of several definitions with the same name). Paths are repo-relative. Copy refs from any tool's output into `--show`: in the overview, a file's path + `:` + a symbol's name as shown is one (`outline/outline.go:scanner.stripComments`), and so are a compiler's or crux's `file:line`.

Paths after `--` limit everything to those files: `recap HEAD~3 -- src/`. Moves to files outside the paths can't be seen then. `--show` works on whatever the command compares: on a clean working tree, give the range (`recap main...feature --show name`).

## Reading the overview

- Each file gets one line: its status (modified, new, deleted, renamed from …, untracked, binary, lock file, generated) and its `+added −removed` line counts, which match `git diff --numstat`.
- Below it, one line per symbol: `changed`, `added`, `removed`, `renamed` (old → new) or `moved` (unchanged, but at another place), with its current lines (`was L…` for removed symbols) and the lines changed inside it. A symbol's own count excludes its children: a changed method is listed under its name, and the class only if its own lines changed.
- `was:` / `now:` lines show a changed signature (also one spread over several lines). Callers may need updating. `(public)` marks changes to the public API: exported or public symbols that were added, removed, renamed or got a new signature. `(required: every implementation must add it)` marks a new trait method without a default.
- Small changes show their changed lines inline (`- return round(x, 2)` / `+ return round(x + 1e-9, 2)`), budget permitting. That is often enough to see a behaviour change without `--show`.
- Code that moved is said to have moved, not reported as new. `(moved to x.py, unchanged)` / `(moved from cart.py, 3 lines differ)` link a definition that left one file and appeared in another, and say whether it changed on the way. `(body taken from Compare, 1 line differs)` marks a new function whose body came from an existing one, as in an extracted helper. When reviewing, run `--show` on it: for moved and extracted code it prints the diff against where the code came from, so the differing lines (where a change hidden in a refactor sits) are visible. `(whitespace only)` means only indentation or spacing changed.
- `moved from a.ts (git saw a delete and a new file; …)`: a file that was moved and edited too much for git to call it a rename, paired by the definitions both have and compared per symbol.
- `merge in progress` and `merge conflict (unresolved)` describe a merge that is under way; the latter marks files with conflict markers.
- `also imports` or `also lines outside definitions` covers changes between symbols, such as imports, constants and top-level code. When there are only one or two such lines, they are shown.
- New and deleted files list their top-level symbols instead of line changes.
- Symbols come from the outline rules shared with glint and seek (Go, Rust, Python, JS/TS, C/C++, Java, Kotlin, C#, GDScript, shell, Lua, Ruby, Markdown headings). Other files get counts only, plus the changed lines when there are only one or two.
- `moved` (without a file) means a definition changed places among the others, unchanged. Lines inserted above a definition don't make it "moved". `(doc comment only)` and `(decorators changed)` say that only the lines above a definition changed; decorator changes can change behaviour. `--show` includes doc comments and decorators.
- `renamed … (rewritten)` is a function at the same place with a similar name and a new body. `line endings only (LF → CRLF)` marks a line-ending change, like `(whitespace only)` above.
- Under the budget (default 150 lines, `-b N`), every file gets its line first, then a share of its changes, with signature changes, renames and removals first. Cut changes are summarised by kind (`… +390 more changes: 390 changed (each +1 −1)`).
- The header's totals are git's own (`git diff --numstat`), also when recap pairs a moved file that git saw as deleted and new.

Lines in `--show` are clipped at 400 characters and the output stops after 400 lines; use `git diff` for more.

## Limits

Symbols use the same outline rules as `glint -A <file>`, so languages glint can't outline get counts only. If `--show name` says no changed symbol matches, it lists the changed ones as refs; pick from that list (e.g. not `main.go:main` when `main` didn't change). A name matches exactly, else as the end of a qualified name (`stripComments` finds `scanner.stripComments`). For a line outside every changed symbol, recap names the symbol that holds it, says it didn't change, and lists what did.

recap matches symbols by kind and name, so a symbol that was renamed and heavily rewritten at the same time shows as removed plus added. Definitions the outline rules don't know (Go `var` blocks, macros, config keys) count as lines outside definitions. When the exact change matters, use `--show`, or `git diff` for files without symbols.
