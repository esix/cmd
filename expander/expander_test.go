package expander

import (
	"testing"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/parser"
)

func TestExpandLiteral(t *testing.T) {
	e := env.New()
	parts := []parser.WordPart{&parser.LiteralPart{Text: "hello"}}
	if got := ExpandWord(parts, e, nil); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestExpandVar(t *testing.T) {
	e := env.New()
	e.Set("NAME", "world")
	parts := []parser.WordPart{
		&parser.LiteralPart{Text: "hello "},
		&parser.VarPart{Name: "NAME", Positional: -1},
	}
	if got := ExpandWord(parts, e, nil); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestExpandPositional(t *testing.T) {
	e := env.New()
	pos := []string{"script.bat", "arg1", "arg2"}
	parts := []parser.WordPart{
		&parser.VarPart{Positional: 1},
	}
	if got := ExpandWord(parts, e, pos); got != "arg1" {
		t.Errorf("got %q, want %q", got, "arg1")
	}
}

func TestExpandDelayed(t *testing.T) {
	e := env.New()
	e.Set("X", "42")
	parts := []parser.WordPart{
		&parser.DelayedVarPart{Name: "X"},
	}
	if got := ExpandWord(parts, e, nil); got != "42" {
		t.Errorf("got %q, want %q", got, "42")
	}
}

func TestExpandDelayedInsideLiteral(t *testing.T) {
	e := env.New()
	e.Set("X", "val")
	e.DelayedExpansion = true
	parts := []parser.WordPart{
		&parser.LiteralPart{Text: "prefix!X!suffix"},
	}
	if got := ExpandWord(parts, e, nil); got != "prefixvalsuffix" {
		t.Errorf("got %q, want %q", got, "prefixvalsuffix")
	}
}

func TestExpandTildeStripQuotes(t *testing.T) {
	e := env.New()
	pos := []string{"script", `"hello world"`}
	parts := []parser.WordPart{
		&parser.TildeVarPart{Positional: 1, Modifiers: ""},
	}
	if got := ExpandWord(parts, e, pos); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestExpandSubstringFromStart(t *testing.T) {
	e := env.New()
	e.Set("S", "HelloWorld")
	parts := []parser.WordPart{
		&parser.SubstringVarPart{Name: "S", Start: 5},
	}
	if got := ExpandWord(parts, e, nil); got != "World" {
		t.Errorf("got %q, want %q", got, "World")
	}
}

func TestExpandSubstringWithLength(t *testing.T) {
	e := env.New()
	e.Set("S", "HelloWorld")
	parts := []parser.WordPart{
		&parser.SubstringVarPart{Name: "S", Start: 0, Length: 5, HasLength: true},
	}
	if got := ExpandWord(parts, e, nil); got != "Hello" {
		t.Errorf("got %q, want %q", got, "Hello")
	}
}

func TestExpandSubstringNegativeStart(t *testing.T) {
	e := env.New()
	e.Set("S", "HelloWorld")
	parts := []parser.WordPart{
		&parser.SubstringVarPart{Name: "S", Start: -3},
	}
	if got := ExpandWord(parts, e, nil); got != "rld" {
		t.Errorf("got %q, want %q", got, "rld")
	}
}

func TestExpandSubstringNegativeLength(t *testing.T) {
	e := env.New()
	e.Set("S", "HelloWorld")
	parts := []parser.WordPart{
		&parser.SubstringVarPart{Name: "S", Start: 0, Length: -3, HasLength: true},
	}
	if got := ExpandWord(parts, e, nil); got != "HelloWo" {
		t.Errorf("got %q, want %q", got, "HelloWo")
	}
}

func TestEarlyExpandPercent(t *testing.T) {
	e := env.New()
	e.Set("NAME", "world")
	got := ExpandPercent("hello %NAME%!", e, nil)
	if got != "hello world!" {
		t.Errorf("got %q, want %q", got, "hello world!")
	}
}

func TestEarlyExpandPositional(t *testing.T) {
	e := env.New()
	pos := []string{"script", "arg1"}
	got := ExpandPercent("echo %1", e, pos)
	if got != "echo arg1" {
		t.Errorf("got %q, want %q", got, "echo arg1")
	}
}

func TestEarlyExpandTilde(t *testing.T) {
	e := env.New()
	pos := []string{"script", `"quoted"`}
	got := ExpandPercent("echo %~1", e, pos)
	if got != "echo quoted" {
		t.Errorf("got %q, want %q", got, "echo quoted")
	}
}

func TestEarlyExpandSubstring(t *testing.T) {
	e := env.New()
	e.Set("S", " 1 2 3")
	got := ExpandPercent("echo %S:~1%", e, nil)
	if got != "echo 1 2 3" {
		t.Errorf("got %q, want %q", got, "echo 1 2 3")
	}
}

func TestEarlyExpandDoublePct(t *testing.T) {
	e := env.New()
	got := ExpandPercent("FOR %%I IN (a) DO echo %%I", e, nil)
	if got != "FOR %%I IN (a) DO echo %%I" {
		t.Errorf("got %q, want %q", got, "FOR %%I IN (a) DO echo %%I")
	}
}

func TestEarlyExpandBangUntouched(t *testing.T) {
	e := env.New()
	e.Set("X", "val")
	got := ExpandPercent("echo !X!", e, nil)
	if got != "echo !X!" {
		t.Errorf("got %q, want %q — early expansion should not touch !", got, "echo !X!")
	}
}
