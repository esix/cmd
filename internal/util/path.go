// Package util provides path conversion helpers between Windows and Unix styles.
package util

import "strings"

// ToUnix converts a Windows-style path to a Unix path.
// - Strips drive letter (C:\foo -> /foo, C:foo -> foo)
// - Converts backslashes to forward slashes
func ToUnix(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		if len(p) >= 3 && p[2] == '\\' {
			p = "/" + p[3:]
		} else {
			p = p[2:]
		}
	}
	return strings.ReplaceAll(p, "\\", "/")
}
