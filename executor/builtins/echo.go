package builtins

import (
	"fmt"
	"strings"

	"github.com/esix/cmd/env"
)

// Echo implements the ECHO command.
// Special cases:
//   - ECHO with no args: prints "ECHO is on." or "ECHO is off."
//   - ECHO. (no space, dot): prints a blank line — handled in executor before args splitting
func Echo(args []string, e *env.Env) int {
	if len(args) == 0 {
		if e.Echo {
			fmt.Print("ECHO is on.\r\n")
		} else {
			fmt.Print("ECHO is off.\r\n")
		}
		return 0
	}
	fmt.Print(strings.Join(args, " ") + "\r\n")
	return 0
}
