package skeptic

import "testing"

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{"README.md", "**/*.md", true},
		{"docs/README.md", "**/*.md", true},
		{"docs/nested/README.md", "**/*.md", true},
		{"docs/README.txt", "**/*.md", false},
		{"docs/a.md", "docs/**", true},
		{"docs/nested/a.md", "docs/**", true},
		{"src/a.md", "docs/**", false},
		{"a.go", "*.go", true},
		{"pkg/a.go", "*.go", false},
		{"a.go", "?.go", true},
		{"ab.go", "?.go", false},
		{".hidden.md", "**/*.md", true}, // {dot:true} equivalent: dotfiles match too
		{"a.go", "a.go", true},
		{"a.go", "b.go", false},
		{"src/main.go", "src/main.go", true},
	}
	for _, c := range cases {
		if got := globMatch(c.path, c.pattern); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.path, c.pattern, got, c.want)
		}
	}
}

// Mutation check: a matcher that just checked strings.Contains(path, ext)
// would wrongly match "notes.md.bak" against "*.md" — verifies the pattern
// is anchored, not a substring search.
func TestGlobMatch_AnchoredNotSubstring(t *testing.T) {
	if globMatch("notes.md.bak", "*.md") {
		t.Fatal("globMatch(\"notes.md.bak\", \"*.md\") should be false — pattern must be anchored")
	}
}
