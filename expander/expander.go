// Package expander resolves variable references in parsed AST word parts.
package expander

import (
	"path/filepath"
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
			text := pt.Text
			if e.DelayedExpansion {
				text = expandBangs(text, e)
			}
			sb.WriteString(text)
		case *parser.VarPart:
			if pt.Positional >= 0 {
				if pt.Positional < len(positional) {
					sb.WriteString(positional[pt.Positional])
				}
			} else {
				sb.WriteString(e.Get(pt.Name))
			}
		case *parser.DelayedVarPart:
			sb.WriteString(expandDelayedRef(pt.Name, e))
		case *parser.TildeVarPart:
			sb.WriteString(expandTilde(pt, positional))
		case *parser.SubstringVarPart:
			sb.WriteString(expandSubstring(pt, e))
		}
	}
	return sb.String()
}

// ExpandArgs expands a slice of WordPart slices into individual strings.
func ExpandArgs(argParts [][]parser.WordPart, e *env.Env, positional []string) []string {
	result := make([]string, len(argParts))
	for i, parts := range argParts {
		result[i] = ExpandWord(parts, e, positional)
	}
	return result
}

// expandTilde handles %~1, %~dp0, %~n1, etc.
func expandTilde(pt *parser.TildeVarPart, positional []string) string {
	val := ""
	if pt.Positional < len(positional) {
		val = positional[pt.Positional]
	}

	// No modifiers: just strip surrounding quotes
	if pt.Modifiers == "" {
		return stripQuotes(val)
	}

	result := ""
	mods := strings.ToLower(pt.Modifiers)

	// f = full path
	if strings.Contains(mods, "f") {
		abs, err := filepath.Abs(stripQuotes(val))
		if err == nil {
			return abs
		}
		return stripQuotes(val)
	}

	path := stripQuotes(val)

	// d = drive (on Unix, always empty or /)
	if strings.Contains(mods, "d") {
		if filepath.IsAbs(path) {
			result += "/"
		}
	}

	// p = path (directory part)
	if strings.Contains(mods, "p") {
		result += filepath.Dir(path)
		if !strings.HasSuffix(result, "/") {
			result += "/"
		}
	}

	// n = file name without extension
	if strings.Contains(mods, "n") {
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		result += strings.TrimSuffix(base, ext)
	}

	// x = extension only
	if strings.Contains(mods, "x") {
		result += filepath.Ext(path)
	}

	// If no recognized modifiers, just strip quotes
	if result == "" {
		return stripQuotes(val)
	}

	return result
}

// expandSubstring handles %VAR:~N% and %VAR:~N,M%.
func expandSubstring(pt *parser.SubstringVarPart, e *env.Env) string {
	val := e.Get(pt.Name)
	if val == "" {
		return ""
	}

	start := pt.Start
	n := len(val)

	// Negative start: count from end
	if start < 0 {
		start = n + start
		if start < 0 {
			start = 0
		}
	}
	if start > n {
		return ""
	}

	if !pt.HasLength {
		return val[start:]
	}

	length := pt.Length
	if length < 0 {
		// Negative length: omit last |length| chars
		end := n + length
		if end <= start {
			return ""
		}
		return val[start:end]
	}

	end := start + length
	if end > n {
		end = n
	}
	return val[start:end]
}

// expandDelayedRef resolves a delayed variable reference which may contain
// substring (:~N,M) or replacement (:old=new) modifiers.
func expandDelayedRef(name string, e *env.Env) string {
	if colonIdx := strings.Index(name, ":~"); colonIdx != -1 {
		varName := name[:colonIdx]
		spec := name[colonIdx+2:]
		return substringExpand(e.Get(varName), spec)
	}
	if colonIdx := strings.IndexByte(name, ':'); colonIdx != -1 {
		eqIdx := strings.IndexByte(name[colonIdx+1:], '=')
		if eqIdx != -1 {
			varName := name[:colonIdx]
			old := name[colonIdx+1 : colonIdx+1+eqIdx]
			newStr := name[colonIdx+1+eqIdx+1:]
			return strings.ReplaceAll(e.Get(varName), old, newStr)
		}
	}
	return e.Get(name)
}

// ExpandBangs expands !VAR! patterns in a string (delayed expansion).
func ExpandBangs(s string, e *env.Env) string {
	return expandBangs(s, e)
}

func expandBangs(s string, e *env.Env) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '!' {
			sb.WriteByte(s[i])
			i++
			continue
		}
		// Find closing !
		j := strings.IndexByte(s[i+1:], '!')
		if j == -1 {
			sb.WriteByte(s[i])
			i++
			continue
		}
		name := s[i+1 : i+1+j]
		if name == "" {
			sb.WriteByte('!')
			i += 2
			continue
		}

		// !VAR:~N,M! — substring
		if colonIdx := strings.Index(name, ":~"); colonIdx != -1 {
			varName := name[:colonIdx]
			spec := name[colonIdx+2:]
			val := e.Get(varName)
			sb.WriteString(substringExpand(val, spec))
			i += j + 2
			continue
		}

		// !VAR:old=new! — string replacement
		if colonIdx := strings.IndexByte(name, ':'); colonIdx != -1 {
			eqIdx := strings.IndexByte(name[colonIdx+1:], '=')
			if eqIdx != -1 {
				varName := name[:colonIdx]
				old := name[colonIdx+1 : colonIdx+1+eqIdx]
				newStr := name[colonIdx+1+eqIdx+1:]
				val := e.Get(varName)
				sb.WriteString(strings.ReplaceAll(val, old, newStr))
				i += j + 2
				continue
			}
		}

		sb.WriteString(e.Get(name))
		i += j + 2
	}
	return sb.String()
}

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
