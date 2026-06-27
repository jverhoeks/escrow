// Package pkgname validates package names/paths before they're interpolated into
// upstream registry URLs, preventing path-traversal and query/fragment injection
// on the (trusted) upstream host. See #98.
package pkgname

import "strings"

// Safe reports whether s is a safe package name or repository path to place in
// an upstream URL path. It allows the characters real names use across
// slash-separated segments (alphanumerics, '.', '-', '_', '@', '+', '~', '/')
// and rejects: empty/over-long input, any "."/".."/empty path segment
// (traversal), control characters, spaces, and the injection-prone '?', '#', '\'.
func Safe(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f { // control chars + space
			return false
		}
		switch r {
		case '?', '#', '\\':
			return false
		}
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
