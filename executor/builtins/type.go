package builtins

import (
	"fmt"
	"os"

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
		path = toUnixPath(path)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "The system cannot find the file specified: %s\n", path)
			code = 1
			continue
		}
		os.Stdout.Write(data)
		// Ensure output ends with a newline
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Println()
		}
	}
	return code
}
