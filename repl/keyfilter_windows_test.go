//go:build windows
// +build windows

package repl

import (
	"testing"

	"github.com/chzyer/readline"
)

func TestFilterInputRuneUnsticksWindowsMetaKeys(t *testing.T) {
	tests := map[rune]rune{
		readline.MetaBackward:  'b',
		readline.MetaForward:   'f',
		readline.MetaDelete:    'd',
		readline.MetaBackspace: readline.CharBackspace,
		'x':                    'x',
	}

	for in, want := range tests {
		got, ok := filterInputRune(in)
		if !ok {
			t.Fatalf("filterInputRune(%v) discarded input", in)
		}
		if got != want {
			t.Fatalf("filterInputRune(%v) = %v, want %v", in, got, want)
		}
	}
}
