package builtins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/esix/cmd/env"
)

// isBatFlag returns true for BAT-style flags like /Q, /F, /S (short flags).
func isBatFlag(s string) bool {
	return len(s) <= 3 && len(s) >= 2 && s[0] == '/'
}

// Del implements DEL / ERASE.
func Del(args []string, _ *env.Env) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}
	code := 0
	for _, pattern := range args {
		if isBatFlag(pattern) {
			continue
		}
		pattern = toUnixPath(pattern)
		matches, _ := filepath.Glob(pattern)
		if len(matches) == 0 {
			fmt.Fprintf(os.Stderr, "Could Not Find %s\n", pattern)
			code = 1
			continue
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				fmt.Fprintf(os.Stderr, "Access is denied: %s\n", m)
				code = 1
			}
		}
	}
	return code
}

// Copy implements COPY.
func Copy(args []string, _ *env.Env) int {
	// Strip flags
	var files []string
	for _, a := range args {
		if !isBatFlag(a) {
			files = append(files, toUnixPath(a))
		}
	}
	if len(files) < 2 {
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}
	src := files[0]
	dst := files[1]

	in, err := os.Open(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "The system cannot find the file specified: %s\n", src)
		return 1
	}
	defer in.Close()

	// If dst is a directory, use the source filename
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}

	out, err := os.Create(dst)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create file: %s\n", dst)
		return 1
	}
	defer out.Close()

	n, _ := io.Copy(out, in)
	fmt.Printf("        1 file(s) copied. (%d bytes)\n", n)
	return 0
}

// Move implements MOVE.
func Move(args []string, _ *env.Env) int {
	var files []string
	for _, a := range args {
		if !isBatFlag(a) {
			files = append(files, toUnixPath(a))
		}
	}
	if len(files) < 2 {
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}
	src := files[0]
	dst := files[1]

	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}

	if err := os.Rename(src, dst); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot move: %v\n", err)
		return 1
	}
	fmt.Printf("        1 file(s) moved.\n")
	return 0
}

// Mkdir implements MKDIR / MD.
func Mkdir(args []string, _ *env.Env) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}
	for _, dir := range args {
		dir = toUnixPath(dir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Cannot create directory: %s\n", dir)
			return 1
		}
	}
	return 0
}

// Rmdir implements RMDIR / RD.
func Rmdir(args []string, _ *env.Env) int {
	recursive := false
	var dirs []string
	for _, a := range args {
		if isBatFlag(a) {
			if strings.ToUpper(a) == "/S" {
				recursive = true
			}
			continue
		}
		dirs = append(dirs, toUnixPath(a))
	}
	for _, dir := range dirs {
		var err error
		if recursive {
			err = os.RemoveAll(dir)
		} else {
			err = os.Remove(dir)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cannot remove directory: %s\n", dir)
			return 1
		}
	}
	return 0
}

// Ren implements REN / RENAME.
func Ren(args []string, _ *env.Env) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "The syntax of the command is incorrect.")
		return 1
	}
	src := toUnixPath(args[0])
	dst := args[1]
	// REN keeps the file in the same directory
	dst = filepath.Join(filepath.Dir(src), dst)
	if err := os.Rename(src, dst); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot rename: %v\n", err)
		return 1
	}
	return 0
}
