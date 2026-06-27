//go:build !windows
// +build !windows

package repl

func filterInputRune(r rune) (rune, bool) {
	return r, true
}
