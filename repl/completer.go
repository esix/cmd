package repl

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/chzyer/readline"
)

// newCompleter returns a tab completer that suggests:
//   - BAT builtin commands
//   - Files in the current directory
var batCommands = []string{
	"ECHO", "SET", "IF", "FOR", "GOTO", "CALL", "EXIT",
	"CD", "CHDIR", "DIR", "CLS", "PAUSE", "REM",
	"SETLOCAL", "ENDLOCAL",
}

func newCompleter() readline.AutoCompleter {
	return readline.NewPrefixCompleter(
		buildItems()...,
	)
}

func buildItems() []readline.PrefixCompleterInterface {
	items := make([]readline.PrefixCompleterInterface, 0, len(batCommands))
	for _, cmd := range batCommands {
		c := cmd // capture
		items = append(items, readline.PcItem(c,
			readline.PcItemDynamic(fileCompleter),
		))
		// also lowercase variant
		items = append(items, readline.PcItem(strings.ToLower(c),
			readline.PcItemDynamic(fileCompleter),
		))
	}
	// Always offer file completion at top level too
	items = append(items, readline.PcItemDynamic(fileCompleter))
	return items
}

// fileCompleter returns file/dir names matching the given prefix.
func fileCompleter(prefix string) []string {
	dir := filepath.Dir(prefix)
	base := filepath.Base(prefix)
	if prefix == "" || strings.HasSuffix(prefix, "/") {
		dir = prefix
		base = ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var matches []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			full += "/"
		}
		matches = append(matches, full)
	}
	return matches
}
