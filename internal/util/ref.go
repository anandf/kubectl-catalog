package util

import "strings"

// SanitizeRef converts an image reference to a safe directory name
// by replacing /, :, and @ with underscores.
func SanitizeRef(ref string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '@' {
			return '_'
		}
		return r
	}, ref)
}
