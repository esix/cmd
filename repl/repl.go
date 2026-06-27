// Package repl provides the interactive Read-Eval-Print Loop.
package repl

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chzyer/readline"
	"github.com/esix/cmd/env"
	"github.com/esix/cmd/executor"
)

// Run starts the interactive shell loop.
func Run(e *env.Env) {
	histFile := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		histFile = filepath.Join(home, ".cmd_history")
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:              "C:\\> ",
		HistoryFile:         histFile,
		AutoComplete:        newCompleter(),
		InterruptPrompt:     "^C",
		EOFPrompt:           "exit",
		FuncFilterInputRune: filterInputRune,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "readline init error: %v\n", err)
		os.Exit(1)
	}
	defer rl.Close()

	ex := executor.New(e)

	for {
		line, err := rl.Readline()
		if err == readline.ErrInterrupt {
			continue
		}
		if err == io.EOF {
			fmt.Println()
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		code := ex.RunLine(line)
		e.ExitCode = code
	}
}
