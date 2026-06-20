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

// DirList implements the subset of `dir` that batch scripts consume
// programmatically: /B (bare names), /S (recurse, full paths), /A-D (files
// only). It returns the matching entries and whether the args were a form it
// could handle. Without /S the entries are bare names (filepath.Base) and
// sorted alphabetically — matching cmd.exe's `dir /b`. With /S they are
// absolute paths in lexical walk order (what `dir /s /b` yields).
//
// The pattern may be a glob ("dir/*.bat"), an existing directory (its contents
// are listed), or empty (the current directory). Backslashes are normalized.
func DirList(args []string) (lines []string, bare bool, ok bool) {
	recursive := false
	filesOnly := false
	pattern := ""
	for _, a := range args {
		u := strings.ToUpper(a)
		switch {
		case u == "/B":
			bare = true
		case u == "/S":
			recursive = true
		case strings.HasPrefix(u, "/A"):
			if strings.Contains(u, "-D") {
				filesOnly = true
			}
		case isDirFlag(a):
			// other flags (/O, /T, /W, ...) — ignore
		default:
			pattern = strings.Trim(a, "\"")
		}
	}
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	if pattern == "" {
		pattern = "."
	}

	// Split the pattern into a base directory and a glob. An existing
	// directory means "list its contents"; otherwise use Dir/Base of the
	// pattern itself.
	var dir, glob string
	if info, err := os.Stat(pattern); err == nil && info.IsDir() {
		dir, glob = pattern, "*"
	} else {
		dir, glob = filepath.Dir(pattern), filepath.Base(pattern)
		if dir == "" {
			dir = "."
		}
	}

	if recursive {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || path == dir {
				return nil
			}
			if info.IsDir() && filesOnly {
				return nil
			}
			if m, _ := filepath.Match(glob, filepath.Base(path)); m {
				abs, _ := filepath.Abs(path)
				lines = append(lines, abs)
			}
			return nil
		})
	} else {
		entries, _ := filepath.Glob(filepath.Join(dir, glob))
		for _, m := range entries {
			info, err := os.Stat(m)
			if err != nil {
				continue
			}
			if filesOnly && info.IsDir() {
				continue
			}
			lines = append(lines, filepath.Base(m))
		}
		sort.Strings(lines)
	}
	return lines, bare, true
}

// isDirFlag reports whether arg is a `dir` switch (e.g. /B, /S, /A-D, /O:N)
// rather than a path. cmd switches are `/` + a single option letter, optionally
// followed by `:` or `-` and a value. A Unix absolute path like "/tmp/iss"
// begins with `/` too but is NOT a flag — distinguished because its second
// character is followed by more letters (no `:`/`-` separator), not because a
// dir flag is ever multi-letter.
func isDirFlag(arg string) bool {
	if len(arg) < 2 || arg[0] != '/' {
		return false
	}
	c := arg[1]
	isLetterOrDigit := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
	if !isLetterOrDigit {
		return false
	}
	// "/B" — bare single-letter flag.
	if len(arg) == 2 {
		return true
	}
	// "/A-D", "/O:N" — single letter then a value separator.
	return arg[2] == ':' || arg[2] == '-'
}

// firstNonFlag returns the first argument that is not a dir switch, or "".
func firstNonFlag(args []string) string {
	for _, a := range args {
		if !isDirFlag(a) {
			return a
		}
	}
	return ""
}

// Dir lists directory contents in a Windows-like format.
func Dir(args []string, e *env.Env) int {
	// Bare mode (/B): emit just names (or full paths with /S), no header —
	// the form scripts capture via `for /f`.
	for _, a := range args {
		if strings.EqualFold(a, "/B") {
			lines, _, _ := DirList(args)
			for _, ln := range lines {
				fmt.Print(ln + "\r\n")
			}
			return 0
		}
	}

	path := "."
	if p := firstNonFlag(args); p != "" {
		path = toUnixPath(p)
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
