// cmd — a BAT-syntax shell for Unix/Linux.
//
// Usage:
//
//	cmd              start interactive shell
//	cmd script.bat   run a BAT file
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/executor"
	"github.com/esix/cmd/repl"
)

func main() {
	e := env.New()

	if len(os.Args) >= 2 {
		// File mode: cmd script.bat [args...]
		path := os.Args[1]
		args := os.Args[2:]
		ex := executor.New(e)
		code := ex.RunFile(path, args)
		os.Exit(code)
	}

	// Interactive mode
	fmt.Println("cmd — BAT shell for Unix. Type EXIT to quit.")

	// Run ~/autoexec.bat if it exists
	if home, err := os.UserHomeDir(); err == nil {
		autoexec := filepath.Join(home, "autoexec.bat")
		if _, err := os.Stat(autoexec); err == nil {
			ex := executor.New(e)
			ex.RunFile(autoexec, nil)
		}
	}

	repl.Run(e)
}
