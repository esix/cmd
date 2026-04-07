// Package lexer tokenizes a single BAT file line into tokens.
//
// Key BAT lexer rules:
//   - ^ is the escape character: ^& means literal &, ^^ means literal ^
//   - %VAR% delimits a variable reference
//   - !VAR! delimits a delayed-expansion variable reference
//   - Whitespace (space/tab) separates tokens
//   - Quoted strings ("...") are a single WORD token (quotes preserved)
package lexer

import "strings"

// Tokenize splits a single logical line into tokens.
// The caller is responsible for stripping a leading @ before calling.
func Tokenize(line string) []Token {
	return TokenizeWithOpts(line, false)
}

// TokenizeWithOpts tokenizes with explicit delayed expansion control.
func TokenizeWithOpts(line string, delayedExpansion bool) []Token {
	l := &lexer{input: line, pos: 0, delayedExpansion: delayedExpansion}
	return l.tokenize()
}

type lexer struct {
	input            string
	pos              int
	delayedExpansion bool // enabled by SETLOCAL EnableDelayedExpansion
}

func (l *lexer) tokenize() []Token {
	var tokens []Token
	for {
		posBefore := l.pos
		l.skipSpaces()
		hadSpace := l.pos > posBefore || l.pos == 0 // first token counts as having a space
		if l.pos >= len(l.input) {
			tokens = append(tokens, Token{Kind: EOF, Pos: l.pos})
			break
		}

		ch := l.input[l.pos]
		addToken := func(tok Token) {
			tok.SpaceBefore = hadSpace
			tokens = append(tokens, tok)
		}

		switch {
		case ch == '|' && l.peek(1) == '|':
			addToken(Token{Kind: OR, Value: "||", Pos: l.pos})
			l.pos += 2

		case ch == '&' && l.peek(1) == '&':
			addToken(Token{Kind: AND, Value: "&&", Pos: l.pos})
			l.pos += 2

		case ch == '&':
			addToken(Token{Kind: AMPERSAND, Value: "&", Pos: l.pos})
			l.pos++

		case ch == '|':
			addToken(Token{Kind: PIPE, Value: "|", Pos: l.pos})
			l.pos++

		case ch == '(':
			addToken(Token{Kind: LPAREN, Value: "(", Pos: l.pos})
			l.pos++

		case ch == ')':
			addToken(Token{Kind: RPAREN, Value: ")", Pos: l.pos})
			l.pos++

		case ch == '>' || ch == '<' || isRedirectStart(l.input, l.pos):
			addToken(l.readRedirect())

		case ch == '%':
			addToken(l.readPercentVar())

		case ch == '!' && l.delayedExpansion:
			addToken(l.readBangVar())

		default:
			addToken(l.readWord())
		}
	}
	return tokens
}

// peek returns the character at pos+offset, or 0 if out of bounds.
func (l *lexer) peek(offset int) byte {
	i := l.pos + offset
	if i >= len(l.input) {
		return 0
	}
	return l.input[i]
}

func isAlphaNum(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}

func (l *lexer) skipSpaces() {
	for l.pos < len(l.input) && (l.input[l.pos] == ' ' || l.input[l.pos] == '\t') {
		l.pos++
	}
}

// isRedirectStart detects "2>" or "2>>" at the given position.
func isRedirectStart(s string, pos int) bool {
	if pos >= len(s) {
		return false
	}
	ch := s[pos]
	if ch >= '0' && ch <= '9' {
		next := pos + 1
		if next < len(s) && s[next] == '>' {
			return true
		}
	}
	return false
}

func (l *lexer) readRedirect() Token {
	start := l.pos
	var sb strings.Builder

	// optional digit prefix (e.g. "2")
	if l.pos < len(l.input) && l.input[l.pos] >= '0' && l.input[l.pos] <= '9' {
		sb.WriteByte(l.input[l.pos])
		l.pos++
	}

	// > or <
	if l.pos < len(l.input) {
		sb.WriteByte(l.input[l.pos])
		l.pos++
	}

	// optional second > for >>
	if l.pos < len(l.input) && l.input[l.pos] == '>' {
		sb.WriteByte(l.input[l.pos])
		l.pos++
	}

	// optional &N (e.g. 2>&1)
	if l.pos < len(l.input) && l.input[l.pos] == '&' {
		sb.WriteByte(l.input[l.pos])
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] >= '0' && l.input[l.pos] <= '9' {
			sb.WriteByte(l.input[l.pos])
			l.pos++
		}
	}

	return Token{Kind: REDIRECTION, Value: sb.String(), Pos: start}
}

// readPercentVar reads %VAR%, %0-%9, %~[modifiers]N, %VAR:~N,M%, or %%I.
// If no closing % is found, treats it as a plain WORD.
func (l *lexer) readPercentVar() Token {
	start := l.pos
	l.pos++ // consume opening %

	// %~[modifiers]N — tilde parameter modifier
	if l.pos < len(l.input) && l.input[l.pos] == '~' {
		l.pos++ // consume ~
		var sb strings.Builder
		sb.WriteString("%~")
		// Read modifier letters and trailing digit
		for l.pos < len(l.input) && (isAlphaNum(l.input[l.pos]) || l.input[l.pos] == '$') {
			sb.WriteByte(l.input[l.pos])
			l.pos++
		}
		return Token{Kind: PERCENT_VAR, Value: sb.String(), Pos: start}
	}

	// positional: %0 .. %9
	if l.pos < len(l.input) && l.input[l.pos] >= '0' && l.input[l.pos] <= '9' {
		val := string([]byte{'%', l.input[l.pos]})
		l.pos++
		return Token{Kind: PERCENT_VAR, Value: val, Pos: start}
	}

	// %% — either a FOR-loop variable (%%I) or a literal %
	if l.pos < len(l.input) && l.input[l.pos] == '%' {
		l.pos++ // consume second %
		// %%X where X is a letter/digit → FOR variable token
		if l.pos < len(l.input) && isAlphaNum(l.input[l.pos]) {
			ch := l.input[l.pos]
			l.pos++
			return Token{Kind: PERCENT_VAR, Value: "%%" + string(ch), Pos: start}
		}
		return Token{Kind: WORD, Value: "%", Pos: start}
	}

	// %VAR% or %VAR:~N,M% or %VAR:old=new%
	end := strings.IndexByte(l.input[l.pos:], '%')
	if end == -1 {
		return Token{Kind: WORD, Value: "%", Pos: start}
	}
	name := l.input[l.pos : l.pos+end]
	l.pos += end + 1 // skip past closing %
	return Token{Kind: PERCENT_VAR, Value: "%" + name + "%", Pos: start}
}

// readBangVar reads !VAR! for delayed expansion.
func (l *lexer) readBangVar() Token {
	start := l.pos
	l.pos++ // consume opening !

	end := strings.IndexByte(l.input[l.pos:], '!')
	if end == -1 {
		return Token{Kind: WORD, Value: "!", Pos: start}
	}
	name := l.input[l.pos : l.pos+end]
	l.pos += end + 1
	return Token{Kind: BANG_VAR, Value: "!" + name + "!", Pos: start}
}

// readWord reads a whitespace-delimited word, handling ^ escapes and quotes.
func (l *lexer) readWord() Token {
	start := l.pos
	var sb strings.Builder

	for l.pos < len(l.input) {
		ch := l.input[l.pos]

		// stop at unquoted delimiters
		if ch == ' ' || ch == '\t' || ch == '|' || ch == '(' || ch == ')' {
			break
		}
		if ch == '&' {
			break
		}
		if ch == '|' {
			break
		}
		if ch == '>' || ch == '<' || isRedirectStart(l.input, l.pos) {
			break
		}
		if ch == '%' {
			break // let the main loop handle variable tokens
		}
		if ch == '!' && l.delayedExpansion {
			break
		}

		// ^ escape
		if ch == '^' {
			l.pos++
			if l.pos < len(l.input) {
				sb.WriteByte(l.input[l.pos])
				l.pos++
			}
			continue
		}

		// quoted string — consume until closing quote
		if ch == '"' {
			sb.WriteByte(ch)
			l.pos++
			for l.pos < len(l.input) && l.input[l.pos] != '"' {
				sb.WriteByte(l.input[l.pos])
				l.pos++
			}
			if l.pos < len(l.input) {
				sb.WriteByte(l.input[l.pos]) // closing "
				l.pos++
			}
			continue
		}

		sb.WriteByte(ch)
		l.pos++
	}

	return Token{Kind: WORD, Value: sb.String(), Pos: start}
}
