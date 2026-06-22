// Package expander resolves variable references in parsed AST word parts.
package expander

import (
	"os"
	"path/filepath"
	"strconv"
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
			if pt.Name != "" {
				// FOR-variable form (%%~nf): value comes from the env var.
				sb.WriteString(applyTildeMods(e.Get(pt.Name), pt.Modifiers))
			} else {
				sb.WriteString(expandTilde(pt, positional))
			}
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

// expandTilde handles the call-argument tilde form %~1, %~dp0, %~n1, etc.
func expandTilde(pt *parser.TildeVarPart, positional []string) string {
	val := ""
	if pt.Positional < len(positional) {
		val = positional[pt.Positional]
	}
	return applyTildeMods(val, pt.Modifiers)
}

// applyTildeMods applies cmd.exe path-decomposition modifiers (d p n x f s,
// and the file-info modifiers z t a) to a path value. Shared by the
// call-argument form (%~dp1) and the FOR-variable form (%%~dpf). With no
// modifiers it just strips surrounding quotes.
func applyTildeMods(val, modifiers string) string {
	if modifiers == "" {
		return stripQuotes(val)
	}

	mods := strings.ToLower(modifiers)
	// Normalize backslashes to forward slashes so filepath.Abs/Clean can
	// resolve ".." segments (on Unix, "\" is not a separator, so "..\..\x"
	// would otherwise stay literal). Consistent with the rest of the port.
	path := strings.ReplaceAll(stripQuotes(val), "\\", "/")
	// Resolve to absolute so d/p/f return useful paths, matching cmd.exe where
	// %~dp0 / %~f1 always give a fully-qualified result.
	absPath := path
	if abs, err := filepath.Abs(path); err == nil {
		absPath = abs
	}

	// File-info modifiers (size/time/attributes) are stat queries used on
	// their own (e.g. %~zf, %~tf, %~af); handle them before the path build.
	if strings.ContainsAny(mods, "atz") {
		info, err := os.Stat(absPath)
		if err != nil {
			return ""
		}
		switch {
		case strings.Contains(mods, "z"):
			return strconv.FormatInt(info.Size(), 10)
		case strings.Contains(mods, "t"):
			return info.ModTime().Format("01/02/2006 03:04 PM")
		case strings.Contains(mods, "a"):
			return fileAttrString(info)
		}
	}

	// f = full path (overrides the piecewise build). s (short name) has no
	// 8.3 equivalent on Unix, so a lone s also yields the full path.
	if strings.Contains(mods, "f") {
		return absPath
	}

	result := ""
	recognized := false
	// d = drive (on Unix, "/" for an absolute path).
	if strings.Contains(mods, "d") {
		recognized = true
		if filepath.IsAbs(absPath) {
			result += "/"
		}
	}
	// p = directory part of the absolute path.
	if strings.Contains(mods, "p") {
		recognized = true
		dir := filepath.Dir(absPath)
		// If d already added "/", don't double the leading slash.
		if strings.Contains(mods, "d") && strings.HasPrefix(dir, "/") {
			dir = strings.TrimPrefix(dir, "/")
		}
		result += dir
		if !strings.HasSuffix(result, "/") {
			result += "/"
		}
	}
	// n = file name without extension.
	if strings.Contains(mods, "n") {
		recognized = true
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		result += strings.TrimSuffix(base, ext)
	}
	// x = extension only.
	if strings.Contains(mods, "x") {
		recognized = true
		result += filepath.Ext(path)
	}
	// s = short name: no 8.3 on Unix; if used alone, yield the full path.
	if strings.Contains(mods, "s") {
		recognized = true
		if result == "" {
			result = absPath
		}
	}

	// No recognized modifier: fall back to the quote-stripped value. When a
	// modifier WAS recognized, an empty result is legitimate (e.g. %~x of an
	// extensionless file) and must be returned as-is.
	if !recognized {
		return stripQuotes(val)
	}
	return result
}

// fileAttrString builds a cmd.exe-style 9-char attribute string. Unix lacks
// most DOS attributes; we approximate directory and read-only.
func fileAttrString(info os.FileInfo) string {
	b := []byte("---------")
	if info.IsDir() {
		b[0] = 'd'
	}
	if info.Mode().Perm()&0o200 == 0 {
		b[1] = 'r'
	}
	return string(b)
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
			// %%~<modifiers><letter> — tilde-modified FOR variable.
			if i+2 < len(s) && s[i+2] == '~' {
				j := i + 3
				for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
					j++
				}
				if j > i+3 {
					seq := s[i+3 : j] // modifiers + the FOR variable (last letter)
					varCh := seq[len(seq)-1]
					mods := seq[:len(seq)-1]
					sb.WriteString(applyTildeMods(e.Get(string(varCh)), mods))
					i = j
					continue
				}
			}
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
