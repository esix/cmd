package builtins

import (
	"fmt"
	"os"
	"strings"

	"github.com/esix/cmd/env"
)

// Type implements the TYPE command — prints the contents of one or more files.
func Type(args []string, e *env.Env) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}
	code := 0
	for _, path := range args {
		// `nul` is a Windows pseudo-device producing no data; commonly used as
		// `type nul > file` to truncate or create an empty file.
		if strings.ToLower(path) == "nul" {
			continue
		}
		path = toUnixPath(path)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "The system cannot find the file specified: %s\n", path)
			code = 1
			continue
		}
		// cmd.exe's TYPE writes the file content verbatim — it does NOT add a
		// trailing newline. BAT code relies on this (e.g. printing padding
		// spaces via `type` without a line break).
		os.Stdout.Write(data)
	}
	return code
}
