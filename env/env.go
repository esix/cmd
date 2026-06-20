// Package env manages the shell's variable environment.
//
// BAT variables are case-insensitive, so all keys are stored uppercase.
// SETLOCAL/ENDLOCAL is modelled as a scope stack: Push creates a snapshot,
// Pop restores it.
package env

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

// Env holds shell variables and the current exit code.
type Env struct {
	vars             map[string]string
	names            map[string]string // UPPER key -> original-case name (cmd.exe preserves the case used at creation)
	stack            []scope // for SETLOCAL / ENDLOCAL
	ExitCode         int
	FileMode         bool // true when executing a .bat file (affects %% vs % in FOR)
	Echo             bool // ECHO ON / ECHO OFF
	DelayedExpansion bool // SETLOCAL EnableDelayedExpansion
}

// scope captures the full environment state at SETLOCAL time.
type scope struct {
	vars             map[string]string
	names            map[string]string
	delayedExpansion bool
}

// New creates an Env pre-populated with the process environment.
func New() *Env {
	e := &Env{
		vars:  make(map[string]string),
		names: make(map[string]string),
		Echo:  true,
	}
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			upper := strings.ToUpper(parts[0])
			e.vars[upper] = parts[1]
			e.names[upper] = parts[0]
		}
	}
	// Map Windows env vars to Unix equivalents
	if e.vars["TEMP"] == "" {
		if tmpdir := os.Getenv("TMPDIR"); tmpdir != "" {
			e.vars["TEMP"] = strings.TrimSuffix(tmpdir, "/")
		} else {
			e.vars["TEMP"] = "/tmp"
		}
	}
	if e.vars["TMP"] == "" {
		e.vars["TMP"] = e.vars["TEMP"]
	}
	return e
}

// Get returns the value of a variable (case-insensitive).
// Missing variables return "".
func (e *Env) Get(name string) string {
	upper := strings.ToUpper(name)
	// Magic variables
	if upper == "ERRORLEVEL" {
		return fmt.Sprint(e.ExitCode)
	}
	// %RANDOM%: a fresh pseudo-random integer 0..32767 on each read, unless
	// the user has explicitly assigned RANDOM (which shadows the dynamic one,
	// matching cmd.exe).  The global rand source is auto-seeded (Go >=1.20).
	if upper == "RANDOM" {
		if v, ok := e.vars["RANDOM"]; ok {
			return v
		}
		return strconv.Itoa(rand.Intn(32768))
	}
	return e.vars[upper]
}

// Set stores a variable.  The original-case spelling is remembered the
// first time a name is created (cmd.exe keeps that case in SET listings).
func (e *Env) Set(name, value string) {
	upper := strings.ToUpper(name)
	if _, exists := e.vars[upper]; !exists {
		e.names[upper] = name
	}
	e.vars[upper] = value
}

// Unset removes a variable (SET VAR= with empty value deletes it in BAT).
func (e *Env) Unset(name string) {
	upper := strings.ToUpper(name)
	delete(e.vars, upper)
	delete(e.names, upper)
}

// DisplayName returns the original-case spelling of a variable name as
// first created (falling back to the given name if unknown).
func (e *Env) DisplayName(name string) string {
	if orig, ok := e.names[strings.ToUpper(name)]; ok {
		return orig
	}
	return name
}

// Push snapshots the current environment (SETLOCAL).
func (e *Env) Push() {
	snapshot := make(map[string]string, len(e.vars))
	for k, v := range e.vars {
		snapshot[k] = v
	}
	nameSnap := make(map[string]string, len(e.names))
	for k, v := range e.names {
		nameSnap[k] = v
	}
	e.stack = append(e.stack, scope{
		vars:             snapshot,
		names:            nameSnap,
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
	e.names = top.names
	e.DelayedExpansion = top.delayedExpansion
	return true
}
