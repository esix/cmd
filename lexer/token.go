package lexer

// Kind represents the type of a token.
type Kind int

const (
	WORD        Kind = iota // a literal word or quoted string
	PERCENT_VAR             // %VAR% or %1 positional
	BANG_VAR                // !VAR! delayed expansion
	REDIRECTION             // > >> < 2> 2>&1
	PIPE                    // |
	AND                     // &&
	OR                      // ||
	LPAREN                  // (
	RPAREN                  // )
	NEWLINE                 // end of logical line
	EOF                     // end of input
)

// Token is a single lexical unit.
type Token struct {
	Kind    Kind
	Value   string // raw text of the token
	Pos     int    // byte offset in the original line
}
