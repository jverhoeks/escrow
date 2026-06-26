// Package pkgref parses escrow's "name@version" package reference strings,
// shared so the split logic isn't duplicated across handlers, the event log,
// dlstats, and the dashboard. See #77.
package pkgref

import "strings"

// Split splits "name@version" on the LAST '@'. A leading '@' (scoped npm names
// like "@scope/pkg@1.0.0") is preserved in the name. Returns (pkg, "") when
// there is no version delimiter.
func Split(pkg string) (name, version string) {
	i := strings.LastIndex(pkg, "@")
	if i <= 0 {
		return pkg, ""
	}
	return pkg[:i], pkg[i+1:]
}

// Name returns just the name part of "name@version".
func Name(pkg string) string {
	n, _ := Split(pkg)
	return n
}
