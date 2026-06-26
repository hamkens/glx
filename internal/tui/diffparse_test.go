package tui

import "testing"

func TestParseUnifiedDiff_LineNumbers(t *testing.T) {
	// A hunk starting at old line 8, new line 8: one context, one addition,
	// then more context. New side gains a line, so it runs ahead.
	diff := "@@ -8,6 +8,7 @@ from typing import Any\n" +
		" from airflow.models import BaseOperator\n" +
		"+from airflow.utils.types import DagRunType\n" +
		" from airflow.utils.state import State\n" +
		"-old_removed_line\n" +
		" trailing context\n"

	lines := parseUnifiedDiff(diff)

	// Expect: hunk, context(8/8), add(new 9), context(9/10), del(old 10), context(11/11)
	type want struct {
		kind     diffLineKind
		old, new int
	}
	wants := []want{
		{lineHunk, 0, 0},
		{lineContext, 8, 8},
		{lineAdd, 0, 9},
		{lineContext, 9, 10},
		{lineDel, 10, 0},
		{lineContext, 11, 11},
	}
	if len(lines) != len(wants) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(wants), lines)
	}
	for i, w := range wants {
		got := lines[i]
		if got.kind != w.kind || got.oldLine != w.old || got.newLine != w.new {
			t.Errorf("line %d: got kind=%d old=%d new=%d; want kind=%d old=%d new=%d",
				i, got.kind, got.oldLine, got.newLine, w.kind, w.old, w.new)
		}
	}
}

func TestParseHunkHeader(t *testing.T) {
	cases := map[string][2]int{
		"@@ -8,6 +8,7 @@ ctx":      {8, 8},
		"@@ -1 +1 @@":              {1, 1},
		"@@ -0,0 +1,5 @@ new file": {0, 1},
		"@@ -120,3 +118,9 @@":      {120, 118},
	}
	for h, want := range cases {
		o, n := parseHunkHeader(h)
		if o != want[0] || n != want[1] {
			t.Errorf("%q: got old=%d new=%d; want old=%d new=%d", h, o, n, want[0], want[1])
		}
	}
}
