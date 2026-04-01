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
	repl.Run(e)
}
