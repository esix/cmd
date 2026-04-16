package repl

import (
	"os"
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
// Paths use backslash separators to match Windows cmd.exe style.
func fileCompleter(prefix string) []string {
	// Convert backslashes to forward slashes for OS filesystem calls
	osPrefix := strings.ReplaceAll(prefix, "\\", "/")

	// Split into directory and partial filename
	dir := "."
	base := osPrefix
	if i := strings.LastIndex(osPrefix, "/"); i >= 0 {
		dir = osPrefix[:i]
		base = osPrefix[i+1:]
		if dir == "" {
			dir = "/"
		}
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
		// Build path with backslash separators (Windows cmd style)
		var full string
		if dir == "." {
			full = name
		} else {
			full = dir + "/" + name
		}
		// Convert to backslash
		full = strings.ReplaceAll(full, "/", "\\")
		if e.IsDir() {
			full += "\\"
		}
		matches = append(matches, full)
	}
	return matches
}
