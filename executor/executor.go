// Package executor runs parsed BAT statements.
package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/executor/builtins"
	"github.com/esix/cmd/expander"
	"github.com/esix/cmd/parser"
)

// Executor runs a list of statements with a program counter (for GOTO support).
type Executor struct {
	env        *env.Env
	positional []string // %0, %1, ... script arguments
	stmts      []parser.Statement
	pc         int // current statement index
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

	stmts, err := parser.ParseLine(line)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
		return 1
	}

	if ex.env.Echo && echoLine && len(stmts) > 0 {
		// In file mode, echo the line before executing
	}

	return ex.RunStmts(stmts, nil)
}

// RunFile executes a .bat file by reading all statements and running them
// with GOTO support (program counter).
func (ex *Executor) RunFile(path string, args []string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open file: %s\n", path)
		return 1
	}

	ex.env.FileMode = true
	ex.positional = append([]string{path}, args...)

	lines := strings.Split(string(data), "\n")
	var stmts []parser.Statement

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		// Skip blank lines
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Strip leading @
		if strings.HasPrefix(line, "@") {
			line = line[1:]
		}

		trimmed := strings.TrimSpace(line)

		// Label line
		if strings.HasPrefix(trimmed, ":") {
			label := strings.ToUpper(trimmed[1:])
			stmts = append(stmts, &parser.LabelStatement{Name: label})
			continue
		}

		// REM / comment
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "REM") && (len(upper) == 3 || upper[3] == ' ') {
			continue
		}

		parsed, err := parser.ParseLine(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			continue
		}
		stmts = append(stmts, parsed...)
	}

	return ex.RunStmts(stmts, args)
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
	}

	ex.stmts = saved
	ex.pc = savedPC
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
	case *parser.LabelStatement:
		return 0 // labels are no-ops during execution
	case *parser.SimpleCommand:
		return ex.execSimple(s)
	default:
		fmt.Fprintf(os.Stderr, "unknown statement type: %T\n", stmt)
		return 1
	}
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
	return builtins.Echo(words, ex.env)
}

// --- SET ---

func (ex *Executor) execSet(s *parser.SetStatement) int {
	if s.Name == "" {
		return builtins.Set(nil, ex.env)
	}
	value := ex.expandParts(s.Value)
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
	label := strings.ToUpper(s.Label)
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

	// CALL :label — subroutine within same file
	if strings.HasPrefix(first, ":") {
		label := strings.ToUpper(first[1:])
		savedPC := ex.pc
		for i, stmt := range ex.stmts {
			if lbl, ok := stmt.(*parser.LabelStatement); ok && lbl.Name == label {
				ex.pc = i + 1
				// Run until EXIT /B or end
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
				return code
			}
		}
		fmt.Fprintf(os.Stderr, "Label not found: %s\n", first)
		return 1
	}

	// CALL script.bat [args...]
	scriptPath := first
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

// --- EXIT ---

func (ex *Executor) execExit(s *parser.ExitStatement) int {
	if s.SubOnly {
		// EXIT /B: signal to RunStmts to stop — set pc past end
		ex.pc = len(ex.stmts)
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
	if batPath, ok := resolveBat(args[0]); ok {
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

	// Apply redirections
	for _, r := range redirects {
		switch r.Op {
		case ">":
			f, err := os.Create(r.File)
			if err == nil {
				cmd.Stdout = f
				defer f.Close()
			}
		case ">>":
			f, err := os.OpenFile(r.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				cmd.Stdout = f
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

// resolveBat checks if name refers to a .bat file (with or without extension).
// Returns the resolved path and true if found.
func resolveBat(name string) (string, bool) {
	// Explicit .bat extension
	if strings.HasSuffix(strings.ToLower(name), ".bat") {
		if _, err := os.Stat(name); err == nil {
			return name, true
		}
		return "", false
	}
	// Try appending .bat
	candidate := name + ".bat"
	if _, err := os.Stat(candidate); err == nil {
		return candidate, true
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
