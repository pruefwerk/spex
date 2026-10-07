package definition

import "strings"

// Extension selects the existing parser without interpreting test semantics.
func Extension(source string) string {
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "@") {
			continue
		}
		if strings.HasPrefix(line, "Feature:") {
			return ".feature"
		}
		break
	}
	return ".spex"
}
