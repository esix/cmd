package parser

import (
	"testing"
)

func TestParseEcho(t *testing.T) {
	stmts, err := ParseLine("ECHO hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts, want 1", len(stmts))
	}
	echo, ok := stmts[0].(*EchoStatement)
	if !ok {
		t.Fatalf("got %T, want *EchoStatement", stmts[0])
	}
	if len(echo.Args) != 2 {
		t.Errorf("got %d args, want 2", len(echo.Args))
	}
}

func TestParseEchoDot(t *testing.T) {
	stmts, _ := ParseLine("ECHO.")
	echo := stmts[0].(*EchoStatement)
	if !echo.Newline {
		t.Error("ECHO. should set Newline=true")
	}
}

func TestParseEchoOnOff(t *testing.T) {
	stmts, _ := ParseLine("ECHO OFF")
	echo := stmts[0].(*EchoStatement)
	if echo.TurnOn == nil || *echo.TurnOn != false {
		t.Error("ECHO OFF should set TurnOn=false")
	}
}

func TestParseSet(t *testing.T) {
	stmts, _ := ParseLine("SET FOO=bar")
	set := stmts[0].(*SetStatement)
	if set.Name != "FOO" {
		t.Errorf("name = %q, want FOO", set.Name)
	}
	if !set.HasEquals {
		t.Error("HasEquals should be true")
	}
}

func TestParseSetArithmetic(t *testing.T) {
	stmts, _ := ParseLine("SET /A X=1+2")
	set := stmts[0].(*SetStatement)
	if !set.Arithmetic {
		t.Error("Arithmetic should be true")
	}
	if set.Name != "X" {
		t.Errorf("name = %q, want X", set.Name)
	}
}

func TestParseIfStringCompare(t *testing.T) {
	stmts, _ := ParseLineWithOpts(`IF "a"=="a" ECHO yes`, false)
	ifStmt := stmts[0].(*IfStatement)
	_, ok := ifStmt.Condition.(*StringCompare)
	if !ok {
		t.Fatalf("condition is %T, want *StringCompare", ifStmt.Condition)
	}
	if len(ifStmt.Then) != 1 {
		t.Fatalf("then has %d stmts, want 1", len(ifStmt.Then))
	}
}

func TestParseIfNot(t *testing.T) {
	stmts, _ := ParseLine(`IF NOT "a"=="b" ECHO yes`)
	ifStmt := stmts[0].(*IfStatement)
	if !ifStmt.Not {
		t.Error("Not should be true")
	}
}

func TestParseIfNumeric(t *testing.T) {
	stmts, _ := ParseLineWithOpts("IF 5 LSS 10 ECHO yes", false)
	ifStmt := stmts[0].(*IfStatement)
	nc, ok := ifStmt.Condition.(*NumericCompare)
	if !ok {
		t.Fatalf("condition is %T, want *NumericCompare", ifStmt.Condition)
	}
	if nc.Op != "LSS" {
		t.Errorf("op = %q, want LSS", nc.Op)
	}
}

func TestParseIfExist(t *testing.T) {
	stmts, _ := ParseLine("IF EXIST /tmp ECHO yes")
	ifStmt := stmts[0].(*IfStatement)
	_, ok := ifStmt.Condition.(*ExistCondition)
	if !ok {
		t.Fatalf("condition is %T, want *ExistCondition", ifStmt.Condition)
	}
}

func TestParseGoto(t *testing.T) {
	stmts, _ := ParseLine("GOTO start")
	gt := stmts[0].(*GotoStatement)
	if len(gt.LabelParts) == 0 {
		t.Fatal("LabelParts should not be empty")
	}
}

func TestParseFor(t *testing.T) {
	stmts, _ := ParseLine("FOR /L %%I IN (1,1,5) DO ECHO %%I")
	f := stmts[0].(*ForStatement)
	if f.Kind != ForRange {
		t.Errorf("kind = %d, want ForRange", f.Kind)
	}
	if f.Variable != "I" {
		t.Errorf("variable = %q, want I", f.Variable)
	}
	if len(f.InList) != 3 {
		t.Errorf("InList = %v, want 3 items", f.InList)
	}
}

func TestParseExit(t *testing.T) {
	stmts, _ := ParseLine("EXIT /B 0")
	exit := stmts[0].(*ExitStatement)
	if !exit.SubOnly {
		t.Error("SubOnly should be true")
	}
	if exit.Code != 0 {
		t.Errorf("code = %d, want 0", exit.Code)
	}
}

func TestParseShift(t *testing.T) {
	stmts, _ := ParseLine("SHIFT")
	_, ok := stmts[0].(*ShiftStatement)
	if !ok {
		t.Fatalf("got %T, want *ShiftStatement", stmts[0])
	}
}

func TestParseChainAnd(t *testing.T) {
	stmts, _ := ParseLine("echo a && echo b")
	chain, ok := stmts[0].(*ChainStatement)
	if !ok {
		t.Fatalf("got %T, want *ChainStatement", stmts[0])
	}
	if chain.Op != "&&" {
		t.Errorf("op = %q, want &&", chain.Op)
	}
}

func TestParseChainOr(t *testing.T) {
	stmts, _ := ParseLine("echo a || echo b")
	chain := stmts[0].(*ChainStatement)
	if chain.Op != "||" {
		t.Errorf("op = %q, want ||", chain.Op)
	}
}

func TestParseChainAmpersand(t *testing.T) {
	stmts, _ := ParseLine("echo a & echo b")
	// & is a chain operator, parsed as ChainStatement
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts, want 1 (chain)", len(stmts))
	}
	chain, ok := stmts[0].(*ChainStatement)
	if !ok {
		t.Fatalf("got %T, want *ChainStatement", stmts[0])
	}
	if chain.Op != "&" {
		t.Errorf("op = %q, want &", chain.Op)
	}
}

func TestParseBlock(t *testing.T) {
	stmts, _ := ParseLine("( echo a & echo b )")
	block, ok := stmts[0].(*BlockStatement)
	if !ok {
		t.Fatalf("got %T, want *BlockStatement", stmts[0])
	}
	// Inside a block, & chains the two echos
	if len(block.Stmts) != 1 {
		t.Fatalf("block has %d stmts, want 1 (chain)", len(block.Stmts))
	}
	_, ok = block.Stmts[0].(*ChainStatement)
	if !ok {
		t.Fatalf("got %T, want *ChainStatement", block.Stmts[0])
	}
}

func TestParseIfBlock(t *testing.T) {
	stmts, _ := ParseLine(`IF "a"=="a" ( echo yes & echo also )`)
	ifStmt := stmts[0].(*IfStatement)
	// The block has one chain statement (echo yes & echo also)
	if len(ifStmt.Then) != 1 {
		t.Fatalf("then has %d stmts, want 1 (chain)", len(ifStmt.Then))
	}
	_, ok := ifStmt.Then[0].(*ChainStatement)
	if !ok {
		t.Fatalf("got %T, want *ChainStatement", ifStmt.Then[0])
	}
}

func TestParseSetlocal(t *testing.T) {
	stmts, _ := ParseLine("SETLOCAL EnableDelayedExpansion")
	sl := stmts[0].(*SetlocalStatement)
	if !sl.EnableDelayedExpansion {
		t.Error("EnableDelayedExpansion should be true")
	}
}

func TestParseTildeVar(t *testing.T) {
	// %~1 inside a token
	parts := parseWordParts("x=%~1")
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	tilde, ok := parts[1].(*TildeVarPart)
	if !ok {
		t.Fatalf("parts[1] is %T, want *TildeVarPart", parts[1])
	}
	if tilde.Positional != 1 {
		t.Errorf("positional = %d, want 1", tilde.Positional)
	}
}

func TestParseTildeVarWithModifiers(t *testing.T) {
	parts := parseWordParts("%~dp0")
	tilde := parts[0].(*TildeVarPart)
	if tilde.Modifiers != "dp" {
		t.Errorf("modifiers = %q, want dp", tilde.Modifiers)
	}
	if tilde.Positional != 0 {
		t.Errorf("positional = %d, want 0", tilde.Positional)
	}
}

func TestParseSubstring(t *testing.T) {
	parts := parseWordParts("%VAR:~1%")
	sub, ok := parts[0].(*SubstringVarPart)
	if !ok {
		t.Fatalf("got %T, want *SubstringVarPart", parts[0])
	}
	if sub.Name != "VAR" {
		t.Errorf("name = %q, want VAR", sub.Name)
	}
	if sub.Start != 1 {
		t.Errorf("start = %d, want 1", sub.Start)
	}
	if sub.HasLength {
		t.Error("HasLength should be false")
	}
}

func TestParseSubstringWithLength(t *testing.T) {
	parts := parseWordParts("%VAR:~0,5%")
	sub := parts[0].(*SubstringVarPart)
	if sub.Start != 0 || sub.Length != 5 || !sub.HasLength {
		t.Errorf("got start=%d len=%d has=%v, want 0,5,true", sub.Start, sub.Length, sub.HasLength)
	}
}

func TestParseSubstringNegative(t *testing.T) {
	parts := parseWordParts("%VAR:~-3%")
	sub := parts[0].(*SubstringVarPart)
	if sub.Start != -3 {
		t.Errorf("start = %d, want -3", sub.Start)
	}
}
