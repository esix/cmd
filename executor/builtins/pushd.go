package builtins

import (
	"fmt"
	"os"

	"github.com/esix/cmd/env"
)

var dirStack []string

// Pushd implements PUSHD — save current dir and change to a new one.
func Pushd(args []string, _ *env.Env) int {
	wd, _ := os.Getwd()
	if len(args) == 0 {
		fmt.Println(wd)
		return 0
	}
	dir := toUnixPath(args[0])
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(os.Stderr, "The system cannot find the path specified: %s\n", args[0])
		return 1
	}
	dirStack = append(dirStack, wd)
	return 0
}

// Popd implements POPD — restore the previous directory.
func Popd(_ []string, _ *env.Env) int {
	if len(dirStack) == 0 {
		return 1
	}
	dir := dirStack[len(dirStack)-1]
	dirStack = dirStack[:len(dirStack)-1]
	os.Chdir(dir) //nolint:errcheck
	return 0
}
