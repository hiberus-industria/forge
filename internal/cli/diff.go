package cli

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderDiff returns a colored unified-style diff of two texts with three
// lines of context. Config files are small, so an O(n·m) LCS is fine.
func renderDiff(path, before, after string) string {
	a, b := splitLines(before), splitLines(after)
	ops := lcsDiff(a, b)
	var sb strings.Builder
	head := lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	add := lipgloss.NewStyle().Foreground(cOK)
	del := lipgloss.NewStyle().Foreground(cErr)
	ctx := lipgloss.NewStyle().Foreground(cMuted)
	status := "modificado"
	switch {
	case before == "":
		status = "nuevo"
	case after == "":
		status = "eliminado"
	}
	sb.WriteString(head.Render(fmt.Sprintf("── %s (%s)", path, status)) + "\n")

	const context = 3
	show := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind != ' ' {
			for j := max(0, i-context); j <= min(len(ops)-1, i+context); j++ {
				show[j] = true
			}
		}
	}
	skipped := false
	for i, op := range ops {
		if !show[i] {
			skipped = true
			continue
		}
		if skipped {
			sb.WriteString(ctx.Render("   ⋯") + "\n")
			skipped = false
		}
		switch op.kind {
		case '+':
			sb.WriteString(add.Render("+ "+op.line) + "\n")
		case '-':
			sb.WriteString(del.Render("- "+op.line) + "\n")
		default:
			sb.WriteString(ctx.Render("  "+op.line) + "\n")
		}
	}
	return sb.String()
}

type diffOp struct {
	kind byte
	line string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func lcsDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}
