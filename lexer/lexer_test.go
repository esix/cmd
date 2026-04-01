package lexer

import (
	"testing"
)

func TestEchoHello(t *testing.T) {
	tokens := Tokenize("ECHO hello world")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: WORD, Value: "hello"},
		{Kind: WORD, Value: "world"},
		{Kind: EOF},
	})
}

func TestPercentVar(t *testing.T) {
	tokens := Tokenize("ECHO %FOO%")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: PERCENT_VAR, Value: "%FOO%"},
		{Kind: EOF},
	})
}

func TestPositional(t *testing.T) {
	tokens := Tokenize("ECHO %1")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: PERCENT_VAR, Value: "%1"},
		{Kind: EOF},
	})
}

func TestDoublePipe(t *testing.T) {
	tokens := Tokenize("foo || bar")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "foo"},
		{Kind: OR, Value: "||"},
		{Kind: WORD, Value: "bar"},
		{Kind: EOF},
	})
}

func TestAmpersand(t *testing.T) {
	tokens := Tokenize("foo && bar")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "foo"},
		{Kind: AND, Value: "&&"},
		{Kind: WORD, Value: "bar"},
		{Kind: EOF},
	})
}

func TestRedirect(t *testing.T) {
	tokens := Tokenize("ECHO hi >out.txt")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: WORD, Value: "hi"},
		{Kind: REDIRECTION, Value: ">"},
		{Kind: WORD, Value: "out.txt"},
		{Kind: EOF},
	})
}

func TestRedirectAppend(t *testing.T) {
	tokens := Tokenize("ECHO hi >>out.txt")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: WORD, Value: "hi"},
		{Kind: REDIRECTION, Value: ">>"},
		{Kind: WORD, Value: "out.txt"},
		{Kind: EOF},
	})
}

func TestRedirect2to1(t *testing.T) {
	tokens := Tokenize("cmd 2>&1")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "cmd"},
		{Kind: REDIRECTION, Value: "2>&1"},
		{Kind: EOF},
	})
}

func TestCaretEscape(t *testing.T) {
	tokens := Tokenize("ECHO hello^&world")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: WORD, Value: "hello&world"},
		{Kind: EOF},
	})
}

func TestDoublePercent(t *testing.T) {
	tokens := Tokenize("ECHO 100%%")
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "ECHO"},
		{Kind: WORD, Value: "100"},
		{Kind: WORD, Value: "%"},
		{Kind: EOF},
	})
}

func TestQuotedString(t *testing.T) {
	// The lexer keeps "hello world"=="hello world" as a single WORD token.
	// The parser is responsible for splitting on == inside IF conditions.
	tokens := Tokenize(`IF "hello world"=="hello world" ECHO yes`)
	assertTokens(t, tokens, []Token{
		{Kind: WORD, Value: "IF"},
		{Kind: WORD, Value: `"hello world"=="hello world"`},
		{Kind: WORD, Value: "ECHO"},
		{Kind: WORD, Value: "yes"},
		{Kind: EOF},
	})
}

// assertTokens compares Kind and Value; ignores Pos.
func assertTokens(t *testing.T, got, want []Token) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("token count: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Value != want[i].Value {
			t.Errorf("token[%d]: got {%v %q}, want {%v %q}", i, got[i].Kind, got[i].Value, want[i].Kind, want[i].Value)
		}
	}
}
