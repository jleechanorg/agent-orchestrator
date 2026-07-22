package skeptic

import (
	"regexp"
	"strings"
)

// globMatch reports whether path matches a simplified glob pattern
// supporting `*` (any run of non-slash chars), `**` (any run of chars,
// including slashes — i.e. crosses path segments), and `?` (a single
// non-slash char). This is a narrower subset than the `minimatch` npm
// package skeptic.ts uses (no brace expansion, no `[...]` character
// classes, no negation) — sufficient for the --exclude-paths flag's
// documented use case (simple patterns like "**/*.md" or "docs/**"), not a
// faithful full port of minimatch. minimatch's `{ dot: true }` option
// (patterns match dotfiles too) is matched implicitly: this matcher never
// special-cases a leading dot.
func globMatch(path, pattern string) bool {
	return globToRegex(pattern).MatchString(path)
}

func globToRegex(pattern string) *regexp.Regexp {
	var sb strings.Builder
	sb.WriteString("^")
	n := len(pattern)
	for i := 0; i < n; {
		c := pattern[i]
		switch c {
		case '*':
			j := i
			for j < n && pattern[j] == '*' {
				j++
			}
			if j-i >= 2 {
				// "**" (globstar): crosses path segments.
				if j < n && pattern[j] == '/' {
					sb.WriteString("(?:.*/)?")
					j++
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
			i = j
		case '?':
			sb.WriteString("[^/]")
			i++
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			sb.WriteString("\\")
			sb.WriteByte(c)
			i++
		default:
			sb.WriteByte(c)
			i++
		}
	}
	sb.WriteString("$")
	return regexp.MustCompile(sb.String())
}
