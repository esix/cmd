package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/esix/cmd/env"
)

// Cd implements CD / CHDIR.
func Cd(args []string, e *env.Env) int {
	if len(args) == 0 {
		wd, _ := os.Getwd()
		fmt.Println(wd)
		return 0
	}
	dir := toUnixPath(args[0])
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(os.Stderr, "The system cannot find the path specified: %s\n", args[0])
		return 1
	}
	return 0
}

// Cls clears the terminal screen.
func Cls(_ []string, _ *env.Env) int {
	fmt.Print("\033[H\033[2J")
	return 0
}

// Pause waits for a keypress.
func Pause(_ []string, _ *env.Env) int {
	fmt.Print("Press any key to continue . . . ")
	buf := make([]byte, 1)
	os.Stdin.Read(buf) //nolint:errcheck
	fmt.Println()
	return 0
}

// Rem is a no-op (comment).
func Rem(_ []string, _ *env.Env) int { return 0 }

// Dir lists directory contents in a Windows-like format.
func Dir(args []string, e *env.Env) int {
	path := "."
	if len(args) > 0 {
		path = toUnixPath(args[0])
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "File Not Found: %s\n", path)
		return 1
	}

	abs, _ := filepath.Abs(path)
	fmt.Printf(" Directory of %s\n\n", abs)

	// Sort: directories first, then files
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	var totalFiles, totalDirs int
	var totalSize int64

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime().Format("01/02/2006  03:04 PM")
		if entry.IsDir() {
			fmt.Printf("%s    <DIR>          %s\n", mod, entry.Name())
			totalDirs++
		} else {
			fmt.Printf("%s    %14d %s\n", mod, info.Size(), entry.Name())
			totalFiles++
			totalSize += info.Size()
		}
	}

	fmt.Printf("\t%d File(s)  %d bytes\n", totalFiles, totalSize)
	fmt.Printf("\t%d Dir(s)\n", totalDirs)
	_ = time.Now() // keep time import used
	return 0
}

// toUnixPath converts a Windows-style path to Unix.
// Strips drive letter, converts backslashes to forward slashes.
func toUnixPath(p string) string {
	// Strip drive letter: C:\foo -> /foo, C:foo -> foo
	if len(p) >= 2 && p[1] == ':' {
		if len(p) >= 3 && p[2] == '\\' {
			p = "/" + p[3:]
		} else {
			p = p[2:]
		}
	}
	return strings.ReplaceAll(p, "\\", "/")
}
