package tui

import (
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// highlightLine syntax-highlights a single line of source for the given file
// path using chroma's 256-color terminal formatter. On any failure it returns
// the input unchanged, so highlighting is always best-effort.
func highlightLine(path, line string) string {
	if line == "" {
		return line
	}
	lexer := lexers.Match(path)
	if lexer == nil {
		return line
	}
	style := styles.Get("github-dark")
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return line
	}
	it, err := lexer.Tokenise(nil, line)
	if err != nil {
		return line
	}
	var sb strings.Builder
	if err := formatter.Format(&sb, style, it); err != nil {
		return line
	}
	return strings.TrimRight(sb.String(), "\n")
}
