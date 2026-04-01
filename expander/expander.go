// Package expander resolves variable references in parsed AST word parts.
package expander

import (
	"strings"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/parser"
)

// ExpandWord resolves all WordParts into a single string using the environment.
func ExpandWord(parts []parser.WordPart, e *env.Env, positional []string) string {
	var sb strings.Builder
	for _, p := range parts {
		switch pt := p.(type) {
		case *parser.LiteralPart:
			sb.WriteString(pt.Text)
		case *parser.VarPart:
			if pt.Positional >= 0 {
				if pt.Positional < len(positional) {
					sb.WriteString(positional[pt.Positional])
				}
			} else {
				sb.WriteString(e.Get(pt.Name))
			}
		}
	}
	return sb.String()
}

// ExpandWords expands a slice of WordPart slices into individual string arguments.
// Each contiguous group of parts is one argument.
func ExpandArgs(argParts [][]parser.WordPart, e *env.Env, positional []string) []string {
	result := make([]string, len(argParts))
	for i, parts := range argParts {
		result[i] = ExpandWord(parts, e, positional)
	}
	return result
}
