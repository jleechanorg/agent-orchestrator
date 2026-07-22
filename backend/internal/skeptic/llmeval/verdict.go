package llmeval

import (
	"regexp"
	"strings"
)

// LineRegex builds a case-insensitive, multiline regex matching a VERDICT
// line for the given set of accepted verdict words (e.g. "PASS", "FAIL"),
// tolerating the same wrapping variants as TS's buildVerdictLineRe in
// verdict-utils.ts:
//   - plain:         VERDICT: PASS
//   - blockquote:    > **VERDICT: SKIPPED**
//   - markdown-bold: **VERDICT: FAIL**
//   - markdown-hdr:  ## VERDICT: FAIL
func LineRegex(verdicts []string) *regexp.Regexp {
	escaped := make([]string, len(verdicts))
	for i, v := range verdicts {
		escaped[i] = regexp.QuoteMeta(v)
	}
	pattern := `(?im)^[ \t]*(?:> ?)?(?:#{1,6}[ \t]*)?(?:\*{1,2})?VERDICT:[ \t]*(` +
		strings.Join(escaped, "|") +
		`)(?:\*{1,2})?[ \t]*(?:[-—:].*)?$`
	return regexp.MustCompile(pattern)
}

// StrictVerdictRegex matches only PASS|FAIL — used to validate raw headless
// tool output before accepting it as a valid chain result. SKIPPED is
// deliberately excluded here: it was the old infra-unavailable sentinel and
// has been replaced by fail-closed VERDICT: FAIL for the internal eval
// chain. Mirrors STRICT_VERDICT_RE in llm-eval-shared.ts.
var StrictVerdictRegex = LineRegex([]string{"PASS", "FAIL"})
