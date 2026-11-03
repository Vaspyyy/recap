package analyze

// Op is one line of a diff: ' ' kept, '-' removed, '+' added.
type Op struct {
	Kind byte
	Text string
}

// maxCells bounds the work of LineDiff (lines of a times lines of b).
const maxCells = 16 << 20

// LineDiff is a minimal line diff of a and b (longest common subsequence).
// It returns nil when the texts are too large to compare this way.
func LineDiff(a, b []string) []Op {
	// common prefix and suffix first: most symbols change in a few places
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma)*len(mb) > maxCells {
		return nil
	}
	var ops []Op
	for _, l := range a[:pre] {
		ops = append(ops, Op{' ', l})
	}
	// lcs[i][j]: length of the LCS of ma[i:] and mb[j:]
	n, m := len(ma), len(mb)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && ma[i] == mb[j]:
			ops = append(ops, Op{' ', ma[i]})
			i++
			j++
		case j == m || (i < n && lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, Op{'-', ma[i]}) // removals before additions, like git
			i++
		default:
			ops = append(ops, Op{'+', mb[j]})
			j++
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, Op{' ', l})
	}
	return ops
}

// Counts returns the added and removed lines of a diff.
func Counts(ops []Op) (plus, minus int) {
	for _, o := range ops {
		switch o.Kind {
		case '+':
			plus++
		case '-':
			minus++
		}
	}
	return
}

// Context keeps the changed lines with n unchanged lines around them; the
// first line (it names the symbol) is always kept. Left-out stretches
// become one "⋮" line.
func Context(ops []Op, n int) []string {
	var out []string
	near := func(i int) bool {
		for d := -n; d <= n; d++ {
			if k := i + d; k >= 0 && k < len(ops) && ops[k].Kind != ' ' {
				return true
			}
		}
		return false
	}
	for i, o := range ops {
		switch {
		case o.Kind != ' ', i == 0, near(i):
			out = append(out, string(o.Kind)+o.Text)
		case len(out) == 0 || out[len(out)-1] != "  ⋮":
			out = append(out, "  ⋮")
		}
	}
	return out
}
