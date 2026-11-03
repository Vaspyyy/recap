#!/usr/bin/env python3
"""Build an anonymous demo repository for recap's README previews."""
import os, shutil, subprocess, textwrap

ROOT = "/var/tmp/recap-demo/home"
R = os.path.join(ROOT, "projects", "ledger")
shutil.rmtree("/var/tmp/recap-demo", ignore_errors=True)
os.makedirs(R)
T = [1767225600]


def git(*args):
    T[0] += 86400
    env = dict(os.environ, GIT_AUTHOR_NAME="Ada Lovelace", GIT_AUTHOR_EMAIL="ada@example.com",
               GIT_COMMITTER_NAME="Ada Lovelace", GIT_COMMITTER_EMAIL="ada@example.com",
               GIT_AUTHOR_DATE=f"@{T[0]} +0000", GIT_COMMITTER_DATE=f"@{T[0]} +0000", GIT_CONFIG_GLOBAL="/dev/null")
    subprocess.run(["git", *args], cwd=R, env=env, check=True, capture_output=True)


def w(name, text):
    p = os.path.join(R, name)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(textwrap.dedent(text).lstrip("\n"))


def lock(version):
    return "\n".join(f"example.com/dep{i} v1.{version}.{i} h1:{'ab' * 20}{i:04d}=" for i in range(60)) + "\n"


git("init", "-q", "-b", "main")
w("go.mod", "module example.com/ledger\n\ngo 1.26\n")
w("go.sum", lock(2))
w("README.md", """
    # ledger

    Money amounts in integer cents.

    ## Usage

    Parse and format amounts with `ParseCents` and `FormatCents`.

    ## Rounding

    Amounts are truncated to whole cents.
""")
w("money.go", """
    package ledger

    import (
    	"strconv"
    	"strings"
    )

    // ParseCents reads "12.34" as 1234.
    func ParseCents(s string) (int64, error) {
    	s = strings.TrimSpace(s)
    	neg := strings.HasPrefix(s, "-")
    	s = strings.TrimPrefix(s, "-")
    	whole, frac, _ := strings.Cut(s, ".")
    	frac = (frac + "00")[:2]
    	w, err := strconv.ParseInt(whole, 10, 64)
    	if err != nil {
    		return 0, err
    	}
    	f, err := strconv.ParseInt(frac, 10, 64)
    	if err != nil {
    		return 0, err
    	}
    	v := w*100 + f
    	if neg {
    		v = -v
    	}
    	return v, nil
    }

    // FormatCents writes 1234 as "12.34".
    func FormatCents(c int64) string {
    	sign := ""
    	if c < 0 {
    		sign, c = "-", -c
    	}
    	return sign + strconv.FormatInt(c/100, 10) + "." + pad(c%100)
    }

    func pad(n int64) string {
    	if n < 10 {
    		return "0" + strconv.FormatInt(n, 10)
    	}
    	return strconv.FormatInt(n, 10)
    }

    // Split divides total into n parts that differ by at most one cent.
    func Split(total int64, n int) []int64 {
    	parts := make([]int64, n)
    	base, rest := total/int64(n), total%int64(n)
    	for i := range parts {
    		parts[i] = base
    		if int64(i) < rest {
    			parts[i]++
    		}
    	}
    	return parts
    }

    // ApplyRate applies a rate in basis points.
    func ApplyRate(c int64, bp int64) int64 {
    	return c * bp / 10000
    }
""")
git("add", "-A")
git("commit", "-q", "-m", "Money amounts in cents")

# the working-tree refactor: an extracted helper with one changed line,
# a rename, a signature change, a function moved to a new file
w("money.go", """
    package ledger

    import (
    	"fmt"
    	"strconv"
    	"strings"
    )

    // ParseCents reads "12.34" as 1234.
    func ParseCents(s string) (int64, error) {
    	return parseAmount(strings.TrimSpace(s))
    }

    func parseAmount(s string) (int64, error) {
    	neg := strings.HasPrefix(s, "-")
    	s = strings.TrimPrefix(s, "-")
    	whole, frac, _ := strings.Cut(s, ".")
    	frac = (frac + "0")[:2]
    	w, err := strconv.ParseInt(whole, 10, 64)
    	if err != nil {
    		return 0, err
    	}
    	f, err := strconv.ParseInt(frac, 10, 64)
    	if err != nil {
    		return 0, err
    	}
    	v := w*100 + f
    	if neg {
    		v = -v
    	}
    	return v, nil
    }

    // Format writes 1234 as "12.34 EUR".
    func Format(c int64, currency string) string {
    	sign := ""
    	if c < 0 {
    		sign, c = "-", -c
    	}
    	return fmt.Sprintf("%s%d.%02d %s", sign, c/100, c%100, currency)
    }

    // ApplyRate applies a rate in basis points, rounding half up.
    func ApplyRate(c int64, bp int64) int64 {
    	return (c*bp + 5000) / 10000
    }
""")
w("split.go", """
    package ledger

    // Split divides total into n parts that differ by at most one cent.
    func Split(total int64, n int) []int64 {
    	if n <= 0 {
    		return nil
    	}
    	parts := make([]int64, n)
    	base, rest := total/int64(n), total%int64(n)
    	for i := range parts {
    		parts[i] = base
    		if int64(i) < rest {
    			parts[i]++
    		}
    	}
    	return parts
    }
""")
w("README.md", """
    # ledger

    Money amounts in integer cents.

    ## Usage

    Parse and format amounts with `ParseCents` and `Format`.

    ## Rounding

    Rates round half up to whole cents.
""")
w("go.sum", lock(3))
git("add", "split.go")  # tracked, so git sees it as new
print("demo repository ready:", R)
