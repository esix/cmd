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

// --- Pipe: cmd1 | cmd2 ---

type PipeStatement struct {
	Commands []Statement
}

func (*PipeStatement) statementNode() {}

// --- Chain: cmd1 && cmd2, cmd1 || cmd2, cmd1 & cmd2 ---

type ChainStatement struct {
	Left  Statement
	Op    string // "&&", "||", or "&"
	Right Statement
}

func (*ChainStatement) statementNode() {}

// --- Block: ( stmt1 & stmt2 & ... ) ---

type BlockStatement struct {
	Stmts []Statement
}

func (*BlockStatement) statementNode() {}

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

// NumericCompare handles: IF val1 LSS val2, IF val1 GTR val2, etc.
type NumericCompare struct {
	Left  []WordPart
	Op    string // "EQU", "NEQ", "LSS", "LEQ", "GTR", "GEQ"
	Right []WordPart
}

func (*NumericCompare) conditionNode() {}

type ExistCondition struct{ Path []WordPart }

func (*ExistCondition) conditionNode() {}

type DefinedCondition struct{ Name string }

func (*DefinedCondition) conditionNode() {}

type ErrorlevelCondition struct{ N int }

func (*ErrorlevelCondition) conditionNode() {}

// --- GOTO ---

type GotoStatement struct {
	Label     string     // static label (if known)
	LabelParts []WordPart // dynamic label (expanded at execution time)
}

func (*GotoStatement) statementNode() {}

// --- CALL ---

type CallStatement struct {
	Args []WordPart // Args[0] is the script/label
}

func (*CallStatement) statementNode() {}

// --- SET ---

type SetStatement struct {
	Name       string
	Value      [][]WordPart // word groups, joined with " " (preserves spaces)
	HasEquals  bool         // true if = was present (distinguishes set to empty from display)
	Arithmetic bool         // SET /A
	Prompt     bool         // SET /P
}

func (*SetStatement) statementNode() {}

// --- ECHO ---

type EchoStatement struct {
	Args      [][]WordPart
	Redirects []Redirect
	TurnOn    *bool // nil = not a toggle; true = ECHO ON, false = ECHO OFF
	Newline   bool  // ECHO. prints a blank line
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
	Variable string
	Kind     ForKind
	InList   []string
	Options  string
	Body     []Statement
}

func (*ForStatement) statementNode() {}

// --- EXIT ---

type ExitStatement struct {
	Code    int
	SubOnly bool // EXIT /B
}

func (*ExitStatement) statementNode() {}

// --- SHIFT ---

type ShiftStatement struct{}

func (*ShiftStatement) statementNode() {}

// --- SETLOCAL / ENDLOCAL ---

type SetlocalStatement struct {
	EnableDelayedExpansion  bool
	DisableDelayedExpansion bool
}

func (*SetlocalStatement) statementNode() {}

type EndlocalStatement struct{}

func (*EndlocalStatement) statementNode() {}

// --- Label (jump target) ---

type LabelStatement struct{ Name string }

func (*LabelStatement) statementNode() {}

// --- WordPart: a word is a sequence of these ---

type WordPart interface{ wordPartNode() }

type LiteralPart struct{ Text string }

func (*LiteralPart) wordPartNode() {}

type VarPart struct {
	Name       string // variable name, or "" for positional
	Positional int    // 0-9 if positional; -1 otherwise
}

func (*VarPart) wordPartNode() {}

// DelayedVarPart represents a !VAR! delayed-expansion variable reference.
type DelayedVarPart struct {
	Name string
}

func (*DelayedVarPart) wordPartNode() {}

// TildeVarPart represents %~1, %~dp0, etc. (parameter with tilde modifiers).
type TildeVarPart struct {
	Positional int    // the digit (0-9)
	Modifiers  string // modifier letters: d, p, n, x, f, s, a, t, z, or empty (strip quotes)
}

func (*TildeVarPart) wordPartNode() {}

// SubstringVarPart represents %VAR:~N% or %VAR:~N,M%.
type SubstringVarPart struct {
	Name      string
	Start     int
	Length    int
	HasLength bool
}

func (*SubstringVarPart) wordPartNode() {}
