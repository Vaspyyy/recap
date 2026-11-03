// Package gitx runs git and reads what a comparison needs: the changed
// files, their old and new contents, and git's own hunks.
package gitx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// git runs with a fixed, script-friendly configuration whatever the user's
// settings are: no pager, no colors, no external diff tools or textconv,
// unquoted paths, English messages.
var baseArgs = []string{"-c", "core.quotepath=off", "-c", "color.ui=false", "-c", "diff.noprefix=false",
	"-c", "diff.mnemonicPrefix=false", "-c", "diff.relative=false", "--no-pager"}

func command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", append(append([]string(nil), baseArgs...), args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANGUAGE=en", "GIT_PAGER=cat", "GIT_EXTERNAL_DIFF=", "GIT_CONFIG_NOSYSTEM=")
	return cmd
}

// Run runs git and returns stdout; on failure the error carries git's message.
func Run(args ...string) ([]byte, error) {
	cmd := command(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// Toplevel is the root of the repository containing the working directory.
func Toplevel() (string, error) {
	out, err := Run("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Side is one end of a comparison.
type Side struct {
	Rev      string // a commit id, "" for the working tree, ":" for the index
	Label    string // how the user named it ("HEAD~3", "working tree")
	Worktree bool
	Index    bool
	Empty    bool // the empty tree (before the first commit)
}

// Comparison is what to compare, worked out from the user's arguments the
// way git diff reads them.
type Comparison struct {
	Old, New Side
	Desc     string   // how the header names it, when "old → new" isn't clear
	DiffArgs []string // the revision arguments for git diff
	Paths    []string // pathspecs after --
}

func (c *Comparison) Describe() string {
	if c.Desc != "" {
		return c.Desc
	}
	return c.Old.Label + " → " + c.New.Label
}

func resolve(rev string) (string, error) {
	out, err := Run("rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return "", fmt.Errorf("%s: unknown revision (not a commit, branch or tag here)", rev)
	}
	return strings.TrimSpace(string(out)), nil
}

func emptyTree() string {
	out, err := Run("hash-object", "-t", "tree", "/dev/null")
	if err != nil {
		return "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	}
	return strings.TrimSpace(string(out))
}

// headOrEmpty is HEAD, or the empty tree in a repository without commits.
func headOrEmpty() Side {
	if id, err := resolve("HEAD"); err == nil {
		return Side{Rev: id, Label: "HEAD"}
	}
	return Side{Rev: emptyTree(), Label: "nothing (no commits yet)", Empty: true}
}

// Compare works out the comparison for revs (0, 1 or 2 revisions, or one
// A..B / A...B range) and paths. staged compares the index with HEAD.
func Compare(revs, paths []string, staged bool) (*Comparison, error) {
	c := &Comparison{Paths: paths}
	worktree := Side{Label: "working tree", Worktree: true}
	switch {
	case staged && len(revs) > 1:
		return nil, errors.New("--staged compares the index with one revision (HEAD by default)")
	case staged:
		c.Old = headOrEmpty()
		if len(revs) == 1 {
			id, err := resolve(revs[0])
			if err != nil {
				return nil, err
			}
			c.Old = Side{Rev: id, Label: revs[0]}
		}
		c.New = Side{Rev: ":", Label: "staged", Index: true}
		c.DiffArgs = []string{"--cached", c.Old.Rev}
	case len(revs) == 0:
		c.Old, c.New = headOrEmpty(), worktree
		c.DiffArgs = []string{c.Old.Rev}
	case len(revs) == 1 && strings.Contains(revs[0], ".."):
		three := strings.Contains(revs[0], "...")
		sep := ".."
		if three {
			sep = "..."
		}
		a, b, _ := strings.Cut(revs[0], sep)
		if a == "" {
			a = "HEAD"
		}
		if b == "" {
			b = "HEAD"
		}
		ida, err := resolve(a)
		if err != nil {
			return nil, err
		}
		idb, err := resolve(b)
		if err != nil {
			return nil, err
		}
		c.Old, c.New = Side{Rev: ida, Label: a}, Side{Rev: idb, Label: b}
		if three {
			out, err := Run("merge-base", ida, idb)
			if err != nil {
				return nil, fmt.Errorf("%s and %s have no common ancestor", a, b)
			}
			base := strings.TrimSpace(string(out))
			c.Old = Side{Rev: base, Label: "the branch point"}
			c.Desc = fmt.Sprintf("%s since it branched off %s (at %s)", b, a, base[:min(len(base), 9)])
		}
		c.DiffArgs = []string{c.Old.Rev, c.New.Rev}
	case len(revs) == 1:
		id, err := resolve(revs[0])
		if err != nil {
			return nil, err
		}
		c.Old, c.New = Side{Rev: id, Label: revs[0]}, worktree
		c.DiffArgs = []string{id}
	case len(revs) == 2:
		ida, err := resolve(revs[0])
		if err != nil {
			return nil, err
		}
		idb, err := resolve(revs[1])
		if err != nil {
			return nil, err
		}
		c.Old, c.New = Side{Rev: ida, Label: revs[0]}, Side{Rev: idb, Label: revs[1]}
		c.DiffArgs = []string{ida, idb}
	default:
		return nil, errors.New("too many revisions: give one (compared with the working tree), two, or a range A..B")
	}
	return c, nil
}

// File is one changed file.
type File struct {
	Status     byte   // A, M, D, R, C, T (type change), U (unmerged), ? (untracked)
	OldPath    string // for R and C, the path before
	Path       string
	Similarity int // for R and C
	Added      int
	Deleted    int
	Binary     bool
	OldMode    string
	NewMode    string
	Hunks      []Hunk
}

// Hunk is a run of changed lines, as git diff -U0 reports it.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Removed            []string // the old lines (without the leading "-")
	Added              []string
}

// Files lists the changed files with their counts and hunks.
func Files(c *Comparison) ([]*File, error) {
	common := []string{"diff", "--no-ext-diff", "--no-textconv", "-M"}
	out, err := Run(append(append(append(common, "--raw", "-z", "--numstat"), c.DiffArgs...), pathArgs(c.Paths)...)...)
	if err != nil {
		return nil, err
	}
	files, err := parseRawNumstat(out)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		patch, err := Run(append(append(append(common, "-U0", "--no-color"), c.DiffArgs...), pathArgs(c.Paths)...)...)
		if err != nil {
			return nil, err
		}
		attachHunks(files, patch)
	}
	return files, nil
}

func pathArgs(paths []string) []string {
	return append([]string{"--"}, paths...)
}

// parseRawNumstat reads `git diff --raw -z --numstat` output: first one raw
// record per file (":old new oldsha newsha status\0path\0[path\0]"), then
// one numstat record per file in the same order.
func parseRawNumstat(out []byte) ([]*File, error) {
	fields := strings.Split(string(out), "\x00")
	var files []*File
	i := 0
	for i < len(fields) && strings.HasPrefix(fields[i], ":") {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) < 5 || i+1 >= len(fields) {
			return nil, fmt.Errorf("unexpected git diff --raw output: %q", fields[i])
		}
		f := &File{OldMode: meta[0], NewMode: meta[1], Status: meta[4][0]}
		if len(meta[4]) > 1 {
			f.Similarity, _ = strconv.Atoi(meta[4][1:])
		}
		i++
		f.Path = fields[i]
		i++
		if f.Status == 'R' || f.Status == 'C' {
			f.OldPath, f.Path = f.Path, fields[i]
			i++
		}
		files = append(files, f)
	}
	// numstat: "added\tdeleted\tpath\0", or for renames "added\tdeleted\t\0old\0new\0"
	for k := 0; k < len(files) && i < len(fields); k++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		i++
		if len(parts) < 3 {
			break
		}
		if parts[2] == "" { // rename: two more fields
			i += 2
		}
		if parts[0] == "-" {
			files[k].Binary = true
			continue
		}
		files[k].Added, _ = strconv.Atoi(parts[0])
		files[k].Deleted, _ = strconv.Atoi(parts[1])
	}
	return files, nil
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// attachHunks splits a -U0 patch into its per-file sections. git prints
// them in the same order as the raw records, so sections are matched by
// position rather than by parsing (possibly quoted) header paths.
func attachHunks(files []*File, patch []byte) {
	k := -1
	var h *Hunk
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "diff --git ") || strings.HasPrefix(l, "diff --cc ") {
			k++
			h = nil
			continue
		}
		if k < 0 || k >= len(files) {
			continue
		}
		if m := hunkRe.FindStringSubmatch(l); m != nil {
			files[k].Hunks = append(files[k].Hunks, Hunk{
				OldStart: atoi(m[1]), OldLines: count(m[2]), NewStart: atoi(m[3]), NewLines: count(m[4]),
			})
			h = &files[k].Hunks[len(files[k].Hunks)-1]
			continue
		}
		if h == nil {
			continue
		}
		switch {
		case strings.HasPrefix(l, "-"):
			h.Removed = append(h.Removed, l[1:])
		case strings.HasPrefix(l, "+"):
			h.Added = append(h.Added, l[1:])
		}
	}
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

// Untracked lists files that aren't tracked and aren't ignored.
func Untracked(paths []string) ([]string, error) {
	out, err := Run(append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--full-name"}, pathArgs(paths)...)...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

// Blobs reads file contents in one git process. Requests are "rev:path"
// (":path" for the index). A missing object gives nil.
type Blobs struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func OpenBlobs() (*Blobs, error) {
	cmd := command("cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Blobs{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}, nil
}

func (b *Blobs) Get(spec string) ([]byte, error) {
	if strings.ContainsAny(spec, "\n") {
		return nil, nil
	}
	if _, err := fmt.Fprintln(b.in, spec); err != nil {
		return nil, err
	}
	head, err := b.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(head)
	if len(f) < 3 || f[1] == "missing" || f[1] == "ambiguous" {
		return nil, nil
	}
	size, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %q", head)
	}
	data := make([]byte, size+1) // contents and a trailing newline
	if _, err := io.ReadFull(b.out, data); err != nil {
		return nil, err
	}
	if f[1] != "blob" {
		return nil, nil
	}
	return data[:size], nil
}

func (b *Blobs) Close() {
	b.in.Close()
	b.cmd.Wait()
}

// Content reads a file on one side of a comparison.
func Content(b *Blobs, top string, s Side, path string) ([]byte, error) {
	switch {
	case s.Worktree:
		data, err := os.ReadFile(filepath.Join(top, path))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return data, err
	case s.Index:
		return b.Get(":" + path)
	default:
		return b.Get(s.Rev + ":" + path)
	}
}

// Commit is one entry of git log.
type Commit struct {
	ID, Short, Author, Age, Subject string
	Parents                         []string
}

// Log lists commits like git log (newest first).
func Log(n int, revs, paths []string) ([]Commit, error) {
	args := []string{"log", "--no-color", "-z", "--format=%H%x1f%h%x1f%an%x1f%ar%x1f%P%x1f%s"}
	if n > 0 {
		args = append(args, "-n", strconv.Itoa(n))
	}
	args = append(append(args, revs...), pathArgs(paths)...)
	out, err := Run(args...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(string(out), "\x00") {
		f := strings.Split(strings.TrimPrefix(rec, "\n"), "\x1f")
		if len(f) < 6 {
			continue
		}
		commits = append(commits, Commit{ID: f[0], Short: f[1], Author: f[2], Age: f[3], Parents: strings.Fields(f[4]), Subject: f[5]})
	}
	return commits, nil
}

// CommitComparison compares a commit with its first parent (the empty tree
// for a root commit).
func CommitComparison(c Commit, paths []string) *Comparison {
	old := Side{Rev: emptyTree(), Label: "nothing", Empty: true}
	if len(c.Parents) > 0 {
		old = Side{Rev: c.Parents[0], Label: c.Short + "^"}
	}
	return &Comparison{Old: old, New: Side{Rev: c.ID, Label: c.Short}, DiffArgs: []string{old.Rev, c.ID}, Paths: paths}
}

// FullDiff is git's own patch for the comparison (for --patch and --show).
func FullDiff(c *Comparison, context int, color bool, paths []string) ([]byte, error) {
	args := []string{"diff", "--no-ext-diff", "-M", "-U" + strconv.Itoa(context)}
	if color {
		args = append([]string{"-c", "color.diff=always"}, append(args, "--color=always")...)
	}
	if len(paths) == 0 {
		paths = c.Paths
	}
	return Run(append(append(args, c.DiffArgs...), pathArgs(paths)...)...)
}

// Conflicts lists the paths with unresolved merge conflicts.
func Conflicts(paths []string) map[string]bool {
	out, err := Run(append([]string{"ls-files", "-u", "-z", "--full-name"}, pathArgs(paths)...)...)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if _, p, ok := strings.Cut(rec, "\t"); ok {
			set[p] = true
		}
	}
	return set
}

// DiffContents is git's -U0 diff of two contents (for files git didn't pair).
func DiffContents(old, new []byte) (hunks []Hunk, added, deleted int, err error) {
	dir, err := os.MkdirTemp("", "recap-")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.RemoveAll(dir)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, old, 0o600); err != nil {
		return nil, 0, 0, err
	}
	if err := os.WriteFile(b, new, 0o600); err != nil {
		return nil, 0, 0, err
	}
	// --no-index exits 1 when the files differ; that's not a failure here
	cmd := command("diff", "--no-index", "--no-ext-diff", "--no-color", "-U0", a, b)
	patch, _ := cmd.Output()
	f := []*File{{}}
	attachHunks(f, patch)
	for _, h := range f[0].Hunks {
		added += len(h.Added)
		deleted += len(h.Removed)
	}
	return f[0].Hunks, added, deleted, nil
}

// MergeHead names the branch or commit being merged, if a merge is in progress.
func MergeHead() string {
	out, err := Run("rev-parse", "-q", "--verify", "MERGE_HEAD")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	id := strings.TrimSpace(string(out))
	if name, err := Run("name-rev", "--name-only", "--no-undefined", id); err == nil {
		if n := strings.TrimSpace(string(name)); n != "" {
			return n
		}
	}
	return id[:min(len(id), 9)]
}

// Message is a commit's full message.
func Message(rev string) (string, error) {
	out, err := Run("log", "-1", "--format=%B", rev, "--")
	return strings.TrimRight(string(out), "\n"), err
}

// ShowCommit resolves rev for `recap show`.
func ShowCommit(rev string) (Commit, error) {
	id, err := resolve(rev)
	if err != nil {
		return Commit{}, err
	}
	cs, err := Log(1, []string{id}, nil)
	if err != nil || len(cs) == 0 {
		return Commit{}, fmt.Errorf("%s: no such commit", rev)
	}
	return cs[0], nil
}

// HasCommits reports whether HEAD points at a commit.
func HasCommits() bool {
	_, err := resolve("HEAD")
	return err == nil
}
