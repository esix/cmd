// Package parser turns a slice of tokens into a slice of Statements.
package parser

import (
	"strconv"
	"strings"

	"github.com/esix/cmd/lexer"
)

// Parse converts a token stream (one logical line) into Statements.
// A single line typically produces one Statement, but parenthesised
// blocks can produce compound statements.
func Parse(tokens []lexer.Token) ([]Statement, error) {
	p := &parser{tokens: tokens, pos: 0}
	return p.parseStatements()
}

// ParseLine is a convenience wrapper: tokenize + parse in one call.
func ParseLine(line string) ([]Statement, error) {
	tokens := lexer.Tokenize(line)
	return Parse(tokens)
}

type parser struct {
	tokens []lexer.Token
	pos    int
}

func (p *parser) peek() lexer.Token {
	if p.pos >= len(p.tokens) {
		return lexer.Token{Kind: lexer.EOF}
	}
	return p.tokens[p.pos]
}

func (p *parser) consume() lexer.Token {
	t := p.peek()
	p.pos++
	return t
}

func (p *parser) parseStatements() ([]Statement, error) {
	var stmts []Statement
	for p.peek().Kind != lexer.EOF {
		stmt, err := p.parseOne()
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
	}
	return stmts, nil
}

func (p *parser) parseOne() (Statement, error) {
	tok := p.peek()
	if tok.Kind == lexer.EOF {
		return nil, nil
	}

	// Determine the command name (first WORD token, uppercased)
	cmdName := ""
	if tok.Kind == lexer.WORD {
		cmdName = strings.ToUpper(tok.Value)
	}

	// ECHO. (with dot, no space) prints a blank line
	if strings.HasPrefix(cmdName, "ECHO.") {
		p.consume()
		return &EchoStatement{Newline: true}, nil
	}

	switch cmdName {
	case "ECHO":
		return p.parseEcho()
	case "SET":
		return p.parseSet()
	case "IF":
		return p.parseIf()
	case "GOTO":
		return p.parseGoto()
	case "CALL":
		return p.parseCall()
	case "FOR":
		return p.parseFor()
	case "EXIT":
		return p.parseExit()
	default:
		return p.parseSimpleCommand()
	}
}

// --- ECHO ---

func (p *parser) parseEcho() (Statement, error) {
	p.consume() // consume ECHO

	// ECHO with no args
	if p.peek().Kind == lexer.EOF {
		return &EchoStatement{}, nil
	}

	// Check for ECHO. (blank line) — this arrives as a single WORD "ECHO."
	// but we already consumed ECHO, so look for a leading dot token.
	// Actually ECHO. arrives as one token before we split; the lexer sees
	// "ECHO." as WORD "ECHO." — handled in executor. Here we check the
	// next token for ON/OFF toggles.
	next := p.peek()
	if next.Kind == lexer.WORD {
		upper := strings.ToUpper(next.Value)
		if upper == "ON" {
			p.consume()
			on := true
			return &EchoStatement{TurnOn: &on}, nil
		}
		if upper == "OFF" {
			p.consume()
			off := false
			return &EchoStatement{TurnOn: &off}, nil
		}
	}

	// Collect remaining tokens as word groups (one group per token)
	args := p.collectWordGroups()
	return &EchoStatement{Args: args}, nil
}

// --- SET ---

func (p *parser) parseSet() (Statement, error) {
	p.consume() // consume SET

	if p.peek().Kind == lexer.EOF {
		// SET with no args lists all variables — treat as SimpleCommand fallback
		return &SetStatement{}, nil
	}

	// Check for /A or /P flags
	arithmetic := false
	prompt := false
	if p.peek().Kind == lexer.WORD {
		flag := strings.ToUpper(p.peek().Value)
		if flag == "/A" {
			arithmetic = true
			p.consume()
		} else if flag == "/P" {
			prompt = true
			p.consume()
		}
	}

	// Expect NAME=VALUE as a single WORD token (the lexer keeps = inside words)
	if p.peek().Kind != lexer.WORD {
		return &SetStatement{Arithmetic: arithmetic, Prompt: prompt}, nil
	}

	raw := p.consume().Value
	eqIdx := strings.IndexByte(raw, '=')
	if eqIdx == -1 {
		// SET VARNAME with no = just prints the variable
		return &SetStatement{Name: raw, Arithmetic: arithmetic, Prompt: prompt}, nil
	}

	name := raw[:eqIdx]
	valueStr := raw[eqIdx+1:]

	// There may be more tokens after the first word (e.g. spaces in value)
	valueParts := parseWordParts(valueStr)
	for p.peek().Kind != lexer.EOF {
		valueParts = append(valueParts, p.collectWordParts()...)
	}

	return &SetStatement{
		Name:       name,
		Value:      valueParts,
		Arithmetic: arithmetic,
		Prompt:     prompt,
	}, nil
}

// --- IF ---

func (p *parser) parseIf() (Statement, error) {
	p.consume() // consume IF

	not := false
	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "NOT" {
		not = true
		p.consume()
	}

	cond, err := p.parseCondition()
	if err != nil {
		return nil, err
	}

	// Parse THEN body — everything up to optional ELSE
	then, elseStmts, err := p.parseIfBody()
	if err != nil {
		return nil, err
	}

	return &IfStatement{Not: not, Condition: cond, Then: then, Else: elseStmts}, nil
}

func (p *parser) parseCondition() (Condition, error) {
	tok := p.peek()

	if tok.Kind == lexer.WORD {
		upper := strings.ToUpper(tok.Value)

		// IF EXIST path
		if upper == "EXIST" {
			p.consume()
			path := p.collectWordParts()
			return &ExistCondition{Path: path}, nil
		}

		// IF ERRORLEVEL N
		if upper == "ERRORLEVEL" {
			p.consume()
			nStr := p.consume().Value
			n, _ := strconv.Atoi(nStr)
			return &ErrorlevelCondition{N: n}, nil
		}
	}

	// IF "left"=="right" — the == may be embedded inside a single WORD token
	// (e.g. `"hello"=="world"`) or split across two tokens (`"hello"==` `"world"`).
	tok = p.peek()
	if tok.Kind == lexer.WORD {
		idx := strings.Index(tok.Value, "==")
		if idx != -1 {
			p.consume()
			left := parseWordParts(tok.Value[:idx])
			right := parseWordParts(tok.Value[idx+2:])
			// Only collect more tokens if right side was empty in this token
			// (e.g. format: "val"==  nexttoken)
			if len(right) == 0 {
				right = append(right, p.collectWordParts()...)
			}
			return &StringCompare{Left: left, Op: "==", Right: right}, nil
		}
	}
	// Fallback: collect left tokens until a token ending with ==
	left := p.collectUntilOp("==")
	right := p.collectWordParts()
	return &StringCompare{Left: left, Op: "==", Right: right}, nil
}

// collectUntilOp collects word parts until it finds a token ending with op.
func (p *parser) collectUntilOp(op string) []WordPart {
	var parts []WordPart
	for p.peek().Kind != lexer.EOF {
		tok := p.peek()
		if tok.Kind == lexer.WORD && strings.HasSuffix(tok.Value, op) {
			// strip the operator suffix and add the rest as a literal
			text := tok.Value[:len(tok.Value)-len(op)]
			p.consume()
			if text != "" {
				parts = append(parts, parseWordParts(text)...)
			}
			return parts
		}
		p.consume()
		parts = append(parts, parseWordParts(tok.Value)...)
	}
	return parts
}

func (p *parser) parseIfBody() (then []Statement, elseStmts []Statement, err error) {
	// Simple single-command THEN (no parentheses for now)
	thenStmt, err := p.parseOne()
	if err != nil {
		return nil, nil, err
	}
	if thenStmt != nil {
		then = append(then, thenStmt)
	}

	// Optional ELSE
	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "ELSE" {
		p.consume()
		elseStmt, err := p.parseOne()
		if err != nil {
			return nil, nil, err
		}
		if elseStmt != nil {
			elseStmts = append(elseStmts, elseStmt)
		}
	}

	return then, elseStmts, nil
}

// --- GOTO ---

func (p *parser) parseGoto() (Statement, error) {
	p.consume() // consume GOTO
	label := ""
	if p.peek().Kind == lexer.WORD {
		label = p.consume().Value
	}
	return &GotoStatement{Label: label}, nil
}

// --- CALL ---

func (p *parser) parseCall() (Statement, error) {
	p.consume() // consume CALL
	args := p.collectWordParts()
	return &CallStatement{Args: args}, nil
}

// --- FOR ---

func (p *parser) parseFor() (Statement, error) {
	p.consume() // consume FOR

	kind := ForInList
	options := ""

	// Check for /L or /F flags
	if p.peek().Kind == lexer.WORD {
		flag := strings.ToUpper(p.peek().Value)
		if flag == "/L" {
			kind = ForRange
			p.consume()
		} else if flag == "/F" {
			kind = ForTokens
			p.consume()
			if p.peek().Kind == lexer.WORD {
				options = p.consume().Value
			}
		}
	}

	// %%I or %I variable
	varName := ""
	if p.peek().Kind == lexer.PERCENT_VAR {
		raw := p.consume().Value
		// strip % signs: %I -> I, %%I -> I
		varName = strings.Trim(raw, "%")
	}

	// IN
	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "IN" {
		p.consume()
	}

	// (items) — items may be space-separated or comma-separated (FOR /L)
	var items []string
	if p.peek().Kind == lexer.LPAREN {
		p.consume()
		for p.peek().Kind != lexer.RPAREN && p.peek().Kind != lexer.EOF {
			tok := p.consume()
			if tok.Kind == lexer.WORD {
				for _, part := range strings.Split(tok.Value, ",") {
					part = strings.TrimSpace(part)
					if part != "" {
						items = append(items, part)
					}
				}
			}
		}
		if p.peek().Kind == lexer.RPAREN {
			p.consume()
		}
	}

	// DO
	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "DO" {
		p.consume()
	}

	body, err := p.parseOne()
	if err != nil {
		return nil, err
	}
	var bodyStmts []Statement
	if body != nil {
		bodyStmts = append(bodyStmts, body)
	}

	return &ForStatement{
		Variable: varName,
		Kind:     kind,
		InList:   items,
		Options:  options,
		Body:     bodyStmts,
	}, nil
}

// --- EXIT ---

func (p *parser) parseExit() (Statement, error) {
	p.consume() // consume EXIT
	subOnly := false
	code := 0

	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "/B" {
		subOnly = true
		p.consume()
	}

	if p.peek().Kind == lexer.WORD {
		n, err := strconv.Atoi(p.peek().Value)
		if err == nil {
			code = n
			p.consume()
		}
	}

	return &ExitStatement{Code: code, SubOnly: subOnly}, nil
}

// --- SimpleCommand (fallback) ---

func (p *parser) parseSimpleCommand() (Statement, error) {
	var args []WordPart
	var redirects []Redirect

	for p.peek().Kind != lexer.EOF {
		tok := p.peek()

		if tok.Kind == lexer.REDIRECTION {
			p.consume()
			file := ""
			if p.peek().Kind == lexer.WORD {
				file = p.consume().Value
			}
			redirects = append(redirects, Redirect{Op: tok.Value, File: file})
			continue
		}

		// Stop at pipeline / conditional operators
		if tok.Kind == lexer.PIPE || tok.Kind == lexer.AND || tok.Kind == lexer.OR {
			break
		}

		p.consume()
		args = append(args, parseWordParts(tok.Value)...)
	}

	return &SimpleCommand{Args: args, Redirects: redirects}, nil
}

// --- Helpers ---

// collectWordGroups drains remaining non-control tokens into word groups.
// Each token becomes one group; groups are joined with spaces during execution.
func (p *parser) collectWordGroups() [][]WordPart {
	var groups [][]WordPart
	for {
		tok := p.peek()
		if tok.Kind == lexer.EOF || tok.Kind == lexer.PIPE ||
			tok.Kind == lexer.AND || tok.Kind == lexer.OR {
			break
		}
		p.consume()
		var parts []WordPart
		switch tok.Kind {
		case lexer.PERCENT_VAR:
			parts = []WordPart{varPartFromToken(tok.Value)}
		case lexer.BANG_VAR:
			name := strings.Trim(tok.Value, "!")
			parts = []WordPart{&VarPart{Name: name, Positional: -1}}
		default:
			parts = parseWordParts(tok.Value)
		}
		groups = append(groups, parts)
	}
	return groups
}

// collectWordParts drains remaining non-control tokens into WordParts.
func (p *parser) collectWordParts() []WordPart {
	var parts []WordPart
	for {
		tok := p.peek()
		if tok.Kind == lexer.EOF || tok.Kind == lexer.PIPE ||
			tok.Kind == lexer.AND || tok.Kind == lexer.OR {
			break
		}
		p.consume()
		switch tok.Kind {
		case lexer.PERCENT_VAR:
			parts = append(parts, varPartFromToken(tok.Value))
		case lexer.BANG_VAR:
			name := strings.Trim(tok.Value, "!")
			parts = append(parts, &VarPart{Name: name, Positional: -1})
		default:
			parts = append(parts, parseWordParts(tok.Value)...)
		}
	}
	return parts
}

// parseWordParts splits a raw string into LiteralParts and VarParts,
// resolving any %VAR% references embedded in the string.
func parseWordParts(s string) []WordPart {
	if s == "" {
		return nil
	}
	var parts []WordPart
	i := 0
	for i < len(s) {
		pct := strings.IndexByte(s[i:], '%')
		if pct == -1 {
			parts = append(parts, &LiteralPart{Text: s[i:]})
			break
		}
		pct += i
		if pct > i {
			parts = append(parts, &LiteralPart{Text: s[i:pct]})
		}
		// Find closing %
		closeIdx := strings.IndexByte(s[pct+1:], '%')
		if closeIdx == -1 {
			parts = append(parts, &LiteralPart{Text: "%"})
			i = pct + 1
			continue
		}
		closeIdx += pct + 1
		name := s[pct+1 : closeIdx]
		if name == "" {
			// %% → literal %
			parts = append(parts, &LiteralPart{Text: "%"})
			i = closeIdx + 1
			continue
		}
		parts = append(parts, &VarPart{Name: name, Positional: -1})
		i = closeIdx + 1
	}
	return parts
}

func varPartFromToken(raw string) WordPart {
	// %1 .. %9
	if len(raw) == 2 && raw[0] == '%' && raw[1] >= '0' && raw[1] <= '9' {
		return &VarPart{Positional: int(raw[1] - '0'), Name: ""}
	}
	// %VAR%
	name := strings.Trim(raw, "%")
	return &VarPart{Name: name, Positional: -1}
}
