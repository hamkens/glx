package tui

import (
	"strconv"
	"strings"
)

// diffLineKind classifies a single rendered diff line.
type diffLineKind int

const (
	lineContext diffLineKind = iota
	lineAdd
	lineDel
	lineHunk // @@ ... @@ header
	lineMeta // file header noise we keep for context
)

// diffLine is one displayable row of a file diff, carrying the line numbers
// needed to anchor an inline comment on the correct side.
type diffLine struct {
	kind    diffLineKind
	text    string // content without the leading +/-/space marker
	oldLine int    // line number on the old side, 0 if N/A
	newLine int    // line number on the new side, 0 if N/A
}

// parseUnifiedDiff turns GitLab's per-file unified diff into diffLines with
// resolved old/new line numbers, tracking position through each @@ hunk.
func parseUnifiedDiff(diff string) []diffLine {
	var out []diffLine
	var oldNo, newNo int

	for _, raw := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			o, n := parseHunkHeader(raw)
			oldNo, newNo = o, n
			out = append(out, diffLine{kind: lineHunk, text: raw})
		case strings.HasPrefix(raw, "+"):
			out = append(out, diffLine{kind: lineAdd, text: raw[1:], newLine: newNo})
			newNo++
		case strings.HasPrefix(raw, "-"):
			out = append(out, diffLine{kind: lineDel, text: raw[1:], oldLine: oldNo})
			oldNo++
		case strings.HasPrefix(raw, " "):
			out = append(out, diffLine{kind: lineContext, text: raw[1:], oldLine: oldNo, newLine: newNo})
			oldNo++
			newNo++
		case raw == "":
			// Trailing newline split; skip empty tail lines.
		default:
			// "\ No newline at end of file" and similar.
			out = append(out, diffLine{kind: lineMeta, text: raw})
		}
	}
	return out
}

// parseHunkHeader extracts the starting old and new line numbers from a header
// like "@@ -8,6 +8,7 @@ optional context".
func parseHunkHeader(h string) (oldStart, newStart int) {
	// Strip leading "@@ " and take up to the closing " @@".
	body := h
	if i := strings.Index(body, "@@"); i >= 0 {
		body = body[i+2:]
	}
	if i := strings.Index(body, "@@"); i >= 0 {
		body = body[:i]
	}
	for _, tok := range strings.Fields(body) {
		switch {
		case strings.HasPrefix(tok, "-"):
			oldStart = leadingInt(tok[1:])
		case strings.HasPrefix(tok, "+"):
			newStart = leadingInt(tok[1:])
		}
	}
	return oldStart, newStart
}

// leadingInt parses the number before an optional ",count" suffix.
func leadingInt(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(s)
	return n
}
