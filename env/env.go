// Package env manages the shell's variable environment.
//
// BAT variables are case-insensitive, so all keys are stored uppercase.
// SETLOCAL/ENDLOCAL is modelled as a scope stack: Push creates a snapshot,
// Pop restores it.
package env

import (
	"fmt"
	"os"
	"strings"
)

// Env holds shell variables and the current exit code.
type Env struct {
	vars             map[string]string
	stack            []scope // for SETLOCAL / ENDLOCAL
	ExitCode         int
	FileMode         bool // true when executing a .bat file (affects %% vs % in FOR)
	Echo             bool // ECHO ON / ECHO OFF
	DelayedExpansion bool // SETLOCAL EnableDelayedExpansion
}

// scope captures the full environment state at SETLOCAL time.
type scope struct {
	vars             map[string]string
	delayedExpansion bool
}

// New creates an Env pre-populated with the process environment.
func New() *Env {
	e := &Env{
		vars: make(map[string]string),
		Echo: true,
	}
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			e.vars[strings.ToUpper(parts[0])] = parts[1]
		}
	}
	return e
}

// Get returns the value of a variable (case-insensitive).
// Missing variables return "".
func (e *Env) Get(name string) string {
	// Magic variable
	if strings.ToUpper(name) == "ERRORLEVEL" {
		return fmt.Sprint(e.ExitCode) // see below — we'll fix the import
	}
	return e.vars[strings.ToUpper(name)]
}

// Set stores a variable.
func (e *Env) Set(name, value string) {
	e.vars[strings.ToUpper(name)] = value
}

// Unset removes a variable (SET VAR= with empty value deletes it in BAT).
func (e *Env) Unset(name string) {
	delete(e.vars, strings.ToUpper(name))
}

// Push snapshots the current environment (SETLOCAL).
func (e *Env) Push() {
	snapshot := make(map[string]string, len(e.vars))
	for k, v := range e.vars {
		snapshot[k] = v
	}
	e.stack = append(e.stack, scope{
		vars:             snapshot,
		delayedExpansion: e.DelayedExpansion,
	})
}

// All returns a copy of all variables.
func (e *Env) All() map[string]string {
	copy := make(map[string]string, len(e.vars))
	for k, v := range e.vars {
		copy[k] = v
	}
	return copy
}

// Pop restores the previous environment snapshot (ENDLOCAL).
// Returns false if there is no saved scope.
func (e *Env) Pop() bool {
	if len(e.stack) == 0 {
		return false
	}
	top := e.stack[len(e.stack)-1]
	e.stack = e.stack[:len(e.stack)-1]
	e.vars = top.vars
	e.DelayedExpansion = top.delayedExpansion
	return true
}
