// Package parser turns a slice of tokens into a slice of Statements.
package parser

import (
	"strconv"
	"strings"

	"github.com/esix/cmd/lexer"
)

// Parse converts a token stream (one logical line) into Statements.
func Parse(tokens []lexer.Token) ([]Statement, error) {
	p := &parser{tokens: tokens, pos: 0}
	return p.parseStatements()
}

// ParseLine is a convenience wrapper: tokenize + parse in one call.
func ParseLine(line string) ([]Statement, error) {
	return ParseLineWithOpts(line, false)
}

// ParseLineWithOpts tokenizes with delayed expansion control, then parses.
func ParseLineWithOpts(line string, delayedExpansion bool) ([]Statement, error) {
	tokens := lexer.TokenizeWithOpts(line, delayedExpansion)
	p := &parser{tokens: tokens, pos: 0, raw: line}
	return p.parseStatements()
}

type parser struct {
	tokens []lexer.Token
	pos    int
	raw    string // original line text (for tokens that need exact slicing)
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
		// Skip & separators between statements
		if p.peek().Kind == lexer.AMPERSAND {
			p.consume()
			continue
		}
		posBefore := p.pos
		stmt, err := p.parseChain()
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
		// Guarantee progress: a stray token the chain parser won't consume
		// (e.g. an unmatched ')') must not spin this loop forever.
		if p.pos == posBefore {
			p.consume()
		}
	}
	return stmts, nil
}

// attachRedirects prepends leading redirects to a statement's redirect list.
func attachRedirects(stmt Statement, leading []Redirect) {
	if len(leading) == 0 {
		return
	}
	switch s := stmt.(type) {
	case *EchoStatement:
		// Leading redirects were consumed before parseEcho ran, so the echo's
		// verbatim RawText (which preserves spacing) remains valid.
		s.Redirects = append(leading, s.Redirects...)
	case *SimpleCommand:
		s.Redirects = append(leading, s.Redirects...)
	case *SetStatement:
		s.Redirects = append(leading, s.Redirects...)
	case *CallStatement:
		s.Redirects = append(leading, s.Redirects...)
	}
}

// parseChain handles: cmd1 && cmd2, cmd1 || cmd2, cmd1 & cmd2
func (p *parser) parseChain() (Statement, error) {
	left, err := p.parsePipe()
	if err != nil || left == nil {
		return left, err
	}
	for {
		tok := p.peek()
		if tok.Kind == lexer.AND || tok.Kind == lexer.OR || tok.Kind == lexer.AMPERSAND {
			op := tok.Value
			p.consume()
			right, err := p.parsePipe()
			if err != nil {
				return nil, err
			}
			if right == nil {
				break
			}
			left = &ChainStatement{Left: left, Op: op, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

// parseIfChain is like parseChain but stops at the block-line boundary marker
// (\x01) so an IF body does not consume statements from later block lines.
// Used for IF THEN/ELSE bodies that aren't wrapped in parens.
func (p *parser) parseIfChain() (Statement, error) {
	left, err := p.parsePipe()
	if err != nil || left == nil {
		return left, err
	}
	for {
		tok := p.peek()
		if tok.Kind == lexer.AND || tok.Kind == lexer.OR ||
			(tok.Kind == lexer.AMPERSAND && tok.Value != "\x01") {
			op := tok.Value
			p.consume()
			right, err := p.parsePipe()
			if err != nil {
				return nil, err
			}
			if right == nil {
				break
			}
			left = &ChainStatement{Left: left, Op: op, Right: right}
		} else {
			break
		}
	}
	return left, nil
}

// parsePipe handles: cmd1 | cmd2 | cmd3
func (p *parser) parsePipe() (Statement, error) {
	left, err := p.parseOne()
	if err != nil || left == nil {
		return left, err
	}
	if p.peek().Kind != lexer.PIPE {
		return left, nil
	}
	cmds := []Statement{left}
	for p.peek().Kind == lexer.PIPE {
		p.consume()
		right, err := p.parseOne()
		if err != nil {
			return nil, err
		}
		if right != nil {
			cmds = append(cmds, right)
		}
	}
	return &PipeStatement{Commands: cmds}, nil
}

func (p *parser) parseOne() (Statement, error) {
	tok := p.peek()
	if tok.Kind == lexer.EOF || tok.Kind == lexer.AMPERSAND ||
		tok.Kind == lexer.AND || tok.Kind == lexer.OR {
		return nil, nil
	}

	// Leading redirections (e.g. `> file echo text`): collect them, parse the
	// command that follows, and attach the redirects to it.
	if tok.Kind == lexer.REDIRECTION {
		var leading []Redirect
		for p.peek().Kind == lexer.REDIRECTION {
			op := p.consume().Value
			file := ""
			if p.peek().Kind == lexer.WORD {
				file = p.consume().Value
			}
			leading = append(leading, Redirect{Op: op, File: file})
		}
		stmt, err := p.parseOne()
		if err != nil {
			return nil, err
		}
		attachRedirects(stmt, leading)
		return stmt, nil
	}

	// Block: ( ... )
	if tok.Kind == lexer.LPAREN {
		return p.parseBlock()
	}

	cmdName := ""
	if tok.Kind == lexer.WORD {
		cmdName = strings.ToUpper(tok.Value)
	}

	// ECHO. (with dot, no space) prints a blank line
	if strings.HasPrefix(cmdName, "ECHO.") {
		p.consume()
		return &EchoStatement{Newline: true}, nil
	}

	// ECHO( safe-echo idiom: `echo(text` is equivalent to `echo text`,
	// and `echo(` alone prints a blank line.
	if strings.HasPrefix(cmdName, "ECHO(") {
		tok := p.consume()
		rest := tok.Value[len("echo("):] // text glued onto the echo( token
		var groups [][]WordPart
		if rest != "" {
			groups = append(groups, parseWordParts(rest))
		}
		// Collect any further space-separated args on the line.
		groups = append(groups, p.collectWordGroups()...)
		if len(groups) == 0 {
			return &EchoStatement{Newline: true}, nil
		}
		return &EchoStatement{Args: groups}, nil
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
	case "SHIFT":
		p.consume()
		return &ShiftStatement{}, nil
	case "SETLOCAL":
		return p.parseSetlocal()
	case "ENDLOCAL":
		p.consume()
		return &EndlocalStatement{}, nil
	default:
		return p.parseSimpleCommand()
	}
}

// --- Block: ( stmt1 & stmt2 & ... ) ---

func (p *parser) parseBlock() (Statement, error) {
	p.consume() // consume (
	var stmts []Statement
	for p.peek().Kind != lexer.RPAREN && p.peek().Kind != lexer.EOF {
		if p.peek().Kind == lexer.AMPERSAND {
			p.consume()
			continue
		}
		// Inside a block, each line is a separate statement. The block-line
		// marker (\x01) and "&" both act as statement separators here, so
		// we should NOT chain through "&" within the block.
		posBefore := p.pos
		stmt, err := p.parseIfChain()
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
		// Guarantee progress on tokens the chain parser won't consume.
		if p.pos == posBefore {
			p.consume()
		}
	}
	if p.peek().Kind == lexer.RPAREN {
		p.consume()
	}
	return &BlockStatement{Stmts: stmts}, nil
}

// --- ECHO ---

func (p *parser) parseEcho() (Statement, error) {
	echoTok := p.consume() // consume ECHO

	if p.atEnd() {
		return &EchoStatement{}, nil
	}

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

	args := p.collectWordGroups()
	// The token right after the args marks where the echo body ends
	// (RPAREN, &, |, redirect, block-marker, or EOF). Use its position to
	// bound the verbatim text so we don't capture a block's closing ")".
	argsEndPos := len(p.raw)
	if bt := p.peek(); bt.Kind != lexer.EOF {
		argsEndPos = bt.Pos
	}
	// Collect any trailing redirections (e.g. ECHO error 1>&2)
	var redirects []Redirect
	for p.peek().Kind == lexer.REDIRECTION {
		op := p.consume().Value
		file := ""
		if p.peek().Kind == lexer.WORD {
			file = p.consume().Value
		}
		redirects = append(redirects, Redirect{Op: op, File: file})
	}

	// Capture verbatim text after "echo " so internal/leading spacing is
	// preserved (cmd.exe: exactly one space after ECHO is the separator, the
	// rest is literal). Only when no redirects are present.
	stmt := &EchoStatement{Args: args, Redirects: redirects}
	if len(redirects) == 0 && p.raw != "" {
		contentStart := echoTok.Pos + len(echoTok.Value)
		// Skip exactly one separator space/tab.
		if contentStart < len(p.raw) && (p.raw[contentStart] == ' ' || p.raw[contentStart] == '\t') {
			contentStart++
		}
		endPos := argsEndPos
		if endPos > len(p.raw) {
			endPos = len(p.raw)
		}
		if contentStart <= endPos {
			raw := strings.TrimRight(p.raw[contentStart:endPos], " \t")
			// Carets are escape characters the token path already resolved;
			// only use verbatim text when none are present.
			if !strings.ContainsRune(raw, '^') {
				stmt.RawText = raw
				stmt.HasRaw = true
			}
		}
	}
	return stmt, nil
}

// --- SET ---

func (p *parser) parseSet() (Statement, error) {
	p.consume() // consume SET

	if p.atEnd() {
		return &SetStatement{}, nil
	}

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

	if p.atEnd() {
		return &SetStatement{Arithmetic: arithmetic, Prompt: prompt}, nil
	}

	tok := p.consume()
	raw := tok.Value
	tokPos := tok.Pos
	hadQuotes := strings.HasPrefix(raw, "\"") && strings.HasSuffix(raw, "\"") && len(raw) >= 2
	// Strip surrounding quotes for SET "name=value" and SET /A "expr"
	raw = stripQuotes(raw)

	eqIdx := strings.IndexByte(raw, '=')

	// For unquoted SET name=value: look up the value in the raw line so we
	// preserve trailing whitespace that the lexer would otherwise drop.
	// e.g. `set X= ` → value = " " (single space) not ""
	if !hadQuotes && eqIdx >= 0 && p.raw != "" && tokPos+len(raw) <= len(p.raw) {
		// Position of `=` in original line
		eqPosInLine := tokPos + eqIdx
		// Find end of value: stop at unquoted &, |, > or end of line.
		// \x01 is the block-line boundary marker (acts like &).
		endPos := len(p.raw)
		hitSep := false
		inQuote := false
		for i := eqPosInLine + 1; i < len(p.raw); i++ {
			ch := p.raw[i]
			if ch == '"' {
				inQuote = !inQuote
				continue
			}
			if !inQuote && (ch == '&' || ch == '|' || ch == '>' || ch == '<' || ch == '\x01') {
				endPos = i
				hitSep = true
				break
			}
		}
		valueStr := p.raw[eqPosInLine+1 : endPos]
		// When value is followed by a separator (&,|,>,<) we trim the
		// trailing whitespace that the user used for visual spacing.
		// Trailing spaces at end-of-line ARE preserved (e.g. `set X= `).
		if hitSep {
			valueStr = strings.TrimRight(valueStr, " \t")
		}
		// Reconstruct raw to include the full value
		raw = p.raw[tokPos:eqPosInLine+1] + valueStr
		eqIdx = strings.IndexByte(raw, '=')
		// Skip remaining tokens until next separator since we consumed the value
		for !p.atEnd() {
			t := p.peek()
			if t.Kind == lexer.AMPERSAND || t.Kind == lexer.AND || t.Kind == lexer.OR ||
				t.Kind == lexer.PIPE || t.Kind == lexer.REDIRECTION || t.Kind == lexer.RPAREN {
				break
			}
			p.consume()
		}
	}

	// For SET /A with spaces: "set /a ii = 2 * i"
	// The = may be a separate token. Collect all tokens as the expression.
	if eqIdx == -1 && arithmetic {
		// Collect rest: raw + remaining tokens form the full expression
		var exprParts []string
		exprParts = append(exprParts, raw)
		for !p.atEnd() {
			tok := p.peek()
			if tok.Kind == lexer.REDIRECTION {
				break
			}
			p.consume()
			exprParts = append(exprParts, tok.Value)
		}
		fullExpr := strings.Join(exprParts, "")
		eqIdx = strings.IndexByte(fullExpr, '=')
		if eqIdx == -1 {
			return &SetStatement{Name: fullExpr, Arithmetic: true}, nil
		}
		name := strings.TrimSpace(fullExpr[:eqIdx])
		value := strings.TrimSpace(fullExpr[eqIdx+1:])
		return &SetStatement{
			Name: name, Value: [][]WordPart{{&LiteralPart{Text: value}}},
			HasEquals: true, Arithmetic: true,
		}, nil
	}

	if eqIdx == -1 {
		return &SetStatement{Name: raw, Arithmetic: arithmetic, Prompt: prompt}, nil
	}

	name := raw[:eqIdx]
	valueStr := raw[eqIdx+1:]

	var valueGroups [][]WordPart
	if valueStr != "" {
		initParts := parseWordParts(valueStr)
		// If next token is adjacent (no space), merge it with the initial value
		// This handles: SET acc=VAR_!x! where !x! immediately follows VAR_
		if !p.atEnd() && !p.peek().SpaceBefore && p.peek().Kind != lexer.REDIRECTION {
			groups := p.collectWordGroups()
			if len(groups) > 0 {
				initParts = append(initParts, groups[0]...)
				groups = groups[1:]
			}
			valueGroups = append(valueGroups, initParts)
			valueGroups = append(valueGroups, groups...)
		} else {
			valueGroups = append(valueGroups, initParts)
			valueGroups = append(valueGroups, p.collectWordGroups()...)
		}
	} else {
		valueGroups = append(valueGroups, p.collectWordGroups()...)
	}

	// Collect trailing redirections (e.g. SET /A "d=1" 2>nul, SET /P var= < file)
	var redirects []Redirect
	for p.peek().Kind == lexer.REDIRECTION {
		op := p.consume().Value
		file := ""
		if p.peek().Kind == lexer.WORD {
			file = p.consume().Value
		}
		redirects = append(redirects, Redirect{Op: op, File: file})
	}

	return &SetStatement{
		Name:       name,
		Value:      valueGroups,
		Redirects:  redirects,
		HasEquals:  true,
		Arithmetic: arithmetic,
		Prompt:     prompt,
	}, nil
}

// --- IF ---

func (p *parser) parseIf() (Statement, error) {
	p.consume() // consume IF

	// Optional flags: /I (case-insensitive), and order-independent NOT
	caseInsensitive := false
	not := false
	for p.peek().Kind == lexer.WORD {
		upper := strings.ToUpper(p.peek().Value)
		if upper == "/I" {
			caseInsensitive = true
			p.consume()
			continue
		}
		if upper == "NOT" {
			not = true
			p.consume()
			continue
		}
		break
	}

	cond, err := p.parseCondition()
	if err != nil {
		return nil, err
	}
	// Mark string-compare conditions as case-insensitive when /I present
	if caseInsensitive {
		if sc, ok := cond.(*StringCompare); ok {
			sc.CaseInsensitive = true
		}
	}

	then, elseStmts, err := p.parseIfBody()
	if err != nil {
		return nil, err
	}

	return &IfStatement{Not: not, Condition: cond, Then: then, Else: elseStmts}, nil
}

var numericOps = map[string]bool{
	"EQU": true, "NEQ": true, "LSS": true,
	"LEQ": true, "GTR": true, "GEQ": true,
}

func (p *parser) parseCondition() (Condition, error) {
	tok := p.peek()

	if tok.Kind == lexer.WORD {
		upper := strings.ToUpper(tok.Value)

		if upper == "EXIST" {
			p.consume()
			path := p.collectOneWordParts()
			return &ExistCondition{Path: path}, nil
		}

		if upper == "DEFINED" {
			p.consume()
			name := p.consume().Value
			return &DefinedCondition{Name: name}, nil
		}

		if upper == "ERRORLEVEL" {
			p.consume()
			nStr := p.consume().Value
			n, _ := strconv.Atoi(nStr)
			return &ErrorlevelCondition{N: n}, nil
		}
	}

	// Try embedded "==" in a single token: "val1"=="val2"
	tok = p.peek()
	if tok.Kind == lexer.WORD {
		idx := strings.Index(tok.Value, "==")
		if idx != -1 {
			p.consume()
			left := parseWordParts(tok.Value[:idx])
			right := parseWordParts(tok.Value[idx+2:])
			if len(right) == 0 {
				right = append(right, p.collectWordParts()...)
			}
			return &StringCompare{Left: left, Op: "==", Right: right}, nil
		}
	}

	// Collect left operand (one word/token group for numeric ops)
	left := p.collectOneWordParts()

	// Check for numeric comparison operator: val1 LSS val2
	if p.peek().Kind == lexer.WORD {
		op := strings.ToUpper(p.peek().Value)
		if numericOps[op] {
			p.consume()
			right := p.collectOneWordParts()
			return &NumericCompare{Left: left, Op: op, Right: right}, nil
		}
	}

	// Check for == as separate token or suffix
	if p.peek().Kind == lexer.WORD && strings.HasPrefix(p.peek().Value, "==") {
		tok := p.consume()
		rightStr := tok.Value[2:]
		right := parseWordParts(rightStr)
		if len(right) == 0 {
			right = p.collectOneWordParts()
		}
		return &StringCompare{Left: left, Op: "==", Right: right}, nil
	}

	// Fallback: collect until ==
	more := p.collectUntilOp("==")
	left = append(left, more...)
	right := p.collectWordParts()
	return &StringCompare{Left: left, Op: "==", Right: right}, nil
}

func (p *parser) parseIfBody() (then []Statement, elseStmts []Statement, err error) {
	// Block body: IF cond ( ... ) ELSE ( ... )
	if p.peek().Kind == lexer.LPAREN {
		block, err := p.parseBlock()
		if err != nil {
			return nil, nil, err
		}
		then = block.(*BlockStatement).Stmts
	} else {
		// In CMD, the THEN body extends to end of line (including & chains).
		// We stop at the line-boundary marker (\x01) injected by joinBlocks
		// so that IF body does not absorb subsequent block-level statements.
		thenStmt, err := p.parseIfChain()
		if err != nil {
			return nil, nil, err
		}
		if thenStmt != nil {
			then = append(then, thenStmt)
		}
	}

	// Optional ELSE
	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "ELSE" {
		p.consume()
		if p.peek().Kind == lexer.LPAREN {
			block, err := p.parseBlock()
			if err != nil {
				return nil, nil, err
			}
			elseStmts = block.(*BlockStatement).Stmts
		} else {
			elseStmt, err := p.parseIfChain()
			if err != nil {
				return nil, nil, err
			}
			if elseStmt != nil {
				elseStmts = append(elseStmts, elseStmt)
			}
		}
	}

	return then, elseStmts, nil
}

// --- SETLOCAL ---

func (p *parser) parseSetlocal() (Statement, error) {
	p.consume()
	stmt := &SetlocalStatement{}
	for p.peek().Kind == lexer.WORD {
		arg := strings.ToUpper(p.peek().Value)
		if arg == "ENABLEDELAYEDEXPANSION" {
			stmt.EnableDelayedExpansion = true
			p.consume()
		} else if arg == "DISABLEDELAYEDEXPANSION" {
			stmt.DisableDelayedExpansion = true
			p.consume()
		} else {
			break
		}
	}
	return stmt, nil
}

// --- GOTO ---

func (p *parser) parseGoto() (Statement, error) {
	p.consume()
	parts := p.collectWordParts()
	return &GotoStatement{LabelParts: parts}, nil
}

// --- CALL ---

func (p *parser) parseCall() (Statement, error) {
	p.consume()
	// Record where the args start so we can capture their verbatim text.
	rawStart := -1
	if t := p.peek(); t.Kind != lexer.EOF {
		rawStart = t.Pos
	}
	// Each whitespace-separated token is one argument. collectWordGroups
	// keeps the parts of a single token (e.g. "!_path!") together so a quoted
	// delayed-expansion argument isn't split into multiple positional params.
	args := p.collectWordGroups()
	// The token after the args (redirect, &, |, ), or EOF) bounds the raw text.
	rawEnd := len(p.raw)
	if t := p.peek(); t.Kind != lexer.EOF && t.Pos < rawEnd {
		rawEnd = t.Pos
	}
	rawText := ""
	if rawStart >= 0 && rawStart < rawEnd && rawEnd <= len(p.raw) {
		rawText = strings.TrimSpace(p.raw[rawStart:rawEnd])
	}
	// Collect trailing redirections (e.g. CALL foo > out.txt 2>&1).
	var redirects []Redirect
	for p.peek().Kind == lexer.REDIRECTION {
		op := p.consume().Value
		file := ""
		if p.peek().Kind == lexer.WORD {
			file = p.consume().Value
		}
		redirects = append(redirects, Redirect{Op: op, File: file})
	}
	return &CallStatement{Args: args, Redirects: redirects, RawText: rawText}, nil
}

// --- FOR ---

func (p *parser) parseFor() (Statement, error) {
	p.consume()

	kind := ForInList
	options := ""
	rootPath := ""

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
		} else if flag == "/D" {
			kind = ForDirs
			p.consume()
		} else if flag == "/R" {
			kind = ForRecursive
			p.consume()
			// Optional root path before the variable
			if p.peek().Kind == lexer.WORD && !strings.HasPrefix(p.peek().Value, "%") {
				rootPath = p.consume().Value
			}
		}
	}

	varName := ""
	if p.peek().Kind == lexer.PERCENT_VAR {
		raw := p.consume().Value
		varName = strings.Trim(raw, "%")
	}

	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "IN" {
		p.consume()
	}

	var items []string
	if p.peek().Kind == lexer.LPAREN {
		p.consume()
		for p.peek().Kind != lexer.RPAREN && p.peek().Kind != lexer.EOF {
			tok := p.consume()
			val := tok.Value
			if tok.Kind == lexer.WORD || tok.Kind == lexer.PERCENT_VAR || tok.Kind == lexer.BANG_VAR {
				for _, part := range strings.Split(val, ",") {
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

	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "DO" {
		p.consume()
	}

	// DO body can be a single command or a ( block )
	var bodyStmts []Statement
	if p.peek().Kind == lexer.LPAREN {
		block, err := p.parseBlock()
		if err != nil {
			return nil, err
		}
		bodyStmts = block.(*BlockStatement).Stmts
	} else {
		body, err := p.parseOne()
		if err != nil {
			return nil, err
		}
		if body != nil {
			bodyStmts = append(bodyStmts, body)
		}
	}

	return &ForStatement{
		Variable: varName,
		Kind:     kind,
		InList:   items,
		Options:  options,
		RootPath: rootPath,
		Body:     bodyStmts,
	}, nil
}

// --- EXIT ---

func (p *parser) parseExit() (Statement, error) {
	p.consume()
	subOnly := false
	code := 0

	if p.peek().Kind == lexer.WORD && strings.ToUpper(p.peek().Value) == "/B" {
		subOnly = true
		p.consume()
	}

	// Code may be a literal number or a variable reference (e.g. !ERRORLEVEL!).
	if p.peek().Kind == lexer.WORD {
		n, err := strconv.Atoi(p.peek().Value)
		if err == nil {
			code = n
			p.consume()
		}
	} else if p.peek().Kind == lexer.BANG_VAR || p.peek().Kind == lexer.PERCENT_VAR {
		tok := p.consume()
		return &ExitStatement{
			CodeParts: tokenToParts(tok),
			SubOnly:   subOnly,
		}, nil
	}

	return &ExitStatement{Code: code, SubOnly: subOnly}, nil
}

// --- SimpleCommand (fallback) ---

func (p *parser) parseSimpleCommand() (Statement, error) {
	var args []WordPart
	var redirects []Redirect

	for !p.atEnd() {
		tok := p.peek()

		if tok.Kind == lexer.PIPE || tok.Kind == lexer.RPAREN {
			break
		}

		if tok.Kind == lexer.REDIRECTION {
			p.consume()
			file := ""
			if p.peek().Kind == lexer.WORD {
				file = p.consume().Value
			}
			redirects = append(redirects, Redirect{Op: tok.Value, File: file})
			continue
		}

		p.consume()
		switch tok.Kind {
		case lexer.PERCENT_VAR:
			args = append(args, varPartFromToken(tok.Value))
		case lexer.BANG_VAR:
			name := strings.Trim(tok.Value, "!")
			args = append(args, &DelayedVarPart{Name: name})
		default:
			// Each WORD token is one argument. Treat as a single LiteralPart;
			// ExpandWord will run %% and !! expansion on the text at runtime.
			args = append(args, &LiteralPart{Text: tok.Value})
		}
	}

	return &SimpleCommand{Args: args, Redirects: redirects}, nil
}

// --- Helpers ---

// atEnd returns true if the next token ends the current statement.
func (p *parser) atEnd() bool {
	k := p.peek().Kind
	return k == lexer.EOF || k == lexer.AMPERSAND ||
		k == lexer.AND || k == lexer.OR || k == lexer.RPAREN
}

// collectWordGroups drains remaining non-control tokens into word groups.
// Adjacent tokens (no whitespace between them) are merged into the same group.
func (p *parser) collectWordGroups() [][]WordPart {
	var groups [][]WordPart
	for !p.atEnd() {
		tok := p.peek()
		if tok.Kind == lexer.PIPE || tok.Kind == lexer.REDIRECTION ||
			tok.Kind == lexer.AMPERSAND || tok.Kind == lexer.AND ||
			tok.Kind == lexer.OR || tok.Kind == lexer.RPAREN {
			break
		}
		p.consume()
		parts := tokenToParts(tok)
		if !tok.SpaceBefore && len(groups) > 0 {
			groups[len(groups)-1] = append(groups[len(groups)-1], parts...)
		} else {
			groups = append(groups, parts)
		}
	}
	return groups
}

// collectWordParts drains remaining non-control tokens into a flat WordPart slice.
func (p *parser) collectWordParts() []WordPart {
	var parts []WordPart
	for !p.atEnd() {
		tok := p.peek()
		if tok.Kind == lexer.PIPE || tok.Kind == lexer.REDIRECTION {
			break
		}
		p.consume()
		parts = append(parts, tokenToParts(tok)...)
	}
	return parts
}

// collectOneWordParts collects parts for a single word (stops at space boundary).
func (p *parser) collectOneWordParts() []WordPart {
	var parts []WordPart
	first := true
	for !p.atEnd() {
		tok := p.peek()
		if tok.Kind == lexer.PIPE || tok.Kind == lexer.REDIRECTION {
			break
		}
		if !first && tok.SpaceBefore {
			break
		}
		// Don't merge == into the operand — it's the comparison operator
		if !first && tok.Kind == lexer.WORD && strings.HasPrefix(tok.Value, "==") {
			break
		}
		p.consume()
		parts = append(parts, tokenToParts(tok)...)
		first = false
	}
	return parts
}

// collectUntilOp collects word parts until it finds a token ending with op.
func (p *parser) collectUntilOp(op string) []WordPart {
	var parts []WordPart
	for p.peek().Kind != lexer.EOF {
		tok := p.peek()
		if tok.Kind == lexer.WORD && strings.HasSuffix(tok.Value, op) {
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

// tokenToParts converts a token into WordParts.
func tokenToParts(tok lexer.Token) []WordPart {
	switch tok.Kind {
	case lexer.PERCENT_VAR:
		return []WordPart{varPartFromToken(tok.Value)}
	case lexer.BANG_VAR:
		name := strings.Trim(tok.Value, "!")
		return []WordPart{&DelayedVarPart{Name: name}}
	default:
		return parseWordParts(tok.Value)
	}
}

// parseWordParts splits a raw string into LiteralParts, VarParts, TildeVarParts,
// and DelayedVarParts (for !VAR! references).
func parseWordParts(s string) []WordPart {
	if s == "" {
		return nil
	}
	var parts []WordPart
	i := 0
	for i < len(s) {
		// Find next % or ! (whichever comes first)
		pct := strings.IndexByte(s[i:], '%')
		bang := strings.IndexByte(s[i:], '!')
		var nextIdx int
		var nextCh byte
		if pct == -1 && bang == -1 {
			parts = append(parts, &LiteralPart{Text: s[i:]})
			break
		}
		if pct == -1 {
			nextIdx = bang + i
			nextCh = '!'
		} else if bang == -1 {
			nextIdx = pct + i
			nextCh = '%'
		} else if pct < bang {
			nextIdx = pct + i
			nextCh = '%'
		} else {
			nextIdx = bang + i
			nextCh = '!'
		}

		if nextIdx > i {
			parts = append(parts, &LiteralPart{Text: s[i:nextIdx]})
		}

		// Handle !VAR! delayed expansion
		if nextCh == '!' {
			closeIdx := strings.IndexByte(s[nextIdx+1:], '!')
			if closeIdx == -1 {
				parts = append(parts, &LiteralPart{Text: "!"})
				i = nextIdx + 1
				continue
			}
			closeIdx += nextIdx + 1
			name := s[nextIdx+1 : closeIdx]
			if name == "" {
				parts = append(parts, &LiteralPart{Text: "!"})
				i = closeIdx + 1
				continue
			}
			parts = append(parts, &DelayedVarPart{Name: name})
			i = closeIdx + 1
			continue
		}

		pct = nextIdx
		// %%X (FOR variable: single letter, not followed by alnum)
		if pct+2 < len(s) && s[pct+1] == '%' {
			ch := s[pct+2]
			isLetter := (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
			nextIsAlnum := pct+3 < len(s) && ((s[pct+3] >= 'a' && s[pct+3] <= 'z') ||
				(s[pct+3] >= 'A' && s[pct+3] <= 'Z') ||
				(s[pct+3] >= '0' && s[pct+3] <= '9') || s[pct+3] == '_')
			if isLetter && !nextIsAlnum {
				parts = append(parts, &VarPart{Name: string(ch), Positional: -1})
				i = pct + 3
				continue
			}
		}

		// %~[modifiers]N — tilde parameter reference
		if pct+1 < len(s) && s[pct+1] == '~' {
			j := pct + 2
			for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
				j++
			}
			if j < len(s) && s[j] >= '0' && s[j] <= '9' {
				mods := s[pct+2 : j]
				digit := int(s[j] - '0')
				parts = append(parts, &TildeVarPart{Positional: digit, Modifiers: mods})
				i = j + 1
				continue
			}
			// Not a valid tilde ref, treat % as literal
			parts = append(parts, &LiteralPart{Text: "%"})
			i = pct + 1
			continue
		}

		// %N — positional parameter
		if pct+1 < len(s) && s[pct+1] >= '0' && s[pct+1] <= '9' {
			digit := int(s[pct+1] - '0')
			parts = append(parts, &VarPart{Positional: digit})
			i = pct + 2
			continue
		}

		closeIdx := strings.IndexByte(s[pct+1:], '%')
		if closeIdx == -1 {
			parts = append(parts, &LiteralPart{Text: "%"})
			i = pct + 1
			continue
		}
		closeIdx += pct + 1
		name := s[pct+1 : closeIdx]
		if name == "" {
			parts = append(parts, &LiteralPart{Text: "%"})
			i = closeIdx + 1
			continue
		}
		// Check for substring: VAR:~N or VAR:~N,M
		if colonIdx := strings.Index(name, ":~"); colonIdx != -1 {
			varName := name[:colonIdx]
			spec := name[colonIdx+2:]
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
			parts = append(parts, &SubstringVarPart{
				Name: varName, Start: start, Length: length, HasLength: hasLength,
			})
			i = closeIdx + 1
			continue
		}
		parts = append(parts, &VarPart{Name: name, Positional: -1})
		i = closeIdx + 1
	}
	return parts
}

func varPartFromToken(raw string) WordPart {
	// %~[modifiers]N — tilde parameter
	if strings.HasPrefix(raw, "%~") {
		rest := raw[2:]
		if len(rest) == 0 {
			return &LiteralPart{Text: raw}
		}
		// Last char should be the digit
		digit := rest[len(rest)-1]
		if digit >= '0' && digit <= '9' {
			mods := rest[:len(rest)-1]
			return &TildeVarPart{
				Positional: int(digit - '0'),
				Modifiers:  mods,
			}
		}
		// Could be %~dp0 style where 0 is embedded
		return &LiteralPart{Text: raw}
	}

	// %%I — FOR variable
	if strings.HasPrefix(raw, "%%") {
		name := strings.Trim(raw, "%")
		return &VarPart{Name: name, Positional: -1}
	}

	// %N — positional
	if len(raw) == 2 && raw[0] == '%' && raw[1] >= '0' && raw[1] <= '9' {
		return &VarPart{Positional: int(raw[1] - '0'), Name: ""}
	}

	// %VAR% or %VAR:~N,M%
	name := strings.Trim(raw, "%")

	// Check for substring: VAR:~N or VAR:~N,M
	if colonIdx := strings.Index(name, ":~"); colonIdx != -1 {
		varName := name[:colonIdx]
		spec := name[colonIdx+2:]
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
		return &SubstringVarPart{
			Name:      varName,
			Start:     start,
			Length:    length,
			HasLength: hasLength,
		}
	}

	return &VarPart{Name: name, Positional: -1}
}

// stripQuotes removes one layer of surrounding double quotes.
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
