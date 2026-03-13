package util

// SanitizeRef converts an image reference to a safe directory name
// by replacing /, :, and @ with underscores.
func SanitizeRef(ref string) string {
	safe := make([]byte, len(ref))
	for i, c := range ref {
		if c == '/' || c == ':' || c == '@' {
			safe[i] = '_'
		} else {
			safe[i] = byte(c)
		}
	}
	return string(safe)
}
