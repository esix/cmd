// Package executor runs parsed BAT statements.
package executor

import (
	"bufio"
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

// scriptFile is a preprocessed BAT file with label index for O(1) GOTO.
type scriptFile struct {
	lines    []scriptLine
	labelIdx map[string]int // label name → line index
}

// Executor runs a list of statements with a program counter (for GOTO support).
type Executor struct {
	env        *env.Env
	positional []string // %0, %1, ... script arguments
	stmts      []parser.Statement // used by RunStmts (interactive / inline)
	lines      []scriptLine       // used by RunFile (lazy parsing)
	labelIdx   map[string]int     // label name → line index for O(1) GOTO
	pc         int                // current line or statement index
	gotoPending bool // true when GOTO was executed in a nested context
	exitPending bool // true when EXIT /B was executed in a nested context
	abortPending bool // true when the current batch file must terminate
	                  // (missing CALL/GOTO label, malformed IF — cmd.exe aborts the script)
	activeForVars []string // FOR variables currently in scope (innermost last)
}

// shouldStop returns true if GOTO, EXIT /B, or a script abort is pending.
func (ex *Executor) shouldStop() bool {
	return ex.gotoPending || ex.exitPending || ex.abortPending
}

// isActiveForVar reports whether name is a FOR variable of an enclosing loop.
func (ex *Executor) isActiveForVar(name string) bool {
	for _, v := range ex.activeForVars {
		if strings.EqualFold(v, name) {
			return true
		}
	}
	return false
}

// updatesErrorlevel reports whether a statement should update %ERRORLEVEL%
// after execution. Builtins like ECHO, SET, IF, FOR don't touch errorlevel
// in real cmd.exe; only external commands, CALL, and EXIT do.
func updatesErrorlevel(stmt parser.Statement) bool {
	switch stmt.(type) {
	case *parser.EchoStatement, *parser.SetStatement, *parser.IfStatement,
		*parser.ForStatement, *parser.SetlocalStatement, *parser.EndlocalStatement,
		*parser.LabelStatement, *parser.ShiftStatement, *parser.GotoStatement:
		return false
	}
	return true
}

// New creates an Executor.
func New(e *env.Env) *Executor {
	return &Executor{env: e}
}

// RunLine parses and runs a single line interactively.
func (ex *Executor) RunLine(line string) int {
	// A prior interactive command may have left an abort pending (e.g. a
	// missing GOTO label at the prompt); the REPL itself never terminates.
	ex.abortPending = false
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
			fmt.Print(strings.TrimSpace(line) + "\r\n")
		}
		fmt.Print("\r\n")
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
// fileCache is disabled to debug EXIT /B issues
var fileCache = map[string]*scriptFile{}

// RunFile executes a .bat file. Lines are parsed on-the-fly so that
// SETLOCAL EnableDelayedExpansion takes effect for subsequent lines.
func (ex *Executor) RunFile(path string, args []string) int {
	// Convert Windows backslash paths to Unix
	path = strings.ReplaceAll(path, "\\", "/")
	// Strip a drive-letter prefix (C:/foo → /foo) so Windows-style absolute
	// paths in scripts resolve on Unix.
	if len(path) >= 2 && path[1] == ':' &&
		((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		path = path[2:]
		if path == "" {
			path = "/"
		}
	}
	// Resolve to absolute path for cache key
	absPath, _ := filepath.Abs(path)

	if sf, cached := fileCache[absPath]; cached {
		return ex.runLines(sf.lines, sf.labelIdx, path, args)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open file: %s\n", path)
		return 1
	}

	// Pre-process lines: classify as label or code, skip blanks and REM
	rawLines := strings.Split(string(data), "\n")
	rawLines = joinCaretContinuations(rawLines)
	var slines []scriptLine
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

	// Build label index for O(1) GOTO
	labelIdx := make(map[string]int, 32)
	for i, sl := range slines {
		if sl.label != "" {
			labelIdx[sl.label] = i
		}
	}

	fileCache[absPath] = &scriptFile{lines: slines, labelIdx: labelIdx}
	return ex.runLines(slines, labelIdx, path, args)
}


func (ex *Executor) runLines(slines []scriptLine, labelIdx map[string]int, path string, args []string) int {
	ex.env.FileMode = true
	savedPos := ex.positional
	ex.positional = append([]string{path}, args...)

	savedLines := ex.lines
	savedLabels := ex.labelIdx
	savedPC := ex.pc
	ex.lines = slines
	ex.labelIdx = labelIdx
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
			if updatesErrorlevel(stmt) {
				ex.env.ExitCode = code
			}
			if ex.abortPending {
				// cmd.exe terminates the current batch file on a missing
				// CALL/GOTO label or a malformed IF; control returns to the
				// caller with errorlevel 1.
				ex.abortPending = false
				ex.pc = len(ex.lines)
				code = 1
				ex.env.ExitCode = 1
				break
			}
			if ex.gotoPending {
				ex.gotoPending = false
				break
			}
			if ex.exitPending {
				ex.exitPending = false
				ex.pc = len(ex.lines)
				break
			}
			// Also check if pc was moved past end by EXIT /B in a chain
			if ex.pc >= len(ex.lines) {
				break
			}
		}
	}

	ex.lines = savedLines
	ex.labelIdx = savedLabels
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
	// Only override positional when a fresh set is supplied (e.g. CALL).
	// When nil, we share the parent's positional so SHIFT inside an IF/FOR
	// block persists to the enclosing scope (matches cmd.exe behavior).
	overrodePositional := positional != nil
	if overrodePositional {
		ex.positional = positional
	}

	code := 0
	for ex.pc < len(ex.stmts) {
		stmt := ex.stmts[ex.pc]
		ex.pc++
		code = ex.execute(stmt)
		if updatesErrorlevel(stmt) {
			ex.env.ExitCode = code
		}
		if ex.shouldStop() {
			break
		}
	}

	ex.stmts = saved
	if !ex.gotoPending && !ex.exitPending {
		ex.pc = savedPC
	}
	// Restore positional only if we overrode it; otherwise leave any SHIFT
	// performed by the block in effect.
	if overrodePositional {
		ex.positional = savedPos
	}
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

	// Run stages sequentially, buffering each stage's output and feeding it
	// to the next stage's stdin. Every stage goes through the normal execute
	// path on a CHILD executor, so builtins (TYPE, FINDSTR, ECHO, SET, ...)
	// work inside pipelines and flow control (GOTO/EXIT/abort) never leaks
	// out — cmd.exe runs pipe segments in child interpreters.
	var input []byte
	code := 0
	for i, stmt := range s.Commands {
		last := i == len(s.Commands)-1
		code, input = ex.runPipeStage(stmt, input, last)
	}
	return code
}

// internalForFSources lists first words of FOR /F ('command') sources that
// must run through the port's own engine rather than sh — sh either lacks
// them or (like `set`) shadows them with an incompatible shell builtin.
var internalForFSources = map[string]bool{
	"SET": true, "ECHO": true, "TYPE": true, "FINDSTR": true,
	"VER": true, "CALL": true, "IF": true, "FOR": true,
}

// runInternalCapture executes a FOR /F ('command') source via a child
// executor with stdout captured (same technique as runPipeStage) when its
// first word is an internal command. Returns ok=false for anything else so
// the caller can fall back to sh.
func (ex *Executor) runInternalCapture(cmdStr string) (string, bool) {
	trimmed := strings.TrimSpace(cmdStr)
	first := trimmed
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		first = trimmed[:i]
	}
	upper := strings.ToUpper(first)
	if strings.HasPrefix(upper, "ECHO.") || strings.HasPrefix(upper, "ECHO(") {
		upper = "ECHO"
	}
	if !internalForFSources[upper] {
		if _, ok := builtins.Registry[upper]; !ok {
			return "", false
		}
	}
	stmts, err := parser.ParseLineWithOpts(cmdStr, ex.env.DelayedExpansion)
	if err != nil {
		return "", false
	}
	savedOut := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		return "", false
	}
	os.Stdout = outW
	var captured []byte
	done := make(chan struct{})
	go func() { captured, _ = io.ReadAll(outR); close(done) }()
	sub := &Executor{env: ex.env, positional: ex.positional}
	for _, st := range stmts {
		sub.execute(st)
	}
	os.Stdout = savedOut
	outW.Close()
	<-done
	outR.Close()
	return string(captured), true
}

// runPipeStage executes one pipeline segment. stdin is fed from the previous
// segment's buffered output; a non-final segment's stdout is captured and
// returned, the final segment writes to the real stdout.
func (ex *Executor) runPipeStage(stmt parser.Statement, input []byte, last bool) (int, []byte) {
	savedIn, savedOut := os.Stdin, os.Stdout

	// Feed the previous stage's output as this stage's stdin.
	inR, inW, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pipe error: %v\n", err)
		return 1, nil
	}
	go func() {
		inW.Write(input) //nolint:errcheck — reader may stop early
		inW.Close()
	}()
	os.Stdin = inR

	var outR, outW *os.File
	var captured []byte
	done := make(chan struct{})
	if last {
		close(done)
	} else {
		outR, outW, err = os.Pipe()
		if err != nil {
			os.Stdin = savedIn
			inR.Close()
			fmt.Fprintf(os.Stderr, "Pipe error: %v\n", err)
			return 1, nil
		}
		os.Stdout = outW
		go func() {
			captured, _ = io.ReadAll(outR)
			close(done)
		}()
	}

	sub := &Executor{env: ex.env, positional: ex.positional}
	code := sub.execute(stmt)

	os.Stdin = savedIn
	os.Stdout = savedOut
	inR.Close()
	if !last {
		outW.Close()
		<-done
		outR.Close()
	}
	return code, captured
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
		if updatesErrorlevel(stmt) {
			ex.env.ExitCode = code
		}
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
		fmt.Print("\r\n")
		return 0
	}
	if s.TurnOn != nil {
		ex.env.Echo = *s.TurnOn
		return 0
	}

	// Handle redirections (e.g. ECHO error 1>&2)
	out := os.Stdout
	for _, r := range s.Redirects {
		file := cleanRedirectFile(r.File, ex.env)
		switch r.Op {
		case "1>&2", ">&2":
			out = os.Stderr
		case ">", "1>":
			f, err := os.Create(file)
			if err == nil {
				out = f
			}
		case ">>", "1>>":
			f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				out = f
			}
		}
	}

	// Prefer verbatim text (preserves leading/internal spacing) when available.
	if s.HasRaw {
		// Expand !VAR! (delayed) and %%X FOR variables that survive line-level
		// %VAR% expansion. ExpandName does bangs then nested-percent expansion.
		text := expander.ExpandName(s.RawText, ex.env)
		fmt.Fprint(out, text+"\r\n")
		return 0
	}

	words := make([]string, 0, len(s.Args))
	for _, group := range s.Args {
		words = append(words, ex.expandParts(group))
	}
	if len(words) == 0 {
		if ex.env.Echo {
			fmt.Fprint(out, "ECHO is on.\r\n")
		} else {
			fmt.Fprint(out, "ECHO is off.\r\n")
		}
		return 0
	}
	fmt.Fprint(out, strings.Join(words, " ")+"\r\n")
	return 0
}

// --- SET ---

func (ex *Executor) execSet(s *parser.SetStatement) int {
	// A bare `set` (no name, no /P, no /A) lists all variables. But
	// `set /p "=prompt"` has an empty name on purpose (the print-no-newline
	// idiom `<nul set /p "=text"`) — that must NOT trigger the listing.
	if s.Name == "" && !s.Prompt && !s.Arithmetic {
		return builtins.Set(nil, ex.env)
	}
	// The variable name may contain %%X (FOR var) / %VAR% / !VAR! references
	// (e.g. `set "%%a=%%b"` inside a FOR body). Resolve them first.
	name := expander.ExpandName(s.Name, ex.env)
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
		return builtins.Set([]string{"/A", name + "=" + value}, ex.env)
	}

	if s.Prompt {
		// SET /P var=prompt — read a line from stdin (or redirected file).
		// Output redirects (e.g. `> file <nul set /p "=text"`) must capture the
		// prompt, so apply them around the prompt print. cmd.exe always prints
		// the prompt (even with redirected stdin) and strips its leading
		// whitespace.
		prompt := value
		cleanup := ex.applyRedirects(s.Redirects)
		promptOut := strings.TrimLeft(prompt, " \t")
		if promptOut != "" {
			fmt.Print(promptOut)
		}
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		cleanup()
		// Empty name (e.g. `<nul set /p "=text"`) is prompt-only: don't assign.
		if name == "" {
			return 0
		}
		if err != nil && line == "" {
			ex.env.Unset(name)
		} else {
			ex.env.Set(name, line)
		}
		return 0
	}

	if !s.HasEquals {
		// SET NAME without = → display the variable
		return builtins.Set([]string{name}, ex.env)
	}
	if value == "" {
		ex.env.Unset(name)
	} else {
		ex.env.Set(name, value)
	}
	return 0
}

// --- IF ---

func (ex *Executor) execIf(s *parser.IfStatement) int {
	result := ex.evalCondition(s.Condition)
	if ex.abortPending {
		// Malformed condition (e.g. empty unquoted operand): the script is
		// aborting — run neither branch.
		return 1
	}
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
		if c.CaseInsensitive {
			return strings.EqualFold(left, right)
		}
		return left == right

	case *parser.NumericCompare:
		leftRaw := ex.expandParts(c.Left)
		rightRaw := ex.expandParts(c.Right)
		// An UNQUOTED operand that expands to nothing malforms the IF on real
		// cmd.exe: it prints `<token> was unexpected at this time.` and aborts
		// the batch script. (A quoted empty operand `""` stays a valid token
		// and falls through to string comparison below.)
		if strings.TrimSpace(leftRaw) == "" || strings.TrimSpace(rightRaw) == "" {
			other := strings.TrimSpace(rightRaw)
			if other == "" {
				other = strings.TrimSpace(leftRaw)
			}
			if other == "" {
				other = c.Op
			}
			fmt.Fprintf(os.Stderr, "%s was unexpected at this time.\n", other)
			ex.abortPending = true
			return false
		}
		left := stripOuterQuotes(leftRaw)
		right := stripOuterQuotes(rightRaw)
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
		return ex.env.Get(ex.expandParts(c.Name)) != ""

	case *parser.ExistCondition:
		path := ex.expandParts(c.Path)
		path = stripOuterQuotes(path)
		// Convert backslashes to forward slashes for OS calls
		path = strings.ReplaceAll(path, "\\", "/")
		// Strip drive letter
		if len(path) >= 2 && path[1] == ':' {
			if len(path) >= 3 && path[2] == '/' {
				path = path[2:]
			} else {
				path = path[2:]
			}
		}
		// Trailing slash means directory
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "."
		}
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

	// Search in file lines using label index (O(1) lookup)
	if ex.lines != nil {
		if idx, ok := ex.labelIdx[label]; ok {
			ex.pc = idx + 1
			ex.gotoPending = true
			return 0
		}
		// Fallback: linear scan (belt and braces if the index missed it)
		for i, sl := range ex.lines {
			if sl.label == label {
				ex.pc = i + 1
				ex.gotoPending = true
				return 0
			}
		}
		return ex.missingLabel(strings.TrimPrefix(raw, ":"))
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
	return ex.missingLabel(strings.TrimPrefix(raw, ":"))
}

// missingLabel reports a CALL/GOTO target that doesn't exist and aborts the
// current batch file — cmd.exe terminates the script (control returns to the
// caller) with errorlevel 1.
func (ex *Executor) missingLabel(label string) int {
	fmt.Fprintf(os.Stderr, "The system cannot find the batch label specified - %s\n", label)
	ex.abortPending = true
	return 1
}

// --- CALL ---

func (ex *Executor) execCall(s *parser.CallStatement) int {
	if len(s.Args) == 0 {
		return 0
	}
	// Apply any output redirections for the whole CALL (e.g. CALL foo > out).
	if len(s.Redirects) > 0 {
		cleanup := ex.applyRedirects(s.Redirects)
		defer cleanup()
	}
	// The call target is dequoted (cmd.exe %~0-style): `call "path with spaces"`
	// must open the file, not a name with literal quotes. Positional args keep
	// their quotes so %1 vs %~1 still differ inside the callee.
	first := stripOuterQuotes(ex.expandParts(s.Args[0]))

	// Expand remaining args as positional params for the call. An unquoted
	// expansion that yields spaces (e.g. `call :x !list!` where list="2 3")
	// splits into multiple positional params, matching cmd.exe; quoted
	// expansions stay a single arg (splitArgs respects quotes).
	var callArgs []string
	callArgs = append(callArgs, first) // %0 = label or script name
	for _, arg := range s.Args[1:] {
		expanded := ex.expandParts(arg)
		if parts := splitArgs(expanded); len(parts) > 0 {
			callArgs = append(callArgs, parts...)
		} else {
			callArgs = append(callArgs, expanded)
		}
	}

	// CALL :label — subroutine within same file
	if strings.HasPrefix(first, ":") {
		label := strings.ToUpper(first[1:])
		savedPC := ex.pc
		savedPos := ex.positional
		ex.positional = callArgs

		if ex.lines != nil {
			if idx, ok := ex.labelIdx[label]; ok {
					ex.pc = idx + 1
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
								code = ex.execExit(exitStmt)
								done = true
								break
							}
							code = ex.execute(stmt)
							if ex.shouldStop() {
								break
							}
						}
						if ex.abortPending {
							// Missing label / malformed IF inside the subroutine:
							// propagate so the whole file unwinds (cmd.exe).
							break
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
		} else {
			for i, stmt := range ex.stmts {
				if lbl, ok := stmt.(*parser.LabelStatement); ok && lbl.Name == label {
					ex.pc = i + 1
					code := 0
					for ex.pc < len(ex.stmts) {
						stmt := ex.stmts[ex.pc]
						ex.pc++
						if exitStmt, ok := stmt.(*parser.ExitStatement); ok && exitStmt.SubOnly {
							code = ex.execExit(exitStmt)
							ex.exitPending = false
							break
						}
						code = ex.execute(stmt)
						if ex.abortPending {
							break
						}
					}
					ex.pc = savedPC
					ex.positional = savedPos
					return code
				}
			}
		}
		ex.positional = savedPos
		return ex.missingLabel(first[1:])
	}

	// CALL of an internal command (call set, call echo, ...) re-dispatches to
	// the builtin with one extra round of %-expansion — the classic batch
	// double-expansion idiom: `call set "y=%%x%%"` assigns the value of x.
	if code, handled := ex.callBuiltin(first, s); handled {
		return code
	}

	// CALL script.bat [args...]
	scriptPath := first
	if resolved, ok := ex.resolveBat(scriptPath); ok {
		scriptPath = resolved
	}
	// Reuse the already-split positional args (callArgs[0] is the script name).
	scriptArgs := callArgs[1:]

	sub := &Executor{env: ex.env}
	return sub.RunFile(scriptPath, scriptArgs)
}

// callBuiltin handles CALL of an internal command. cmd.exe restarts the
// command line with one extra round of variable expansion: `%%` collapses to
// `%`, then delayed !VAR! refs and a second %VAR% pass apply. Returns
// handled=false when the target isn't an internal command (so CALL falls
// through to script resolution).
//
// Exception: `call set <args>` with no `=` and no switch is NOT dispatched to
// the builtin — plain-name args can't be a SET assignment, and batch suites
// in the wild (e.g. gw-batsic's stl module) use `call set <verb> ...` to
// invoke a script named set.bat from PATH.
func (ex *Executor) callBuiltin(first string, s *parser.CallStatement) (int, bool) {
	upper := strings.ToUpper(first)
	lookup := upper
	// `call echo.` / `call echo(text` — the echo idioms glue onto the word.
	if strings.HasPrefix(upper, "ECHO.") || strings.HasPrefix(upper, "ECHO(") {
		lookup = "ECHO"
	}
	if _, isBuiltin := builtins.Registry[lookup]; !isBuiltin {
		return 0, false
	}
	text := s.RawText
	if text == "" {
		// Token-only parse (no raw line available): best-effort reassembly.
		var parts []string
		for _, arg := range s.Args {
			parts = append(parts, ex.expandParts(arg))
		}
		text = strings.Join(parts, " ")
	} else {
		text = ex.expandCallText(text)
	}

	if upper == "SET" {
		rest := ""
		if idx := strings.IndexAny(text, " \t"); idx >= 0 {
			rest = strings.TrimSpace(text[idx+1:])
		}
		// `call set name=value`, `call set /A ...`, `call set` (list all) and
		// `call set NAME` (single word: variable query) go to the builtin.
		// Multiple plain words can't be a valid SET invocation — they're a
		// script named set.bat on PATH (gw-batsic's stl module does this).
		looksLikeSet := rest == "" || strings.ContainsRune(rest, '=') ||
			strings.HasPrefix(rest, "/") || !strings.ContainsAny(rest, " \t")
		if !looksLikeSet {
			return 0, false
		}
	}

	// Bangs were already resolved by the extra expansion round; parse with
	// delayed expansion off so values containing '!' aren't re-expanded.
	stmts, err := parser.ParseLineWithOpts(text, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
		return 1, true
	}
	code := 0
	for _, stmt := range stmts {
		code = ex.execute(stmt)
		if ex.shouldStop() {
			break
		}
	}
	return code, true
}

// expandCallText performs CALL's extra expansion round over the raw command
// text: %%X of an enclosing FOR loop substitutes the loop value, other %%
// pairs collapse to a single %, delayed !VAR! refs expand, then a second
// %VAR% expansion pass runs (this is what makes `%%x%%` reach SET as the
// value of x).
func (ex *Executor) expandCallText(text string) string {
	var sb strings.Builder
	i := 0
	for i < len(text) {
		if text[i] == '%' && i+1 < len(text) && text[i+1] == '%' {
			if i+2 < len(text) {
				ch := text[i+2]
				isLetter := (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
				nextAlnum := i+3 < len(text) &&
					((text[i+3] >= 'a' && text[i+3] <= 'z') ||
						(text[i+3] >= 'A' && text[i+3] <= 'Z') ||
						(text[i+3] >= '0' && text[i+3] <= '9') || text[i+3] == '_')
				if isLetter && !nextAlnum && ex.isActiveForVar(string(ch)) {
					sb.WriteString(ex.env.Get(string(ch)))
					i += 3
					continue
				}
			}
			sb.WriteByte('%')
			i += 2
			continue
		}
		sb.WriteByte(text[i])
		i++
	}
	out := sb.String()
	if ex.env.DelayedExpansion {
		out = expander.ExpandBangs(out, ex.env)
	}
	return expander.ExpandPercent(out, ex.env, ex.positional)
}

// --- FOR ---

func (ex *Executor) execFor(s *parser.ForStatement) int {
	// Track the loop variable so CALL's extra expansion round can tell a
	// FOR-variable ref (%%i) apart from the %%-escape in `call set "y=%%x%%"`.
	depth := len(ex.activeForVars)
	ex.activeForVars = append(ex.activeForVars, s.Variable)
	defer func() { ex.activeForVars = ex.activeForVars[:depth] }()

	switch s.Kind {
	case parser.ForRange:
		return ex.execForRange(s)
	case parser.ForInList:
		return ex.execForInList(s)
	case parser.ForTokens:
		return ex.execForTokens(s)
	case parser.ForDirs:
		return ex.execForDirs(s)
	case parser.ForRecursive:
		return ex.execForRecursive(s)
	default:
		fmt.Fprintf(os.Stderr, "FOR variant not yet implemented\n")
		return 1
	}
}

// execForDirs handles FOR /D %%v IN (pattern) DO ...
// Iterates over directories matching the pattern.
func (ex *Executor) execForDirs(s *parser.ForStatement) int {
	code := 0
	for _, item := range s.InList {
		// Expand !VAR! and strip outer quotes; convert backslashes for OS calls
		item = expander.ExpandBangs(item, ex.env)
		item = stripOuterQuotes(item)
		pattern := strings.ReplaceAll(item, "\\", "/")
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || !info.IsDir() {
				continue
			}
			ex.env.Set(s.Variable, m)
			code = ex.RunStmts(s.Body, nil)
			if ex.shouldStop() {
				return code
			}
		}
	}
	return code
}

// execForRecursive handles FOR /R [path] %%v IN (pattern) DO ...
// Walks the directory tree and matches files against pattern in each subdirectory.
func (ex *Executor) execForRecursive(s *parser.ForStatement) int {
	root := s.RootPath
	if root == "" {
		root = "."
	}
	root = strings.ReplaceAll(root, "\\", "/")
	root = strings.Trim(root, "\"")

	code := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		for _, item := range s.InList {
			pattern := filepath.Join(path, strings.ReplaceAll(item, "\\", "/"))
			matches, _ := filepath.Glob(pattern)
			for _, m := range matches {
				ex.env.Set(s.Variable, m)
				code = ex.RunStmts(s.Body, nil)
				if ex.shouldStop() {
					return filepath.SkipDir
				}
			}
		}
		return nil
	})
	_ = err
	return code
}

func (ex *Executor) execForRange(s *parser.ForStatement) int {
	// IN (start,step,end)
	if len(s.InList) < 3 {
		fmt.Fprintf(os.Stderr, "FOR /L requires (start,step,end)\n")
		return 1
	}
	parseI := func(v string) int {
		// Range bounds may be delayed-expansion refs (e.g. FOR /L in (1,1,!n!)).
		// %VAR% is already expanded at line level; expand any !VAR! here.
		v = expander.ExpandBangs(v, ex.env)
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
		if ex.abortPending {
			break
		}
	}
	return code
}

func (ex *Executor) execForInList(s *parser.ForStatement) int {
	code := 0
	for _, rawItem := range s.InList {
		// Expand delayed-expansion (!VAR!) then %%X FOR-variable refs, so an
		// inner loop like `for %%p in (%%j)` sees the outer loop's value.
		item := expander.ExpandBangs(rawItem, ex.env)
		item = expander.ExpandForVars(item, ex.env)
		// Item may now contain spaces from expansion; split into multiple items
		// so that "for %%i in (!list!)" iterates each token of !list!.
		// Empty expansion → no iterations.
		if strings.TrimSpace(item) == "" {
			continue
		}
		// A fully double-quoted item is one element even if it contains spaces
		// (e.g. for %%f in ("my dir/*.bat")). Otherwise spaces separate items
		// (e.g. for %%i in (!list!) where list expanded to "a b c").
		var items []string
		if strings.HasPrefix(item, "\"") && strings.HasSuffix(item, "\"") && len(item) >= 2 {
			items = []string{item}
		} else if strings.ContainsAny(item, " \t") {
			items = strings.Fields(item)
		} else {
			items = []string{item}
		}
		for _, it := range items {
			// A glob element (containing * or ?) is expanded against the
			// filesystem. cmd.exe globs only wildcard elements; a no-match
			// wildcard yields zero iterations (not the literal).
			if strings.ContainsAny(it, "*?") {
				// Strip surrounding quotes and normalize backslashes so the
				// pattern is a valid filesystem glob.
				pat := strings.ReplaceAll(stripOuterQuotes(it), "\\", "/")
				matches, _ := filepath.Glob(pat)
				for _, m := range matches {
					// Plain FOR matches files only (FOR /D matches directories).
					if info, err := os.Stat(m); err == nil && info.IsDir() {
						continue
					}
					ex.env.Set(s.Variable, m)
					code = ex.RunStmts(s.Body, nil)
					if ex.shouldStop() {
						return code
					}
				}
			} else {
				// Literal element: cmd.exe keeps surrounding quotes on the
				// FOR variable (for %%i in ("a b") → %%i is "a b").
				ex.env.Set(s.Variable, it)
				code = ex.RunStmts(s.Body, nil)
				if ex.shouldStop() {
					return code
				}
			}
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
	// Custom split: keys are space-separated, but the value of `delims=`
	// is everything from `=` to the next space or end (preserves a literal
	// space delimiter as in `delims= `).
	i := 0
	for i < len(optStr) {
		// Skip leading whitespace between keys
		for i < len(optStr) && (optStr[i] == ' ' || optStr[i] == '\t') {
			i++
		}
		if i >= len(optStr) {
			break
		}
		// Read the key up to '=' or whitespace
		keyStart := i
		for i < len(optStr) && optStr[i] != '=' && optStr[i] != ' ' && optStr[i] != '\t' {
			i++
		}
		key := strings.ToLower(optStr[keyStart:i])
		if key == "usebackq" {
			opts.usebackq = true
			continue
		}
		// If we stopped at '=', read the value
		if i < len(optStr) && optStr[i] == '=' {
			i++ // skip =
			valStart := i
			// For delims=, take everything up to the next non-delim option key
			// pattern (i.e., space followed by a known keyword) or end.
			if key == "delims" {
				// Scan to end-of-string or until we see ` <key>=` pattern
				for i < len(optStr) {
					if optStr[i] == ' ' || optStr[i] == '\t' {
						// Look ahead: next non-space starts a known keyword?
						j := i + 1
						for j < len(optStr) && (optStr[j] == ' ' || optStr[j] == '\t') {
							j++
						}
						kStart := j
						for j < len(optStr) && optStr[j] != '=' && optStr[j] != ' ' && optStr[j] != '\t' {
							j++
						}
						kw := strings.ToLower(optStr[kStart:j])
						if kw == "tokens" || kw == "eol" || kw == "usebackq" || kw == "skip" {
							break
						}
					}
					i++
				}
				opts.delims = optStr[valStart:i]
				continue
			}
			// Other values: read until next whitespace
			for i < len(optStr) && optStr[i] != ' ' && optStr[i] != '\t' {
				i++
			}
			val := optStr[valStart:i]
			switch key {
			case "tokens":
				opts.tokens = parseTokenSpec(val)
			case "eol":
				if len(val) > 0 {
					opts.eol = val[0]
				}
			}
		}
	}
	return opts
}

// parseTokenSpec parses a FOR /F tokens= spec into 1-based token indices.
// Supports single numbers, comma lists, and M-N ranges, optionally with a
// trailing * (rest-of-line) marker, e.g. "1", "1,3,5", "1-4", "2-3,7", "1,2*".
// -1 represents the wildcard (*). Malformed parts are skipped (never producing
// a 0/-negative index that would later index the field slice out of range).
func parseTokenSpec(spec string) []int {
	const maxToken = 128 // cmd.exe caps tokens well below this; guards huge ranges
	var result []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		wildcard := strings.HasSuffix(part, "*")
		part = strings.TrimSuffix(part, "*")
		if part != "" {
			if dash := strings.IndexByte(part, '-'); dash > 0 {
				// M-N range → M, M+1, ..., N
				lo, err1 := strconv.Atoi(strings.TrimSpace(part[:dash]))
				hi, err2 := strconv.Atoi(strings.TrimSpace(part[dash+1:]))
				if err1 == nil && err2 == nil && lo >= 1 && hi >= lo {
					if hi > maxToken {
						hi = maxToken
					}
					for n := lo; n <= hi; n++ {
						result = append(result, n)
					}
				}
			} else if n, err := strconv.Atoi(part); err == nil && n >= 1 {
				result = append(result, n)
			}
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

// stripNulRedirect removes a trailing Windows null redirect (`2>nul`, `>nul`)
// from a command string so it isn't passed to sh (which would create a file).
func stripNulRedirect(cmd string) string {
	for _, suffix := range []string{" 2>nul", " >nul", " 1>nul", " 2> nul", " > nul"} {
		if idx := strings.Index(strings.ToLower(cmd), suffix); idx != -1 {
			cmd = cmd[:idx] + cmd[idx+len(suffix):]
		}
	}
	return strings.TrimSpace(cmd)
}

// runDirCommand handles Windows `dir` invocations captured by `for /f`
// (notably `dir /A-D /S /B pattern`). Returns the newline-joined output and
// true if the command was a `dir` we could handle. Delegates to
// builtins.DirList so the standalone `dir` builtin and this path agree.
func runDirCommand(cmdStr string) (string, bool) {
	fields := splitArgs(cmdStr)
	if len(fields) == 0 || strings.ToLower(fields[0]) != "dir" {
		return "", false
	}
	lines, _, ok := builtins.DirList(fields[1:])
	if !ok {
		return "", false
	}
	return strings.Join(lines, "\n"), true
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
			// Strip a trailing `2>nul` / `>nul` (Windows null redirect) — sh
			// would otherwise create a literal "nul" file.
			cmdStr = stripNulRedirect(cmdStr)
			// Convert Windows path separators (\) to / so sh doesn't treat
			// them as escape sequences (e.g. `sort "%TEMP%\file"`).
			cmdStr = strings.ReplaceAll(cmdStr, "\\", "/")
			// Handle Windows `dir` internally; sh has no such command.
			if out, ok := runDirCommand(cmdStr); ok {
				lines = strings.Split(strings.TrimRight(out, "\r\n"), "\n")
			} else if out, ok := ex.runInternalCapture(cmdStr); ok {
				// Internal commands — notably `set <prefix>` enumeration —
				// must run in the port's own engine: sh's `set` is a
				// different builtin and would capture nothing.
				lines = strings.Split(strings.TrimRight(out, "\r\n"), "\n")
			} else {
				out, err := exec.Command("sh", "-c", cmdStr).Output()
				if err == nil {
					lines = strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
				}
			}
			// Strip trailing \r on each line (the captured output may carry
			// CRLF line endings from files written by `echo`).
			for i, ln := range lines {
				lines[i] = strings.TrimRight(ln, "\r")
			}
		} else if strings.HasPrefix(source, "\"") && strings.HasSuffix(source, "\"") {
			if opts.usebackq {
				// usebackq + "..." = read from file
				filename := source[1 : len(source)-1]
				filename = strings.ReplaceAll(filename, "\\", "/")
				data, err := os.ReadFile(filename)
				if err == nil {
					lines = strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
				// Strip trailing \r on each line (CRLF line endings)
				for i, ln := range lines {
					lines[i] = strings.TrimRight(ln, "\r")
				}
				}
			} else {
				// "string" = parse the string directly
				str := source[1 : len(source)-1]
				lines = []string{str}
			}
		} else {
			// Bare filename
			source = strings.ReplaceAll(source, "\\", "/")
			data, err := os.ReadFile(source)
			if err == nil {
				lines = strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
				// Strip trailing \r on each line (CRLF line endings)
				for i, ln := range lines {
					lines[i] = strings.TrimRight(ln, "\r")
				}
			}
		}
	}

	code := 0
	baseVar := strings.ToUpper(s.Variable)
	if baseVar == "" {
		// Malformed FOR /F without a loop variable.
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}

	// Register the derived token variables (%%a → %%a,%%b,%%c for tokens=1,2,3)
	// as active FOR variables for CALL's extra expansion round.
	depth := len(ex.activeForVars)
	for i := range opts.tokens {
		if i == 0 {
			continue // base variable already registered by execFor
		}
		ex.activeForVars = append(ex.activeForVars, string(rune(baseVar[0])+rune(i)))
	}
	defer func() { ex.activeForVars = ex.activeForVars[:depth] }()

	for _, line := range lines {
		// FOR /F skips blank lines entirely (matches cmd.exe).
		if strings.TrimSpace(line) == "" {
			continue
		}
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
			} else if tokIdx >= 1 && tokIdx-1 < len(fields) {
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
	code := s.Code
	if len(s.CodeParts) > 0 {
		val := strings.TrimSpace(ex.expandParts(s.CodeParts))
		if n, err := strconv.Atoi(val); err == nil {
			code = n
		}
	}
	if s.SubOnly {
		ex.exitPending = true
		return code
	}
	os.Exit(code)
	return code
}

// --- SimpleCommand ---

func (ex *Executor) execSimple(s *parser.SimpleCommand) int {
	if len(s.Args) == 0 {
		return 0
	}

	// Expand all args and strip quotes
	args := make([]string, 0, len(s.Args))
	for _, part := range s.Args {
		a := ex.expandParts([]parser.WordPart{part})
		a = strings.Trim(a, "\"")
		args = append(args, a)
	}

	cmdName := strings.ToUpper(args[0])

	// Convert backslash paths — except for FINDSTR, whose patterns use \ as a
	// regex escape (\< \>); it converts its own file args internally.
	if cmdName != "FINDSTR" {
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "\\", "/")
		}
	}

	// Builtin?
	if fn, ok := builtins.Registry[cmdName]; ok {
		// Apply redirections for builtins (e.g. cmd /C >file echo text)
		cleanup := ex.applyRedirects(s.Redirects)
		code := fn(args[1:], ex.env)
		cleanup()
		return code
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
	// Convert backslash paths
	args[0] = strings.ReplaceAll(args[0], "\\", "/")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Normalize redirect file paths
	for i := range redirects {
		redirects[i].File = cleanRedirectFile(redirects[i].File, ex.env)
	}
	for _, r := range redirects {
		switch r.Op {
		case ">", "1>":
			f, err := os.Create(r.File)
			if err == nil {
				cmd.Stdout = f
			}
		case ">>", "1>>":
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				cmd.Stdout = f
			}
		case "2>":
			f, err := os.Create(r.File)
			if err == nil {
				cmd.Stderr = f
			}
		case "2>>":
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				cmd.Stderr = f
			}
		case "2>&1":
			cmd.Stderr = cmd.Stdout
		case "1>&2", ">&2":
			cmd.Stdout = cmd.Stderr
		case "<":
			f, err := os.Open(r.File)
			if err == nil {
				cmd.Stdin = f
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
// Excludes echo( pattern where ( is not a block delimiter.
func countUnquotedParens(line string) int {
	depth := 0
	inQuote := false
	upper := strings.ToUpper(line)
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				// Skip echo( pattern — the ( is part of echo, not a block
				if i >= 4 && upper[i-4:i] == "ECHO" {
					continue
				}
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

// joinCaretContinuations merges lines that end with an unescaped caret (^),
// the BAT line-continuation marker. A trailing `^` (after stripping CR) means
// the next physical line is part of the same logical line.
func joinCaretContinuations(lines []string) []string {
	var result []string
	i := 0
	for i < len(lines) {
		line := strings.TrimRight(lines[i], "\r")
		// Count trailing carets; an odd count means the last one is a
		// continuation (an even count is escaped carets `^^`).
		for endsWithContinuationCaret(line) && i+1 < len(lines) {
			line = line[:len(line)-1] // drop the trailing ^
			i++
			next := strings.TrimRight(lines[i], "\r")
			line += next
		}
		result = append(result, line)
		i++
	}
	return result
}

// endsWithContinuationCaret reports whether the line ends with an odd number of
// carets (so the final caret is a line-continuation, not an escaped `^^`).
func endsWithContinuationCaret(line string) bool {
	n := 0
	for j := len(line) - 1; j >= 0 && line[j] == '^'; j-- {
		n++
	}
	return n%2 == 1
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
			// Use \x01 as a line-boundary marker; the parser treats it as a
			// statement separator that does NOT get consumed into IF/FOR body.
			accum += "\x01" + strings.TrimSpace(line)
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
	name = strings.ReplaceAll(name, "\\", "/")
	// Strip a drive-letter prefix (C:/foo → /foo) like RunFile does, so
	// extension probing works for Windows-style absolute paths.
	if len(name) >= 2 && name[1] == ':' &&
		((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) {
		name = name[2:]
		if name == "" {
			name = "/"
		}
	}
	lower := strings.ToLower(name)
	hasBatExt := strings.HasSuffix(lower, ".bat") || strings.HasSuffix(lower, ".cmd")

	// Only check the exact name if it already has a .bat/.cmd extension
	var candidates []string
	if hasBatExt {
		candidates = []string{name}
	} else {
		candidates = []string{name + ".bat", name + ".cmd"}
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
		// Convert Windows-style backslashes to forward slashes for OS calls
		dir = strings.ReplaceAll(dir, "\\", "/")
		for _, c := range candidates {
			full := filepath.Join(dir, c)
			if _, err := os.Stat(full); err == nil {
				return full, true
			}
		}
	}
	return "", false
}

// cleanRedirectFile normalizes a redirect target path.
func cleanRedirectFile(file string, e *env.Env) string {
	file = strings.Trim(file, "\"")
	// Expand !VAR! in redirect paths when delayed expansion is on
	if e != nil && e.DelayedExpansion {
		file = expander.ExpandBangs(file, e)
	}
	// Convert backslashes AFTER expansion (expanded path may contain \)
	file = strings.ReplaceAll(file, "\\", "/")
	if strings.ToLower(file) == "nul" {
		file = "/dev/null"
	}
	return file
}

// applyRedirects temporarily redirects os.Stdout/Stderr for builtin commands.
// Returns a cleanup function that restores the originals.
func (ex *Executor) applyRedirects(redirects []parser.Redirect) func() {
	if len(redirects) == 0 {
		return func() {}
	}

	savedOut := os.Stdout
	savedErr := os.Stderr
	savedIn := os.Stdin
	var files []*os.File

	for _, r := range redirects {
		file := cleanRedirectFile(r.File, ex.env)
		switch r.Op {
		case ">", "1>":
			f, err := os.Create(file)
			if err == nil {
				os.Stdout = f
				files = append(files, f)
			}
		case ">>", "1>>":
			f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				os.Stdout = f
				files = append(files, f)
			}
		case "2>":
			f, err := os.Create(file)
			if err == nil {
				os.Stderr = f
				files = append(files, f)
			}
		case "1>&2", ">&2":
			os.Stdout = os.Stderr
		case "2>&1":
			os.Stderr = os.Stdout
		case "<":
			f, err := os.Open(file)
			if err == nil {
				os.Stdin = f
				files = append(files, f)
			}
		}
	}

	return func() {
		os.Stdout = savedOut
		os.Stderr = savedErr
		os.Stdin = savedIn
		for _, f := range files {
			f.Close()
		}
	}
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
