package env

import "testing"

func TestGetSet(t *testing.T) {
	e := New()
	e.Set("FOO", "bar")
	if got := e.Get("FOO"); got != "bar" {
		t.Errorf("Get FOO = %q, want %q", got, "bar")
	}
	// case insensitive
	if got := e.Get("foo"); got != "bar" {
		t.Errorf("Get foo = %q, want %q", got, "bar")
	}
}

func TestUnset(t *testing.T) {
	e := New()
	e.Set("X", "1")
	e.Unset("X")
	if got := e.Get("X"); got != "" {
		t.Errorf("Get X after Unset = %q, want empty", got)
	}
}

func TestErrorlevel(t *testing.T) {
	e := New()
	e.ExitCode = 42
	if got := e.Get("ERRORLEVEL"); got != "42" {
		t.Errorf("Get ERRORLEVEL = %q, want %q", got, "42")
	}
}

func TestPushPop(t *testing.T) {
	e := New()
	e.Set("A", "original")
	e.Push()
	e.Set("A", "modified")
	if got := e.Get("A"); got != "modified" {
		t.Errorf("after push+set: %q, want %q", got, "modified")
	}
	e.Pop()
	if got := e.Get("A"); got != "original" {
		t.Errorf("after pop: %q, want %q", got, "original")
	}
}

func TestPushPopDelayedExpansion(t *testing.T) {
	e := New()
	e.DelayedExpansion = false
	e.Push()
	e.DelayedExpansion = true
	if !e.DelayedExpansion {
		t.Error("delayed expansion should be true")
	}
	e.Pop()
	if e.DelayedExpansion {
		t.Error("delayed expansion should be restored to false after Pop")
	}
}

func TestPopEmpty(t *testing.T) {
	e := New()
	if e.Pop() {
		t.Error("Pop on empty stack should return false")
	}
}

func TestAll(t *testing.T) {
	e := New()
	e.Set("TEST1", "a")
	e.Set("TEST2", "b")
	all := e.All()
	if all["TEST1"] != "a" || all["TEST2"] != "b" {
		t.Errorf("All returned unexpected: %v", all)
	}
	// modifying the copy should not affect the env
	all["TEST1"] = "changed"
	if e.Get("TEST1") != "a" {
		t.Error("modifying All() return value should not affect env")
	}
}
