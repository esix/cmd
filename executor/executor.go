// Package executor runs parsed BAT statements.
package executor

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/executor/builtins"
	"github.com/esix/cmd/expander"
	"github.com/esix/cmd/parser"
)

// scriptLine is a raw line in a .bat file, pre-classified as label or code.
type scriptLine struct {
	raw   string // the line text (@ stripped, trimmed)
	label string // non-empty if this is a :LABEL line (uppercased, without colon)
}

// Executor runs a list of statements with a program counter (for GOTO support).
type Executor struct {
	env        *env.Env
	positional []string // %0, %1, ... script arguments
	stmts      []parser.Statement // used by RunStmts (interactive / inline)
	lines      []scriptLine       // used by RunFile (lazy parsing)
	pc         int                // current line or statement index
	gotoPending bool // true when GOTO was executed in a nested context
	exitPending bool // true when EXIT /B was executed in a nested context
}

// shouldStop returns true if GOTO or EXIT /B is pending.
func (ex *Executor) shouldStop() bool {
	return ex.gotoPending || ex.exitPending
}

// New creates an Executor.
func New(e *env.Env) *Executor {
	return &Executor{env: e}
}

// RunLine parses and runs a single line interactively.
func (ex *Executor) RunLine(line string) int {
	// Strip leading @ (echo suppression)
	echoLine := true
	if strings.HasPrefix(line, "@") {
		echoLine = false
		line = line[1:]
	}

	// Handle ECHO. (blank line) before tokenizing
	if strings.ToUpper(strings.TrimSpace(line)) == "ECHO." ||
		strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "ECHO.") {
		if echoLine && ex.env.Echo {
			fmt.Println(strings.TrimSpace(line))
		}
		fmt.Println()
		return 0
	}

	line = expander.ExpandPercent(line, ex.env, ex.positional)
	stmts, err := parser.ParseLineWithOpts(line, ex.env.DelayedExpansion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
		return 1
	}

	if ex.env.Echo && echoLine && len(stmts) > 0 {
		// In file mode, echo the line before executing
	}

	return ex.RunStmts(stmts, nil)
}

// fileCache caches preprocessed script lines to avoid re-reading and
// re-processing the same .bat file on every CALL.
// fileCache caches preprocessed script lines.
var fileCache = make(map[string][]scriptLine)

// RunFile executes a .bat file. Lines are parsed on-the-fly so that
// SETLOCAL EnableDelayedExpansion takes effect for subsequent lines.
func (ex *Executor) RunFile(path string, args []string) int {
	// Resolve to absolute path for cache key
	absPath, _ := filepath.Abs(path)

	slines, cached := fileCache[absPath]
	if cached {
		return ex.runLines(slines, path, args)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open file: %s\n", path)
		return 1
	}

	// Pre-process lines: classify as label or code, skip blanks and REM
	rawLines := strings.Split(string(data), "\n")
	slines = nil
	for _, line := range rawLines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Strip leading @ (echo suppression marker) — may have leading whitespace
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "@") {
			trimmed = strings.TrimSpace(trimmed[1:])
			line = trimmed
		}

		// Shebang line
		if strings.HasPrefix(trimmed, "#!") {
			continue
		}

		// :: comment (double colon = label that's never matched)
		if strings.HasPrefix(trimmed, "::") {
			continue
		}

		// Label — only first word is the label name, rest is comment
		if strings.HasPrefix(trimmed, ":") {
			labelLine := strings.TrimSpace(trimmed[1:])
			fields := strings.Fields(labelLine)
			if len(fields) == 0 {
				continue
			}
			slines = append(slines, scriptLine{label: strings.ToUpper(fields[0])})
			continue
		}

		// REM / comment
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "REM") && (len(upper) == 3 || upper[3] == ' ') {
			continue
		}

		slines = append(slines, scriptLine{raw: line})
	}

	// Join multi-line ( ... ) blocks into single lines using & as separator
	slines = joinBlocks(slines)

	// Cache for future calls
	fileCache[absPath] = slines

	return ex.runLines(slines, path, args)
}


func (ex *Executor) runLines(slines []scriptLine, path string, args []string) int {
	ex.env.FileMode = true
	savedPos := ex.positional
	ex.positional = append([]string{path}, args...)

	savedLines := ex.lines
	savedPC := ex.pc
	ex.lines = slines
	ex.pc = 0

	code := 0
	for ex.pc < len(ex.lines) {
		sl := ex.lines[ex.pc]
		ex.pc++

		if sl.label != "" {
			continue // labels are no-ops during execution
		}

		// Parse with current delayed expansion state
		expanded := expander.ExpandPercent(sl.raw, ex.env, ex.positional)
		stmts, err := parser.ParseLineWithOpts(expanded, ex.env.DelayedExpansion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			continue
		}
		for _, stmt := range stmts {
			code = ex.execute(stmt)
			ex.env.ExitCode = code
			if ex.gotoPending {
				ex.gotoPending = false
				break
			}
			if ex.exitPending {
				ex.exitPending = false
				ex.pc = len(ex.lines) // stop file execution
				break
			}
		}
	}

	ex.lines = savedLines
	ex.pc = savedPC
	ex.positional = savedPos
	return code
}

// RunStmts executes a slice of statements with GOTO support.
func (ex *Executor) RunStmts(stmts []parser.Statement, positional []string) int {
	saved := ex.stmts
	savedPC := ex.pc
	savedPos := ex.positional

	ex.stmts = stmts
	ex.pc = 0
	if positional != nil {
		ex.positional = positional
	}

	code := 0
	for ex.pc < len(ex.stmts) {
		stmt := ex.stmts[ex.pc]
		ex.pc++
		code = ex.execute(stmt)
		ex.env.ExitCode = code
		if ex.shouldStop() {
			break
		}
	}

	ex.stmts = saved
	if !ex.gotoPending && !ex.exitPending {
		ex.pc = savedPC
	}
	ex.positional = savedPos
	return code
}

func (ex *Executor) execute(stmt parser.Statement) int {
	switch s := stmt.(type) {
	case *parser.EchoStatement:
		return ex.execEcho(s)
	case *parser.SetStatement:
		return ex.execSet(s)
	case *parser.IfStatement:
		return ex.execIf(s)
	case *parser.GotoStatement:
		return ex.execGoto(s)
	case *parser.CallStatement:
		return ex.execCall(s)
	case *parser.ForStatement:
		return ex.execFor(s)
	case *parser.ExitStatement:
		return ex.execExit(s)
	case *parser.ShiftStatement:
		return ex.execShift()
	case *parser.PipeStatement:
		return ex.execPipe(s)
	case *parser.ChainStatement:
		return ex.execChain(s)
	case *parser.BlockStatement:
		return ex.execBlock(s)
	case *parser.SetlocalStatement:
		return ex.execSetlocal(s)
	case *parser.EndlocalStatement:
		return ex.execEndlocal()
	case *parser.LabelStatement:
		return 0
	case *parser.SimpleCommand:
		return ex.execSimple(s)
	default:
		fmt.Fprintf(os.Stderr, "unknown statement type: %T\n", stmt)
		return 1
	}
}

// --- PIPE: cmd1 | cmd2 | cmd3 ---

func (ex *Executor) execPipe(s *parser.PipeStatement) int {
	if len(s.Commands) == 0 {
		return 0
	}
	if len(s.Commands) == 1 {
		return ex.execute(s.Commands[0])
	}

	// Build pipe chain: create all intermediate pipes
	n := len(s.Commands)
	pipes := make([]io.ReadCloser, n-1) // pipes[i] = stdout of command i
	writers := make([]io.WriteCloser, n-1)
	for i := 0; i < n-1; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Pipe error: %v\n", err)
			return 1
		}
		pipes[i] = r
		writers[i] = w
	}

	// Run each stage in a goroutine
	type result struct {
		index int
		code  int
	}
	done := make(chan result, n)

	for i, stmt := range s.Commands {
		var stdin io.Reader = os.Stdin
		var stdout io.Writer = os.Stdout
		if i > 0 {
			stdin = pipes[i-1]
		}
		if i < n-1 {
			stdout = writers[i]
		}

		go func(idx int, st parser.Statement, in io.Reader, out io.Writer) {
			code := ex.runPipeStage(st, in, out)
			// Close our write end so the next stage sees EOF
			if wc, ok := out.(io.WriteCloser); ok && idx < n-1 {
				wc.Close()
			}
			done <- result{idx, code}
		}(i, stmt, stdin, stdout)
	}

	// Collect results
	codes := make([]int, n)
	for range s.Commands {
		r := <-done
		codes[r.index] = r.code
	}

	// Close read ends
	for _, r := range pipes {
		r.Close()
	}

	return codes[n-1] // exit code of last command
}

// runPipeStage runs a single command in a pipeline with redirected I/O.
func (ex *Executor) runPipeStage(stmt parser.Statement, stdin io.Reader, stdout io.Writer) int {
	// External command (SimpleCommand)
	if simple, ok := stmt.(*parser.SimpleCommand); ok {
		args := make([]string, 0, len(simple.Args))
		for _, part := range simple.Args {
			args = append(args, ex.expandParts([]parser.WordPart{part}))
		}
		if len(args) == 0 {
			return 0
		}
		cmdName := strings.ToUpper(args[0])
		// ECHO builtin — use system echo in pipe context
		if cmdName == "ECHO" {
			fmt.Fprintln(stdout, strings.Join(args[1:], " "))
			return 0
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			return 1
		}
		return 0
	}

	// Builtin statements (ECHO, TYPE, etc.) — redirect stdout
	if echo, ok := stmt.(*parser.EchoStatement); ok {
		words := make([]string, 0, len(echo.Args))
		for _, group := range echo.Args {
			words = append(words, ex.expandParts(group))
		}
		fmt.Fprintln(stdout, strings.Join(words, " "))
		return 0
	}

	// Fallback: run normally (stdout not redirected for complex statements)
	return ex.execute(stmt)
}

// --- CHAIN: cmd1 && cmd2, cmd1 || cmd2, cmd1 & cmd2 ---

func (ex *Executor) execChain(s *parser.ChainStatement) int {
	leftCode := ex.execute(s.Left)
	if ex.shouldStop() {
		return leftCode
	}
	switch s.Op {
	case "&":
		return ex.execute(s.Right)
	case "&&":
		if leftCode == 0 {
			return ex.execute(s.Right)
		}
		return leftCode
	case "||":
		if leftCode != 0 {
			return ex.execute(s.Right)
		}
		return leftCode
	}
	return leftCode
}

// --- BLOCK: ( stmt1 & stmt2 & ... ) ---

func (ex *Executor) execBlock(s *parser.BlockStatement) int {
	code := 0
	for _, stmt := range s.Stmts {
		code = ex.execute(stmt)
		ex.env.ExitCode = code
		if ex.shouldStop() {
			break
		}
	}
	return code
}

// --- SHIFT ---

func (ex *Executor) execShift() int {
	if len(ex.positional) > 1 {
		ex.positional = ex.positional[1:]
	}
	return 0
}

// --- ECHO ---

func (ex *Executor) execEcho(s *parser.EchoStatement) int {
	if s.Newline {
		fmt.Println()
		return 0
	}
	if s.TurnOn != nil {
		ex.env.Echo = *s.TurnOn
		return 0
	}
	words := make([]string, 0, len(s.Args))
	for _, group := range s.Args {
		words = append(words, ex.expandParts(group))
	}
	// Handle redirections (e.g. ECHO error 1>&2)
	out := os.Stdout
	for _, r := range s.Redirects {
		switch r.Op {
		case "1>&2", ">&2":
			out = os.Stderr
		case ">", "1>":
			f, err := os.Create(r.File)
			if err == nil {
				defer f.Close()
				out = f
			}
		case ">>", "1>>":
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				defer f.Close()
				out = f
			}
		}
	}
	if len(words) == 0 {
		if ex.env.Echo {
			fmt.Fprintln(out, "ECHO is on.")
		} else {
			fmt.Fprintln(out, "ECHO is off.")
		}
		return 0
	}
	fmt.Fprintln(out, strings.Join(words, " "))
	return 0
}

// --- SET ---

func (ex *Executor) execSet(s *parser.SetStatement) int {
	if s.Name == "" {
		return builtins.Set(nil, ex.env)
	}
	// Expand each word group and join with spaces
	words := make([]string, 0, len(s.Value))
	for _, group := range s.Value {
		words = append(words, ex.expandParts(group))
	}
	value := strings.Join(words, " ")

	if s.Arithmetic {
		// Expand !VAR! in arithmetic expressions when delayed expansion is on
		if ex.env.DelayedExpansion {
			value = expander.ExpandBangs(value, ex.env)
		}
		return builtins.Set([]string{"/A", s.Name + "=" + value}, ex.env)
	}

	if !s.HasEquals {
		// SET NAME without = → display the variable
		return builtins.Set([]string{s.Name}, ex.env)
	}
	if value == "" {
		ex.env.Unset(s.Name)
	} else {
		ex.env.Set(s.Name, value)
	}
	return 0
}

// --- IF ---

func (ex *Executor) execIf(s *parser.IfStatement) int {
	result := ex.evalCondition(s.Condition)
	if s.Not {
		result = !result
	}
	if result {
		return ex.RunStmts(s.Then, nil)
	} else if len(s.Else) > 0 {
		return ex.RunStmts(s.Else, nil)
	}
	return 0
}

func (ex *Executor) evalCondition(cond parser.Condition) bool {
	switch c := cond.(type) {
	case *parser.StringCompare:
		left := stripOuterQuotes(ex.expandParts(c.Left))
		right := stripOuterQuotes(ex.expandParts(c.Right))
		return left == right

	case *parser.NumericCompare:
		left := stripOuterQuotes(ex.expandParts(c.Left))
		right := stripOuterQuotes(ex.expandParts(c.Right))
		// CMD behavior: try numeric first, fall back to string comparison
		lNum, lErr := strconv.Atoi(strings.TrimSpace(left))
		rNum, rErr := strconv.Atoi(strings.TrimSpace(right))
		if lErr == nil && rErr == nil {
			// Both are numbers: numeric comparison
			switch c.Op {
			case "EQU":
				return lNum == rNum
			case "NEQ":
				return lNum != rNum
			case "LSS":
				return lNum < rNum
			case "LEQ":
				return lNum <= rNum
			case "GTR":
				return lNum > rNum
			case "GEQ":
				return lNum >= rNum
			}
		} else {
			// Fall back to string comparison
			switch c.Op {
			case "EQU":
				return left == right
			case "NEQ":
				return left != right
			case "LSS":
				return left < right
			case "LEQ":
				return left <= right
			case "GTR":
				return left > right
			case "GEQ":
				return left >= right
			}
		}

	case *parser.DefinedCondition:
		return ex.env.Get(c.Name) != ""

	case *parser.ExistCondition:
		path := ex.expandParts(c.Path)
		_, err := os.Stat(path)
		return err == nil

	case *parser.ErrorlevelCondition:
		return ex.env.ExitCode >= c.N
	}
	return false
}

// --- GOTO ---

func (ex *Executor) execGoto(s *parser.GotoStatement) int {
	var raw string
	if len(s.LabelParts) > 0 {
		raw = ex.expandParts(s.LabelParts)
	} else {
		raw = s.Label
	}
	label := strings.ToUpper(strings.TrimPrefix(raw, ":"))

	// GOTO :EOF — jump past end (exit script/subroutine)
	if label == "EOF" {
		if ex.lines != nil {
			ex.pc = len(ex.lines)
		} else {
			ex.pc = len(ex.stmts)
		}
		return 0
	}

	// Search in file lines (lazy-parse mode)
	if ex.lines != nil {
		for i, sl := range ex.lines {
			if sl.label == label {
				ex.pc = i + 1
				ex.gotoPending = true // signal RunStmts to break out
				return 0
			}
		}
		fmt.Fprintf(os.Stderr, "Label not found: %s\n", s.Label)
		return 1
	}

	// Search in pre-parsed statements (interactive/RunStmts mode)
	for i, stmt := range ex.stmts {
		if lbl, ok := stmt.(*parser.LabelStatement); ok {
			if lbl.Name == label {
				ex.pc = i + 1
				return 0
			}
		}
	}
	fmt.Fprintf(os.Stderr, "Label not found: %s\n", s.Label)
	return 1
}

// --- CALL ---

func (ex *Executor) execCall(s *parser.CallStatement) int {
	if len(s.Args) == 0 {
		return 0
	}
	first := ex.expandParts([]parser.WordPart{s.Args[0]})

	// Expand remaining args as positional params for the call
	var callArgs []string
	callArgs = append(callArgs, first) // %0 = label or script name
	for _, arg := range s.Args[1:] {
		callArgs = append(callArgs, ex.expandParts([]parser.WordPart{arg}))
	}

	// CALL :label — subroutine within same file
	if strings.HasPrefix(first, ":") {
		label := strings.ToUpper(first[1:])
		savedPC := ex.pc
		savedPos := ex.positional
		ex.positional = callArgs

		if ex.lines != nil {
			for i, sl := range ex.lines {
				if sl.label == label {
					ex.pc = i + 1
					code := 0
					for ex.pc < len(ex.lines) {
						sl := ex.lines[ex.pc]
						ex.pc++
						if sl.label != "" {
							continue
						}
						expanded := expander.ExpandPercent(sl.raw, ex.env, ex.positional)
		stmts, err := parser.ParseLineWithOpts(expanded, ex.env.DelayedExpansion)
						if err != nil {
							continue
						}
						done := false
						for _, stmt := range stmts {
							if exitStmt, ok := stmt.(*parser.ExitStatement); ok && exitStmt.SubOnly {
								code = exitStmt.Code
								done = true
								break
							}
							code = ex.execute(stmt)
							if ex.shouldStop() {
								break
							}
						}
						if done || ex.exitPending {
							ex.exitPending = false
							break
						}
						if ex.gotoPending {
							ex.gotoPending = false
							continue
						}
					}
					ex.pc = savedPC
					ex.positional = savedPos
					return code
				}
			}
		} else {
			for i, stmt := range ex.stmts {
				if lbl, ok := stmt.(*parser.LabelStatement); ok && lbl.Name == label {
					ex.pc = i + 1
					code := 0
					for ex.pc < len(ex.stmts) {
						stmt := ex.stmts[ex.pc]
						ex.pc++
						if exitStmt, ok := stmt.(*parser.ExitStatement); ok && exitStmt.SubOnly {
							code = exitStmt.Code
							break
						}
						code = ex.execute(stmt)
					}
					ex.pc = savedPC
					ex.positional = savedPos
					return code
				}
			}
		}
		ex.positional = savedPos
		fmt.Fprintf(os.Stderr, "Label not found: %s\n", first)
		return 1
	}

	// CALL script.bat [args...]
	scriptPath := first
	if resolved, ok := ex.resolveBat(scriptPath); ok {
		scriptPath = resolved
	}
	var scriptArgs []string
	for _, part := range s.Args[1:] {
		scriptArgs = append(scriptArgs, ex.expandParts([]parser.WordPart{part}))
	}

	sub := &Executor{env: ex.env}
	return sub.RunFile(scriptPath, scriptArgs)
}

// --- FOR ---

func (ex *Executor) execFor(s *parser.ForStatement) int {
	switch s.Kind {
	case parser.ForRange:
		return ex.execForRange(s)
	case parser.ForInList:
		return ex.execForInList(s)
	case parser.ForTokens:
		return ex.execForTokens(s)
	default:
		fmt.Fprintf(os.Stderr, "FOR variant not yet implemented\n")
		return 1
	}
}

func (ex *Executor) execForRange(s *parser.ForStatement) int {
	// IN (start,step,end)
	if len(s.InList) < 3 {
		fmt.Fprintf(os.Stderr, "FOR /L requires (start,step,end)\n")
		return 1
	}
	parseI := func(v string) int {
		n := 0
		fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	}
	start := parseI(s.InList[0])
	step := parseI(s.InList[1])
	end := parseI(s.InList[2])

	code := 0
	for i := start; (step > 0 && i <= end) || (step < 0 && i >= end); i += step {
		ex.env.Set(s.Variable, fmt.Sprintf("%d", i))
		code = ex.RunStmts(s.Body, nil)
	}
	return code
}

func (ex *Executor) execForInList(s *parser.ForStatement) int {
	code := 0
	for _, item := range s.InList {
		// Check if item is a glob pattern
		if strings.ContainsAny(item, "*?") {
			matches, _ := filepath.Glob(item)
			for _, m := range matches {
				ex.env.Set(s.Variable, m)
				code = ex.RunStmts(s.Body, nil)
			}
		} else {
			ex.env.Set(s.Variable, item)
			code = ex.RunStmts(s.Body, nil)
		}
	}
	return code
}

// --- FOR /F ---

type forFOpts struct {
	tokens  []int  // token indices (1-based); -1 = wildcard (rest of line)
	delims  string // delimiter characters
	eol     byte   // end-of-line comment char
	usebackq bool
}

func parseForFOpts(optStr string) forFOpts {
	opts := forFOpts{
		tokens: []int{1},       // default: token 1
		delims: " \t",          // default: space and tab
		eol:    ';',            // default: semicolon
	}
	optStr = strings.Trim(optStr, "\"")
	parts := strings.Fields(optStr)
	for _, part := range parts {
		lower := strings.ToLower(part)
		if lower == "usebackq" {
			opts.usebackq = true
			continue
		}
		if strings.HasPrefix(lower, "eol=") {
			if len(part) > 4 {
				opts.eol = part[4]
			}
			continue
		}
		if strings.HasPrefix(lower, "tokens=") {
			spec := part[7:]
			opts.tokens = parseTokenSpec(spec)
			continue
		}
		if strings.HasPrefix(lower, "delims=") {
			opts.delims = part[7:]
			continue
		}
	}
	return opts
}

// parseTokenSpec parses "1,2*" or "1*" or "1,2,3" into token indices.
// -1 represents the wildcard (*) = rest of line.
func parseTokenSpec(spec string) []int {
	var result []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		wildcard := strings.HasSuffix(part, "*")
		part = strings.TrimSuffix(part, "*")
		if part != "" {
			n, _ := strconv.Atoi(part)
			result = append(result, n)
		}
		if wildcard {
			result = append(result, -1) // wildcard marker
		}
	}
	if len(result) == 0 {
		result = []int{1}
	}
	return result
}

// splitByDelims splits s by any character in delims, returning non-empty tokens.
func splitByDelims(s, delims string) []string {
	if delims == "" {
		return []string{s}
	}
	f := func(r rune) bool {
		return strings.ContainsRune(delims, r)
	}
	return strings.FieldsFunc(s, f)
}

func (ex *Executor) execForTokens(s *parser.ForStatement) int {
	opts := parseForFOpts(s.Options)

	// Determine source lines
	var lines []string
	if len(s.InList) > 0 {
		source := strings.Join(s.InList, " ")
		source = strings.Trim(source, " ")
		// Apply delayed expansion to the source
		if ex.env.DelayedExpansion {
			source = expander.ExpandBangs(source, ex.env)
		}

		if (strings.HasPrefix(source, "'") && strings.HasSuffix(source, "'")) ||
			(strings.HasPrefix(source, "`") && strings.HasSuffix(source, "`")) {
			// Command: execute and capture output
			cmdStr := source[1 : len(source)-1]
			out, err := exec.Command("sh", "-c", cmdStr).Output()
			if err == nil {
				lines = strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			}
		} else if strings.HasPrefix(source, "\"") && strings.HasSuffix(source, "\"") {
			if opts.usebackq {
				// usebackq + "..." = read from file
				filename := source[1 : len(source)-1]
				data, err := os.ReadFile(filename)
				if err == nil {
					lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
				}
			} else {
				// "string" = parse the string directly
				str := source[1 : len(source)-1]
				lines = []string{str}
			}
		} else {
			// Bare filename
			data, err := os.ReadFile(source)
			if err == nil {
				lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
			}
		}
	}

	code := 0
	baseVar := strings.ToUpper(s.Variable)

	for _, line := range lines {
		// Skip EOL comment lines
		if opts.eol != 0 && len(line) > 0 && line[0] == opts.eol {
			continue
		}

		fields := splitByDelims(line, opts.delims)

		// Assign tokens to variables: %%a, %%b, %%c, ...
		varChar := baseVar[0] // 'A', 'B', etc.
		for i, tokIdx := range opts.tokens {
			varName := string(rune(varChar) + rune(i))
			if tokIdx == -1 {
				// Wildcard: rest of line from the last assigned token's position
				lastIdx := 0
				if i > 0 && opts.tokens[i-1] > 0 {
					lastIdx = opts.tokens[i-1]
				}
				// Rejoin remaining fields
				if lastIdx < len(fields) {
					ex.env.Set(varName, strings.Join(fields[lastIdx:], string(opts.delims[0:1])))
				} else {
					ex.env.Set(varName, "")
				}
			} else if tokIdx-1 < len(fields) {
				ex.env.Set(varName, fields[tokIdx-1])
			} else {
				ex.env.Set(varName, "")
			}
		}

		code = ex.RunStmts(s.Body, nil)
		if ex.shouldStop() {
			break
		}
	}
	return code
}

// --- SETLOCAL / ENDLOCAL ---

func (ex *Executor) execSetlocal(s *parser.SetlocalStatement) int {
	ex.env.Push()
	if s.EnableDelayedExpansion {
		ex.env.DelayedExpansion = true
	}
	if s.DisableDelayedExpansion {
		ex.env.DelayedExpansion = false
	}
	return 0
}

func (ex *Executor) execEndlocal() int {
	if !ex.env.Pop() {
		fmt.Fprintln(os.Stderr, "ENDLOCAL without matching SETLOCAL")
	}
	return 0
}

// --- EXIT ---

func (ex *Executor) execExit(s *parser.ExitStatement) int {
	if s.SubOnly {
		ex.exitPending = true
		return s.Code
	}
	os.Exit(s.Code)
	return s.Code
}

// --- SimpleCommand ---

func (ex *Executor) execSimple(s *parser.SimpleCommand) int {
	if len(s.Args) == 0 {
		return 0
	}

	// Expand all args
	args := make([]string, 0, len(s.Args))
	for _, part := range s.Args {
		args = append(args, ex.expandParts([]parser.WordPart{part}))
	}

	cmdName := strings.ToUpper(args[0])

	// Builtin?
	if fn, ok := builtins.Registry[cmdName]; ok {
		return fn(args[1:], ex.env)
	}

	// .bat file typed directly (e.g. "myscript.bat" or "myscript")
	if batPath, ok := ex.resolveBat(args[0]); ok {
		sub := &Executor{env: ex.env}
		return sub.RunFile(batPath, args[1:])
	}

	// External command
	return ex.runExternal(args, s.Redirects)
}

func (ex *Executor) runExternal(args []string, redirects []parser.Redirect) int {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Apply redirections (translate Windows nul → /dev/null)
	for i := range redirects {
		if strings.ToLower(redirects[i].File) == "nul" {
			redirects[i].File = "/dev/null"
		}
	}
	for _, r := range redirects {
		switch r.Op {
		case ">", "1>":
			f, err := os.Create(r.File)
			if err == nil {
				cmd.Stdout = f
				defer f.Close()
			}
		case ">>", "1>>":
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				cmd.Stdout = f
				defer f.Close()
			}
		case "2>":
			f, err := os.Create(r.File)
			if err == nil {
				cmd.Stderr = f
				defer f.Close()
			}
		case "2>>":
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				cmd.Stderr = f
				defer f.Close()
			}
		case "2>&1":
			cmd.Stderr = cmd.Stdout
		case "1>&2", ">&2":
			cmd.Stdout = cmd.Stderr
		case "<":
			f, err := os.Open(r.File)
			if err == nil {
				cmd.Stdin = f
				defer f.Close()
			}
		}
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "'%s' is not recognized as an internal or external command.\n", args[0])
		return 1
	}
	return 0
}

// joinBlocks merges multi-line parenthesized blocks into single lines.
// e.g. "if cond (\n  cmd1\n  cmd2\n)" becomes "if cond ( cmd1 & cmd2 )"
// countUnquotedParens counts ( and ) outside of quoted strings.
func countUnquotedParens(line string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
			}
		}
	}
	return depth
}

func joinBlocks(lines []scriptLine) []scriptLine {
	var result []scriptLine
	depth := 0
	var accum string

	for _, sl := range lines {
		if sl.label != "" {
			if depth > 0 {
				result = append(result, scriptLine{raw: accum})
				accum = ""
				depth = 0
			}
			result = append(result, sl)
			continue
		}

		line := sl.raw
		opens := countUnquotedParens(line)

		if depth == 0 && opens <= 0 {
			result = append(result, sl)
			continue
		}

		if depth == 0 {
			accum = line
			depth += opens
		} else {
			accum += " & " + strings.TrimSpace(line)
			depth += opens
		}

		if depth <= 0 {
			result = append(result, scriptLine{raw: accum})
			accum = ""
			depth = 0
		}
	}

	if accum != "" {
		result = append(result, scriptLine{raw: accum})
	}

	return result
}

// resolveBat checks if name refers to a .bat file (with or without extension).
// Returns the resolved path and true if found.
func (ex *Executor) resolveBat(name string) (string, bool) {
	candidates := []string{name}
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".bat") && !strings.HasSuffix(lower, ".cmd") {
		candidates = append(candidates, name+".bat", name+".cmd")
	}

	// Check CWD first
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}

	// Search PATH from our env (which BAT scripts can modify via SET)
	pathEnv := ex.env.Get("PATH")
	if pathEnv == "" {
		pathEnv = os.Getenv("PATH")
	}
	// BAT uses ; as PATH separator, Unix uses : — support both
	pathEnv = strings.ReplaceAll(pathEnv, ";", string(filepath.ListSeparator))
	for _, dir := range filepath.SplitList(pathEnv) {
		for _, c := range candidates {
			full := filepath.Join(dir, c)
			if _, err := os.Stat(full); err == nil {
				return full, true
			}
		}
	}
	return "", false
}

// --- Helpers ---

func (ex *Executor) expandParts(parts []parser.WordPart) string {
	return expander.ExpandWord(parts, ex.env, ex.positional)
}

// splitArgs splits a single expanded string into args by space.
// Respects quoted strings.
func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	var args []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			inQuote = !inQuote
			cur.WriteByte(ch)
		} else if ch == ' ' && !inQuote {
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		} else {
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// stripOuterQuotes removes surrounding double quotes if present.
func stripOuterQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
