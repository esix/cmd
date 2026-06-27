//go:build windows
// +build windows

package repl

import "github.com/chzyer/readline"

func filterInputRune(r rune) (rune, bool) {
	switch r {
	case readline.MetaBackward:
		return 'b', true
	case readline.MetaForward:
		return 'f', true
	case readline.MetaDelete:
		return 'd', true
	case readline.MetaBackspace:
		return readline.CharBackspace, true
	default:
		return r, true
	}
}
