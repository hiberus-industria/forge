package cli

import "testing"

func TestLCSDiff(t *testing.T) {
	ops := lcsDiff(splitLines("a\nb\nc\n"), splitLines("a\nx\nc\nd\n"))
	got := ""
	for _, o := range ops {
		got += string(o.kind) + o.line + "|"
	}
	if got != " a|-b|+x| c|+d|" {
		t.Fatal(got)
	}
}
