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
			fmt.Println("ECHO is on.")
		} else {
			fmt.Println("ECHO is off.")
		}
		return 0
	}
	fmt.Println(strings.Join(args, " "))
	return 0
}
