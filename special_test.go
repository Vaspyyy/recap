//go:build unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Vaspyyy/prism/report"
)

// recapWithin is recap with a time limit. A regression that reads a named
// pipe blocks (rather than filling memory, as /dev/zero would), and on the
// limit the pipes are opened for writing, so a blocked reader gets EOF and
// the run ends before the test fails.
func recapWithin(t *testing.T, pipes []string, args ...string) (string, int) {
	t.Helper()
	type result struct {
		out  string
		code int
	}
	done := make(chan result, 1)
	go func() {
		out, _, code := recapAll(t, args...)
		done <- result{out, code}
	}()
	select {
	case r := <-done:
		return r.out, r.code
	case <-time.After(20 * time.Second):
		for _, p := range pipes {
			if w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
		<-done
		t.Fatalf("recap %v hangs on a named pipe", args)
	}
	return "", 0
}

func mkfifo(t *testing.T, p string) {
	t.Helper()
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, p string) {
	t.Helper()
	os.Remove(p)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// recap reads the working tree as git sees it: a symlink is its target's
// path, never followed, and what git can't read (a named pipe, a socket, a
// device) is never opened.
func TestSpecialFiles(t *testing.T) {
	write, commit := repo(t)
	write("a.go", "package a\n\nfunc A() int {\n\treturn 1\n}\n")
	write("b.txt", "two\n")
	write("c.txt", "c\n")
	write("d.txt", "four\n")
	write("e.txt", "e\n")
	if err := os.Symlink("a.go", "link"); err != nil {
		t.Fatal(err)
	}
	commit("first")
	outside := t.TempDir()
	pipe := filepath.Join(outside, "pipe")
	mkfifo(t, pipe)
	// a pipe in the repository too, for a short link target: the overview
	// clips inline lines at 80 characters, and macOS's temp paths are long
	// (an untracked pipe isn't listed, as git doesn't list it)
	mkfifo(t, "p")
	pipes := []string{pipe, "p", "b.txt", "upipe"}

	// named pipes first, one case at a time: a regression blocks on them
	// (and the test stops) instead of filling memory on /dev/zero below
	symlink(t, pipe, "ulink") // an untracked symlink to a pipe
	out, code := recapWithin(t, pipes)
	if code != 0 || !strings.HasSuffix(out, "\nulink  untracked, 1 line, symlink\n") {
		t.Errorf("an untracked symlink is its target's path: exit %d\n%s", code, out)
	}
	symlink(t, "p", "e.txt") // a tracked file that is a symlink to a pipe now
	out, code = recapWithin(t, pipes)
	if want := "\ne.txt  type changed (file, symlink or submodule), +1 −1, symlink: -e  +p\n"; code != 0 || !strings.Contains(out, want) {
		t.Errorf("a tracked file that became a symlink: exit %d, missing %q in\n%s", code, want, out)
	}
	os.Remove("b.txt") // a tracked file that is a pipe now
	mkfifo(t, "b.txt")
	mkfifo(t, "upipe") // an untracked pipe: git doesn't list it
	out, code = recapWithin(t, pipes)
	for _, want := range []string{
		"recap: HEAD → working tree: 2 files changed, +1 −2, 1 untracked\n",
		"\nb.txt  deleted, 1 line, now a named pipe, which git can't diff or add\n",
		"\ne.txt  type changed (file, symlink or submodule), +1 −1, symlink: -e  +p\n",
		"\nulink  untracked, 1 line, symlink\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d, missing %q in\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "upipe") {
		t.Errorf("an untracked pipe isn't listed, as git doesn't:\n%s", out)
	}
	if out, code := recapWithin(t, pipes, "--show", "ulink"); code != 0 || !strings.HasSuffix(out, "\n+"+pipe+"\n") {
		t.Errorf("--show ulink: exit %d\n%s", code, out)
	}
	if out, code := recapWithin(t, pipes, "-p"); code != 0 || !strings.Contains(out, "diff --git a/e.txt b/e.txt") || strings.Contains(out, "diff --git a/b.txt") {
		t.Errorf("-p: git's patch, without the pipe it can't diff: exit %d\n%s", code, out)
	}
	jsonOut, code := recapWithin(t, pipes, "--json")
	d, err := report.Check([]byte(jsonOut))
	if err != nil || code != 0 {
		t.Fatalf("--json: exit %d, %v\n%s", code, err, jsonOut)
	}
	var res report.Recap
	if err := json.Unmarshal(d.Result, &res); err != nil {
		t.Fatal(err)
	}
	if b := fileByPath(t, res.Files, "b.txt"); b.Status != "deleted" || b.Removed != 1 {
		t.Errorf("JSON b.txt: %+v", b)
	}
	if e := fileByPath(t, res.Files, "e.txt"); e.Status != "type changed" || !e.Symlink || e.Added != 1 || e.Removed != 1 {
		t.Errorf("JSON e.txt: %+v", e)
	}
	if u := fileByPath(t, res.Files, "ulink"); u.Status != "untracked" || !u.Symlink || u.Added != 1 {
		t.Errorf("JSON ulink: %+v", u)
	}
	if !contains(res.Notes, "b.txt: now a named pipe, which git can't diff or add; listed as deleted") {
		t.Errorf("JSON notes %q", res.Notes)
	}

	// a symlink to a device that never ends, a changed symlink, and a
	// change after a type change (git prints a type change as two sections)
	symlink(t, "/dev/zero", "c.txt")
	write("d.txt", "five\n")
	symlink(t, "elsewhere.txt", "link")
	out, code = recapWithin(t, pipes)
	for _, want := range []string{
		"\nc.txt  type changed (file, symlink or submodule), +1 −1, symlink: -c  +/dev/zero\n",
		"\nd.txt  modified, +1 −1: -four  +five\n",
		"\nlink  modified, +1 −1, symlink: -a.go  +elsewhere.txt\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d, missing %q in\n%s", code, want, out)
		}
	}
	if out, _ := recapWithin(t, pipes, "--show", "link"); out != "link  (+1 −1)\n-a.go\n+elsewhere.txt\n" {
		t.Errorf("--show link: the old target, then the new one:\n%s", out)
	}
	jsonOut, _ = recapWithin(t, pipes, "--json", "--show", "c.txt")
	if d, err := report.Check([]byte(jsonOut)); err != nil || json.Unmarshal(d.Result, &res) != nil || len(res.Show) != 1 ||
		strings.Join(res.Show[0].Diff, "|") != "-c|+/dev/zero" {
		t.Errorf("--json --show c.txt: %v\n%s", err, jsonOut)
	}
	jsonOut, _ = recapWithin(t, pipes, "--json")
	if d, err := report.Check([]byte(jsonOut)); err != nil || json.Unmarshal(d.Result, &res) != nil {
		t.Fatalf("--json: %v\n%s", err, jsonOut)
	}
	if l := fileByPath(t, res.Files, "link"); l.Status != "modified" || !l.Symlink || l.Added != 1 || l.Removed != 1 {
		t.Errorf("JSON link: %+v", l)
	}
	if c := fileByPath(t, res.Files, "c.txt"); c.Status != "type changed" || !c.Symlink {
		t.Errorf("JSON c.txt: %+v", c)
	}
}

// gitIn runs git in dir, with the test's identity and no user config.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The user's git config can't change the patch recap parses:
// diff.submodule=log prints a submodule without a "diff --git" header, and
// the files after it got the wrong changed lines.
func TestSubmodulesWithUserConfig(t *testing.T) {
	write, commit := repo(t)
	z := "package z\n\nfunc Z() int {\n\treturn 1\n}\n"
	write("sub", "a file first\n")
	write("z.go", z)
	commit("first")
	os.Remove("sub") // the file becomes a submodule
	gitIn(t, ".", "init", "-q", "-b", "main", "sub")
	write("sub/s.txt", "s\n")
	gitIn(t, "sub", "add", "-A")
	gitIn(t, "sub", "commit", "-q", "-m", "s")
	write("z.go", strings.Replace(z, "return 1", "return 2", 1))
	commit("submodule")
	gitIn(t, ".", "config", "diff.submodule", "log")
	gitIn(t, ".", "config", "diff.interHunkContext", "5")
	out, code := recap(t, "HEAD~1", "HEAD")
	if code != 0 || !strings.Contains(out, "\nz.go  modified, +1 −1\n  changed  func Z  L3-5  +1 −1\n") {
		t.Errorf("a file that became a submodule: exit %d\n%s", code, out)
	}
	write("sub/s.txt", "t\n") // the submodule moves on
	gitIn(t, "sub", "commit", "-q", "-am", "t")
	write("z.go", strings.Replace(z, "return 1", "return 3", 1))
	commit("submodule moves")
	if out, code := recap(t, "HEAD~1", "HEAD"); code != 0 || !strings.Contains(out, "\nz.go  modified, +1 −1\n  changed  func Z  L3-5  +1 −1\n") {
		t.Errorf("a changed submodule: exit %d\n%s", code, out)
	}
}

// A special file at a path the old side never had isn't listed: there's
// nothing to delete. And the user's environment can't change what recap's
// pathspecs mean.
func TestSpecialFilesAtNewPaths(t *testing.T) {
	write, commit := repo(t)
	write("a.go", "package a\n\nfunc A() int {\n\treturn 1\n}\n")
	write("old.txt", "old\n")
	commit("first")
	write("later.txt", "later\n")
	commit("second")
	pipes := []string{"added.txt", "new.txt", "later.txt"}

	write("added.txt", "new\n") // staged, then a pipe
	gitIn(t, ".", "add", "added.txt")
	os.Remove("added.txt")
	mkfifo(t, "added.txt")
	gitIn(t, ".", "mv", "old.txt", "new.txt") // a staged rename whose new path is a pipe
	os.Remove("new.txt")
	mkfifo(t, "new.txt")
	write("a.go", "package a\n\nfunc A() int {\n\treturn 2\n}\n")
	out, code := recapWithin(t, pipes)
	if code != 0 || strings.Contains(out, "added.txt") || strings.Contains(out, "new.txt") ||
		!strings.HasPrefix(out, "recap: HEAD → working tree: 2 files changed, +1 −2\n") ||
		!strings.Contains(out, "\nold.txt  deleted, 1 line\n") || !strings.Contains(out, "\n  changed  func A  L3-5  +1 −1\n") {
		t.Errorf("paths HEAD didn't have aren't listed: exit %d\n%s", code, out)
	}

	os.Remove("later.txt") // committed after HEAD~1, a pipe now
	mkfifo(t, "later.txt")
	if out, code := recapWithin(t, pipes, "HEAD~1"); code != 0 || strings.Contains(out, "later.txt") {
		t.Errorf("recap HEAD~1: later.txt wasn't there in HEAD~1: exit %d\n%s", code, out)
	}
	if out, _ := recapWithin(t, pipes); !strings.Contains(out, "\nlater.txt  deleted, 1 line, now a named pipe, which git can't diff or add\n") {
		t.Errorf("recap: HEAD had later.txt:\n%s", out)
	}

	t.Setenv("GIT_LITERAL_PATHSPECS", "1")
	if out, code := recapWithin(t, pipes); code != 0 || !strings.Contains(out, "\n  changed  func A  L3-5  +1 −1\n") || !strings.Contains(out, "\nold.txt  deleted") {
		t.Errorf("GIT_LITERAL_PATHSPECS=1 hides the other changes: exit %d\n%s", code, out)
	}
}
