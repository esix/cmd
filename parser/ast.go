// Package parser defines the AST node types produced by the BAT parser.
package parser

// Statement is implemented by every AST node.
type Statement interface {
	statementNode()
}

// --- Simple command (most things) ---

type SimpleCommand struct {
	Args      []WordPart // argv; each element may contain mixed literals and vars
	Redirects []Redirect
}

func (*SimpleCommand) statementNode() {}

// --- Redirect ---

type Redirect struct {
	Op   string // ">", ">>", "<", "2>", "2>&1", etc.
	File string
}

// --- IF ---

type IfStatement struct {
	Not       bool
	Condition Condition
	Then      []Statement
	Else      []Statement // nil if no ELSE
}

func (*IfStatement) statementNode() {}

// Condition variants
type Condition interface{ conditionNode() }

type StringCompare struct {
	Left  []WordPart
	Op    string // "==" only in BAT
	Right []WordPart
}

func (*StringCompare) conditionNode() {}

type ExistCondition struct{ Path []WordPart }

func (*ExistCondition) conditionNode() {}

type ErrorlevelCondition struct{ N int }

func (*ErrorlevelCondition) conditionNode() {}

// --- GOTO ---

type GotoStatement struct{ Label string }

func (*GotoStatement) statementNode() {}

// --- CALL ---

type CallStatement struct {
	Args []WordPart // Args[0] is the script/label
}

func (*CallStatement) statementNode() {}

// --- SET ---

type SetStatement struct {
	Name       string
	Value      []WordPart
	Arithmetic bool // SET /A
	Prompt     bool // SET /P
}

func (*SetStatement) statementNode() {}

// --- ECHO ---

type EchoStatement struct {
	// Args is a list of word groups. Each group is one whitespace-separated
	// token from the original line (e.g. ["hello", "%NAME%", "world"]).
	// Groups are joined with " " during execution.
	Args    [][]WordPart
	TurnOn  *bool // nil = not a toggle; true = ECHO ON, false = ECHO OFF
	Newline bool  // ECHO. prints a blank line
}

func (*EchoStatement) statementNode() {}

// --- FOR ---

type ForKind int

const (
	ForInList  ForKind = iota // FOR %%I IN (a b c) DO
	ForInFiles                // FOR %%I IN (*.txt) DO
	ForRange                  // FOR /L %%I IN (start,step,end) DO
	ForTokens                 // FOR /F "tokens=..." %%I IN (...) DO
)

type ForStatement struct {
	Variable string // e.g. "I" from %%I
	Kind     ForKind
	InList   []string // items for ForInList / ForInFiles / range params
	Options  string   // FOR /F options string
	Body     []Statement
}

func (*ForStatement) statementNode() {}

// --- EXIT ---

type ExitStatement struct {
	Code    int
	SubOnly bool // EXIT /B exits subroutine only, not whole shell
}

func (*ExitStatement) statementNode() {}

// --- Label (jump target) ---

type LabelStatement struct{ Name string }

func (*LabelStatement) statementNode() {}

// --- WordPart: a word is a sequence of these ---

// WordPart is a segment of a word — either a literal string or a variable ref.
type WordPart interface{ wordPartNode() }

type LiteralPart struct{ Text string }

func (*LiteralPart) wordPartNode() {}

type VarPart struct {
	Name     string // variable name, or "" for positional
	Positional int  // 0-9 if positional; -1 otherwise
}

func (*VarPart) wordPartNode() {}
