// Package builtins registers all built-in BAT commands.
package builtins

import "github.com/esix/cmd/env"

// Func is the signature every builtin must implement.
// args are the already-expanded arguments (argv[1:]).
// Returns the exit code.
type Func func(args []string, e *env.Env) int

// Registry maps uppercased command names to their implementation.
var Registry = map[string]Func{
	"ECHO":  Echo,
	"SET":   Set,
	"CD":    Cd,
	"CHDIR": Cd,
	"CLS":   Cls,
	"PAUSE": Pause,
	"DIR":   Dir,
	"REM":   Rem,
	"TYPE":   Type,
	"SHIFT":  func(_ []string, _ *env.Env) int { return 0 },
	"PUSHD":  Pushd,
	"POPD":   Popd,
	"DEL":    Del,
	"ERASE":  Del,
	"COPY":   Copy,
	"MOVE":   Move,
	"MKDIR":  Mkdir,
	"MD":     Mkdir,
	"RMDIR":  Rmdir,
	"RD":     Rmdir,
	"REN":    Ren,
	"RENAME":   Ren,
	"CERTUTIL": Certutil,
	"CMD":      Cmd,
	"FINDSTR":  Findstr,
	// CHCP sets the console code page on Windows; a no-op on Unix.
	"CHCP": func(_ []string, _ *env.Env) int { return 0 },
	// VER / TITLE / COLOR are cosmetic Windows console commands; no-op.
	"VER":   func(_ []string, _ *env.Env) int { return 0 },
	"TITLE": func(_ []string, _ *env.Env) int { return 0 },
	"COLOR": func(_ []string, _ *env.Env) int { return 0 },
}
