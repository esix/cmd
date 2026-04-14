package expander

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/esix/cmd/env"
)

// ExpandPercent performs early expansion on a raw line: replaces %VAR%, %N,
// and %~[mods]N with their values. This happens BEFORE tokenizing, which is
// how real CMD.EXE works. Delayed expansion (!VAR!) is NOT touched here.
func ExpandPercent(line string, e *env.Env, positional []string) string {
	var sb strings.Builder
	i := 0
	for i < len(line) {
		if line[i] != '%' {
			sb.WriteByte(line[i])
			i++
			continue
		}

		// %% → literal %
		// %% handling:
		// %%X (single letter, not followed by alnum) → FOR variable, keep %%X
		// Otherwise → keep %% intact (SET /A modulo or literal)
		if i+1 < len(line) && line[i+1] == '%' {
			if i+2 < len(line) {
				ch := line[i+2]
				isLetter := (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
				nextIsAlnum := i+3 < len(line) && isAlphaNum(line[i+3])
				if isLetter && !nextIsAlnum {
					sb.WriteByte('%')
					sb.WriteByte('%')
					sb.WriteByte(ch)
					i += 3
					continue
				}
			}
			sb.WriteByte('%')
			sb.WriteByte('%')
			i += 2
			continue
		}

		// %~[modifiers]N — tilde parameter expansion
		if i+1 < len(line) && line[i+1] == '~' {
			j := i + 2
			// Read modifier letters
			for j < len(line) && ((line[j] >= 'a' && line[j] <= 'z') || (line[j] >= 'A' && line[j] <= 'Z')) {
				j++
			}
			// Expect a digit
			if j < len(line) && line[j] >= '0' && line[j] <= '9' {
				mods := strings.ToLower(line[i+2 : j])
				digit := int(line[j] - '0')
				val := tildeExpand(digit, mods, positional)
				sb.WriteString(val)
				i = j + 1
				continue
			}
			// Not a valid tilde ref
			sb.WriteByte('%')
			i++
			continue
		}

		// %0-%9 — positional parameter
		if i+1 < len(line) && line[i+1] >= '0' && line[i+1] <= '9' {
			digit := int(line[i+1] - '0')
			if digit < len(positional) {
				sb.WriteString(positional[digit])
			}
			i += 2
			continue
		}

		// %VAR% or %VAR:~N,M%
		closeIdx := strings.IndexByte(line[i+1:], '%')
		if closeIdx == -1 {
			sb.WriteByte('%')
			i++
			continue
		}
		closeIdx += i + 1
		name := line[i+1 : closeIdx]

		if name == "" {
			sb.WriteByte('%')
			i = closeIdx + 1
			continue
		}

		// %VAR:~N,M% — substring
		if colonIdx := strings.Index(name, ":~"); colonIdx != -1 {
			varName := name[:colonIdx]
			spec := name[colonIdx+2:]
			val := e.Get(varName)
			sb.WriteString(substringExpand(val, spec))
			i = closeIdx + 1
			continue
		}

		// %VAR:old=new% — string replacement
		if colonIdx := strings.IndexByte(name, ':'); colonIdx != -1 {
			eqIdx := strings.IndexByte(name[colonIdx+1:], '=')
			if eqIdx != -1 {
				varName := name[:colonIdx]
				old := name[colonIdx+1 : colonIdx+1+eqIdx]
				newStr := name[colonIdx+1+eqIdx+1:]
				val := e.Get(varName)
				sb.WriteString(strings.ReplaceAll(val, old, newStr))
				i = closeIdx + 1
				continue
			}
		}

		// %VAR%
		sb.WriteString(e.Get(name))
		i = closeIdx + 1
	}
	return sb.String()
}

func tildeExpand(digit int, mods string, positional []string) string {
	val := ""
	if digit < len(positional) {
		val = positional[digit]
	}

	if mods == "" {
		return stripQuotes(val)
	}

	path := stripQuotes(val)
	result := ""

	if strings.Contains(mods, "f") {
		abs, err := filepath.Abs(path)
		if err == nil {
			return abs
		}
		return path
	}

	if strings.Contains(mods, "d") {
		if filepath.IsAbs(path) {
			result += "/"
		}
	}
	if strings.Contains(mods, "p") {
		dir := filepath.Dir(path)
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		result += dir
	}
	if strings.Contains(mods, "n") {
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		result += strings.TrimSuffix(base, ext)
	}
	if strings.Contains(mods, "x") {
		result += filepath.Ext(path)
	}

	if result == "" {
		return stripQuotes(val)
	}
	return result
}

func substringExpand(val, spec string) string {
	start := 0
	length := 0
	hasLength := false

	if commaIdx := strings.IndexByte(spec, ','); commaIdx != -1 {
		start, _ = strconv.Atoi(spec[:commaIdx])
		length, _ = strconv.Atoi(spec[commaIdx+1:])
		hasLength = true
	} else {
		start, _ = strconv.Atoi(spec)
	}

	n := len(val)
	if start < 0 {
		start = n + start
		if start < 0 {
			start = 0
		}
	}
	if start > n {
		return ""
	}
	if !hasLength {
		return val[start:]
	}
	if length < 0 {
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

func isAlphaNum(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}
