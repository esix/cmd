package builtins

import (
	"fmt"
	"os"
	"strings"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/expander"
)

// Cmd implements a minimal "cmd /C command" and "cmd /V:ON /C command".
func Cmd(args []string, e *env.Env) int {
	delayedExpand := false

	// Find /C flag — everything after it is the command to run
	cmdIdx := -1
	for i, a := range args {
		upper := strings.ToUpper(a)
		if upper == "/V:ON" || upper == "/V" {
			delayedExpand = true
			continue
		}
		if upper == "/C" {
			cmdIdx = i + 1
			break
		}
	}
	if cmdIdx < 0 || cmdIdx >= len(args) {
		fmt.Fprintln(os.Stderr, "cmd: /C flag required")
		return 1
	}

	cmdStr := strings.Join(args[cmdIdx:], " ")

	// Expand !VAR! if /V:ON was specified
	if delayedExpand {
		cmdStr = expander.ExpandBangs(cmdStr, e)
	}

	// echo(text — output text after (. Rejoin without space after echo(
	if strings.HasPrefix(strings.ToUpper(cmdStr), "ECHO(") {
		// Find where echo( ends and rejoin the rest tightly
		echoIdx := 5 // len("echo(")
		// The first arg might be just "echo(" with text in the next arg
		text := cmdStr[echoIdx:]
		text = strings.TrimLeft(text, " ") // remove artifact space from arg joining
		fmt.Println(text)
		return 0
	}

	if strings.HasPrefix(strings.ToUpper(cmdStr), "ECHO ") {
		fmt.Println(cmdStr[5:])
		return 0
	}

	fmt.Fprintf(os.Stderr, "cmd /C: unsupported command: %s\n", cmdStr)
	return 1
}
