package builtins

import (
	"testing"

	"github.com/esix/cmd/env"
)

func TestSetArithmetic(t *testing.T) {
	e := env.New()
	code := Set([]string{"/A", "X=2+3"}, e)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := e.Get("X"); got != "5" {
		t.Errorf("X = %q, want %q", got, "5")
	}
}

func TestSetArithmeticMultiply(t *testing.T) {
	e := env.New()
	Set([]string{"/A", "X=10*5"}, e)
	if got := e.Get("X"); got != "50" {
		t.Errorf("X = %q, want %q", got, "50")
	}
}

func TestSetArithmeticWithSpaces(t *testing.T) {
	e := env.New()
	Set([]string{"/A", "X = 3 + 4"}, e)
	if got := e.Get("X"); got != "7" {
		t.Errorf("X = %q, want %q", got, "7")
	}
}

func TestSetArithmeticDivision(t *testing.T) {
	e := env.New()
	Set([]string{"/A", "X=10/3"}, e)
	if got := e.Get("X"); got != "3" {
		t.Errorf("X = %q, want %q", got, "3")
	}
}

func TestSetArithmeticModulo(t *testing.T) {
	e := env.New()
	Set([]string{"/A", "X=10%3"}, e)
	if got := e.Get("X"); got != "1" {
		t.Errorf("X = %q, want %q", got, "1")
	}
}

func TestSetDisplayVar(t *testing.T) {
	e := env.New()
	e.Set("TESTVAR", "hello")
	code := Set([]string{"TESTVAR"}, e)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestSetDisplayMissing(t *testing.T) {
	e := env.New()
	code := Set([]string{"NONEXISTENT_VAR_XYZ"}, e)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for missing var", code)
	}
}
