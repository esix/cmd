package builtins

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/esix/cmd/env"
)

// Findstr implements the commonly used subset of Windows findstr.exe:
//
//	/R  search strings are regular expressions (findstr dialect)
//	/L  search strings are literals
//	/C:"string"  literal/regex search string that may contain spaces (repeatable)
//	/I  case-insensitive
//	/V  print lines that do NOT match
//	/N  prefix matching lines with their line number
//	/B  match at beginning of line
//	/E  match at end of line
//	/X  match whole line exactly
//	/M  print only file names with matches
//
// A bare (non-/C:) pattern argument is split on spaces into multiple search
// strings, any of which may match — the classic findstr gotcha. Exit code 0
// if any line matched, 1 if none.
func Findstr(args []string, _ *env.Env) int {
	var (
		regex, literal, ignoreCase, invert  bool
		lineNums, begin, end, exact, names  bool
		patterns                            []string
		patternsAreC                        bool
		files                               []string
	)

	for _, a := range args {
		// A switch is `/X` or `/X:...` — a single letter after the slash.
		// Anything longer without a colon (e.g. /var/folders/x/file) is a
		// Unix file path, not a switch.
		isSwitch := strings.HasPrefix(a, "/") &&
			(len(a) == 2 || (len(a) >= 3 && a[2] == ':'))
		if isSwitch {
			upper := strings.ToUpper(a)
			switch {
			case upper == "/R":
				regex = true
			case upper == "/L":
				literal = true
			case upper == "/I":
				ignoreCase = true
			case upper == "/V":
				invert = true
			case upper == "/N":
				lineNums = true
			case upper == "/B":
				begin = true
			case upper == "/E":
				end = true
			case upper == "/X":
				exact = true
			case upper == "/M":
				names = true
			case strings.HasPrefix(upper, "/C:"):
				patterns = append(patterns, strings.Trim(a[3:], "\""))
				patternsAreC = true
			default:
				// /G: /F: /D: /A: /P /S /O etc. not supported — ignore
			}
			continue
		}
		arg := strings.Trim(a, "\"")
		if len(patterns) == 0 && !patternsAreC {
			// First non-switch arg is the search-string list; spaces separate
			// alternative search strings (each may match independently).
			// Skip empties from consecutive spaces — they'd match every line.
			for _, w := range strings.Split(arg, " ") {
				if w != "" {
					patterns = append(patterns, w)
				}
			}
		} else {
			files = append(files, toUnixPath(arg))
		}
	}

	if len(patterns) == 0 {
		fmt.Fprintln(os.Stderr, "FINDSTR: Bad command line")
		return 2
	}

	// /C: strings are literal unless /R; bare strings are regex unless /L.
	useRegex := regex || (!literal && !patternsAreC)

	matchers := make([]func(string) bool, 0, len(patterns))
	for _, p := range patterns {
		var expr string
		if useRegex {
			expr = findstrRegexToGo(p)
		} else {
			expr = regexp.QuoteMeta(p)
		}
		if exact {
			expr = "^(?:" + expr + ")$"
		} else {
			if begin && !strings.HasPrefix(expr, "^") {
				expr = "^(?:" + expr + ")"
			}
			if end && !strings.HasSuffix(expr, "$") {
				expr = "(?:" + expr + ")$"
			}
		}
		if ignoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FINDSTR: bad search string %q\n", p)
			return 2
		}
		matchers = append(matchers, re.MatchString)
	}

	lineMatches := func(line string) bool {
		for _, m := range matchers {
			if m(line) {
				return !invert
			}
		}
		return invert
	}

	showName := len(files) > 1
	anyMatch := false

	scanOne := func(name string, lines []string) {
		fileHit := false
		for i, line := range lines {
			if !lineMatches(line) {
				continue
			}
			anyMatch = true
			fileHit = true
			if names {
				break // only the file name is printed, once
			}
			prefix := ""
			if showName {
				prefix = name + ":"
			}
			if lineNums {
				prefix += fmt.Sprintf("%d:", i+1)
			}
			fmt.Print(prefix + line + "\r\n")
		}
		if names && fileHit {
			fmt.Print(name + "\r\n")
		}
	}

	if len(files) == 0 {
		// No files: filter stdin (pipe mode).
		var lines []string
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
		}
		scanOne("", lines)
	} else {
		code := 0
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				fmt.Fprintf(os.Stderr, "FINDSTR: Cannot open %s\n", f)
				code = 2
				continue
			}
			text := strings.TrimRight(string(data), "\r\n")
			var lines []string
			if text != "" {
				lines = strings.Split(text, "\n")
				for i := range lines {
					lines[i] = strings.TrimRight(lines[i], "\r")
				}
			}
			scanOne(f, lines)
		}
		if code != 0 && !anyMatch {
			return code
		}
	}

	if anyMatch {
		return 0
	}
	return 1
}

// findstrRegexToGo translates findstr's regex dialect to Go regexp syntax.
// findstr supports: . * ^ $ [class] \< \> and treats everything else as a
// literal — so Go metacharacters like + ? ( ) { } | must be escaped.
func findstrRegexToGo(p string) string {
	var sb strings.Builder
	i := 0
	for i < len(p) {
		ch := p[i]
		switch ch {
		case '\\':
			if i+1 < len(p) {
				next := p[i+1]
				if next == '<' || next == '>' {
					sb.WriteString(`\b`) // word boundary
				} else {
					sb.WriteString(regexp.QuoteMeta(string(next)))
				}
				i += 2
				continue
			}
			sb.WriteString(`\\`)
		case '.', '*', '^', '$':
			sb.WriteByte(ch)
		case '[':
			// Copy the character class verbatim through the closing ].
			j := i + 1
			if j < len(p) && (p[j] == '^') {
				j++
			}
			if j < len(p) && p[j] == ']' {
				j++ // a ] right after [ (or [^) is a literal member
			}
			for j < len(p) && p[j] != ']' {
				j++
			}
			if j < len(p) {
				sb.WriteString(p[i : j+1])
				i = j + 1
				continue
			}
			sb.WriteString(`\[`) // unterminated class → literal
		default:
			sb.WriteString(regexp.QuoteMeta(string(ch)))
		}
		i++
	}
	return sb.String()
}
