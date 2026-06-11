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
	if dbg := false; dbg {
		// Hidden debug; enable by setting dbg true
		_ = val
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
	// Resolve to absolute path so d/p modifiers return useful paths,
	// matching Windows cmd.exe behavior where %~dp0 always gives full dir.
	absPath := path
	if abs, err := filepath.Abs(path); err == nil {
		absPath = abs
	}

	// d = drive (on Unix, always / for absolute paths)
	if strings.Contains(mods, "d") {
		if filepath.IsAbs(absPath) {
			result += "/"
		}
	}

	// p = path (directory part of the absolute path)
	if strings.Contains(mods, "p") {
		dir := filepath.Dir(absPath)
		// If d was already added "/", don't double the leading slash
		if strings.Contains(mods, "d") && strings.HasPrefix(dir, "/") {
			dir = strings.TrimPrefix(dir, "/")
		}
		result += dir
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
// The name may contain %VAR%, %%i, or !VAR! references that must be expanded
// first (e.g. !_s:~%%i,1! inside a FOR body needs %%i replaced with the FOR var).
func expandDelayedRef(name string, e *env.Env) string {
	return resolveDelayedRef(name, e)
}

// resolveDelayedRef resolves a single delayed-expansion reference's inner name
// (the text between the !...!) to its value, applying cmd.exe semantics:
//   - %%i / %VAR% nested refs are resolved first (FOR-body case)
//   - VAR:~N,M  → substring
//   - VAR:old=new → string replacement
//   - VAR=       → cmd.exe quirk: value with one leading '=' stripped iff it
//                  starts with '=', else empty. gw-batsic's char→hex table
//                  relies on this to encode the '=' character.
//   - VAR        → plain lookup
func resolveDelayedRef(name string, e *env.Env) string {
	name = expandNestedPercents(name, e)
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
	// cmd.exe quirk: `!VAR=!` (trailing '=', no colon) yields VAR's value with
	// one leading '=' removed if it starts with '=', otherwise empty.
	if len(name) > 1 && name[len(name)-1] == '=' && !strings.Contains(name, ":") {
		val := e.Get(name[:len(name)-1])
		if strings.HasPrefix(val, "=") {
			return val[1:]
		}
		return ""
	}
	return e.Get(name)
}

// ExpandName resolves a variable name that may itself contain %%X (FOR var),
// %VAR%, or !VAR! references — e.g. the target of `set "%%a=..."` in a FOR body.
func ExpandName(s string, e *env.Env) string {
	if e.DelayedExpansion {
		s = expandBangs(s, e)
	}
	return expandNestedPercents(s, e)
}

// ExpandForVars resolves %%X (FOR variables) and %VAR% references in a string.
// Used for FOR /IN list items like `for %%p in (%%j)` where the inner list
// references the outer loop's variable.
func ExpandForVars(s string, e *env.Env) string {
	return expandNestedPercents(s, e)
}

// expandNestedPercents resolves %% (FOR var) and %VAR% inside a delayed-ref name.
func expandNestedPercents(s string, e *env.Env) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '%' {
			sb.WriteByte(s[i])
			i++
			continue
		}
		// %% followed by single letter — FOR variable: look up that letter as env var
		if i+1 < len(s) && s[i+1] == '%' {
			if i+2 < len(s) {
				ch := s[i+2]
				if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
					sb.WriteString(e.Get(string(ch)))
					i += 3
					continue
				}
			}
			// Plain %% → emit as is
			sb.WriteByte('%')
			sb.WriteByte('%')
			i += 2
			continue
		}
		// %VAR% — find closing %
		closeIdx := strings.IndexByte(s[i+1:], '%')
		if closeIdx == -1 {
			sb.WriteByte('%')
			i++
			continue
		}
		closeIdx += i + 1
		varName := s[i+1 : closeIdx]
		if varName == "" {
			sb.WriteByte('%')
			i = closeIdx + 1
			continue
		}
		sb.WriteString(e.Get(varName))
		i = closeIdx + 1
	}
	return sb.String()
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
		sb.WriteString(resolveDelayedRef(name, e))
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
