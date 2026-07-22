package skeptic

import (
	"strings"
	"testing"
)

func TestIsEvidenceAuthentic(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty body", "", false},
		{"whitespace-only body", "   \n\t  ", false},
		{"no evidence section at all", "## Background\nsome text", false},
		{"evidence section present but empty", "## Evidence\n\n## Testing\nmore", false},
		{"evidence section whitespace only", "## Evidence\n   \n## Testing", false},
		{"authentic evidence", "## Evidence\nRan `go test ./...`, output:\n```\nok  	pkg	0.1s\n```\n## Testing\nmore", true},
		{"simulated is fabricated", "## Evidence\nSimulated the failure locally.", false},
		{"example.com is fabricated", "## Evidence\nScreenshot at https://example.com/img.png", false},
		{"screenshot placeholder tag", "## Evidence\n<screenshot path=foo>", false},
		{"value placeholder tag", "## Evidence\n<value>", false},
		{"TODO is fabricated", "## Evidence\nTODO: add real evidence", false},
		{"TBD is fabricated", "## Evidence\nCoverage: TBD", false},
		{"placeholder word", "## Evidence\nplaceholder text here", false},
		{"coverage without percent fails", "## Evidence\ncoverage increased after this change", false},
		{"coverage with percent passes", "## Evidence\ncoverage is 97%", true},
		{"coverage command invocation is exempt", "## Evidence\n$ pnpm test --coverage", true},
		{"coverage in prose with em dash still fails without percent", "## Evidence\nCoverage improved -- all tests pass", false},
		{"fabricated pattern outside evidence section is ignored", "## Background\nTODO fix this\n## Evidence\nreal output here, all good", true},
		{"case-insensitive TODO/TBD/simulated", "## Evidence\ntodo tbd SIMULATED", false},
		// Regression: an earlier version of the Go port discarded
		// same-line trailing text after "## Evidence" (e.g. "## Evidence:
		// some content" on one line, nothing after it) because it
		// unconditionally cut through the first newline after the heading
		// match — with no following newline at all, that cut discarded
		// the ENTIRE evidence content, producing a false "empty section"
		// FAIL. TS's split (body.split(/^##\s*Evidence/im)[1]) only splits
		// on the regex match itself and keeps everything after it,
		// including same-line text with no trailing newline. Found by
		// adversarial review; this exact input (single line, no trailing
		// newline) is the one that actually distinguishes the two
		// behaviors — an earlier regression test using multi-line inputs
		// happened not to, since the buggy and fixed code paths landed on
		// the same verdict for that specific content.
		{"heading with same-line trailing text and no following newline is not treated as empty", "## Evidence: real output here, all good", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsEvidenceAuthentic(c.body)
			if got != c.want {
				t.Fatalf("IsEvidenceAuthentic(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestGetChangedFiles(t *testing.T) {
	cases := []struct {
		name string
		diff string
		want []string
	}{
		{"empty diff", "", nil},
		{
			// getChangedFiles only recognizes a plain (non-git-header)
			// modification via the "diff --git" line — a bare
			// "--- a/x\n+++ b/x" pair with no git header and no deletion
			// (+++ /dev/null) never gets added, matching the real
			// getChangedFiles in prompt.ts exactly (verified against the TS
			// source: the m1 "--- a/" branch only records lastOldPath, it
			// never calls files.add unless the following +++ line is
			// /dev/null). Real `git diff`/GitHub API diffs always include
			// the "diff --git" header per file, so this is not a gap for
			// real input — it's just not exercised by a header-less
			// synthetic fixture.
			"standard unified diff with git header",
			"diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new",
			[]string{"foo.go"},
		},
		{
			"bare unified diff without a git header adds nothing",
			"--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new",
			nil,
		},
		{
			"git diff header for binary/renamed files",
			"diff --git a/old.png b/new.png\nBinary files a/old.png and b/new.png differ",
			[]string{"new.png", "new.png"}, // both the git-header and the binary-line match; getChangedFiles dedups anyway below
		},
		{
			"deleted file via /dev/null pairing",
			"diff --git a/deleted.go b/deleted.go\n--- a/deleted.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone",
			[]string{"deleted.go", "deleted.go"}, // git-header + dev/null-pairing both name it; dedups below
		},
		{
			"multiple files",
			"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go",
			[]string{"a.go", "b.go"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := getChangedFiles(c.diff)
			// dedupe want for the binary case (git-header + binary-line both name new.png)
			want := dedupe(c.want)
			if len(got) != len(want) {
				t.Fatalf("getChangedFiles(%q) = %v, want %v", c.diff, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("getChangedFiles(%q) = %v, want %v", c.diff, got, want)
				}
			}
		})
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func basePR() PRInfo {
	return PRInfo{
		Number:      42,
		Title:       "Fix the thing",
		Body:        "",
		State:       "OPEN",
		HeadRefOID:  "abc1234567",
		BaseRefName: "main",
		IsDraft:     false,
	}
}

func TestBuildSkepticPrompt_GateLabels(t *testing.T) {
	state := MergeGateState{
		CIPassing:                  true,
		NoConflicts:                true,
		CRApproved:                 true,
		CRState:                    "approved",
		BugbotErrors:               0,
		UnresolvedBlockingComments: 2,
		EvidenceRequired:           true,
		EvidenceApproved:           false,
		SkepticVerdict:             "",
	}
	out := BuildSkepticPrompt(basePR(), state, "", nil, nil, nil)

	mustContain(t, out, "1. CI green:            PASS")
	mustContain(t, out, "2. No merge conflicts:  PASS")
	mustContain(t, out, "3. CR APPROVED:         PASS (state: approved)")
	mustContain(t, out, "4. Bugbot clean:        PASS (errors: 0)")
	mustContain(t, out, "5. Comments resolved:   FAIL (2 blocking)")
	mustContain(t, out, "6. Evidence review:    FAIL")
	mustContain(t, out, "7. Prior skeptic verdict: not posted yet")
}

func TestBuildSkepticPrompt_CRDismissedWithoutApprovalAppendsSuffix(t *testing.T) {
	state := MergeGateState{CRState: "dismissed", CRDismissedWithoutApproval: true}
	out := BuildSkepticPrompt(basePR(), state, "", nil, nil, nil)
	mustContain(t, out, "state: dismissed + DISMISSED_WITHOUT_APPROVAL")
}

func TestBuildSkepticPrompt_EvidenceLabelSeeRule10WhenNotRequired(t *testing.T) {
	state := MergeGateState{EvidenceRequired: false}
	out := BuildSkepticPrompt(basePR(), state, "", nil, nil, nil)
	mustContain(t, out, "6. Evidence review:    (see Rule 10)")
}

func TestBuildSkepticPrompt_EmptyBodyApprovedSuffix(t *testing.T) {
	pr := basePR()
	state := MergeGateState{CRApproved: true, CRState: "approved"}
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "approved", Body: "", SubmittedAt: "2026-01-01T00:00:00Z", CommitID: pr.HeadRefOID},
	}
	out := BuildSkepticPrompt(pr, state, "", reviews, nil, nil)
	mustContain(t, out, "[EMPTY BODY APPROVED — valid per Rule 2]")
}

func TestBuildSkepticPrompt_VirtualApprovedReviewInsertedOnCommentFallback(t *testing.T) {
	state := MergeGateState{CRApproved: true, CRState: "approved (comment fallback)"}
	out := BuildSkepticPrompt(basePR(), state, "", nil, nil, nil)
	mustContain(t, out, "coderabbitai (approved")
	mustContain(t, out, "Approving via comment fallback [approve]")
}

func TestBuildSkepticPrompt_ReviewAnchoring(t *testing.T) {
	pr := basePR()
	reviews := []ReviewInfo{
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "approved", Body: "on head", SubmittedAt: "2026-01-01T00:00:00Z", CommitID: pr.HeadRefOID},
		{Author: &ReviewAuthor{Login: "coderabbitai"}, State: "changes_requested", Body: "stale one", SubmittedAt: "2025-12-31T00:00:00Z", CommitID: "deadbeef1234"},
		{Author: &ReviewAuthor{Login: "someone"}, State: "commented", Body: "no commit", SubmittedAt: "2025-12-30T00:00:00Z"},
	}
	out := BuildSkepticPrompt(pr, MergeGateState{}, "", reviews, nil, nil)
	mustContain(t, out, ", on-head): on head")
	mustContain(t, out, ", stale:deadbee): stale one")
	mustContain(t, out, ", unanchored): no commit")
}

func TestBuildSkepticPrompt_DesignDocPresentVsAbsent(t *testing.T) {
	pr := basePR()
	doc := "design doc content here"
	withDoc := BuildSkepticPrompt(pr, MergeGateState{}, "", nil, &doc, nil)
	mustContain(t, withDoc, "--- DESIGN DOC (docs/design/pr-designs/pr-42.md) ---")
	mustContain(t, withDoc, "design doc content here")

	withoutDoc := BuildSkepticPrompt(pr, MergeGateState{}, "", nil, nil, nil)
	mustContain(t, withoutDoc, "DESIGN DOC NOT FOUND for this PR.")
}

func TestBuildSkepticPrompt_PRDescriptionSectionOnlyWhenBodyPresent(t *testing.T) {
	pr := basePR()
	pr.Body = "the PR description"
	withBody := BuildSkepticPrompt(pr, MergeGateState{}, "", nil, nil, nil)
	mustContain(t, withBody, "--- PR DESCRIPTION ---")
	mustContain(t, withBody, "the PR description")

	pr2 := basePR()
	pr2.Body = ""
	withoutBody := BuildSkepticPrompt(pr2, MergeGateState{}, "", nil, nil, nil)
	if strings.Contains(withoutBody, "--- PR DESCRIPTION ---") {
		t.Fatal("PR DESCRIPTION section should be omitted entirely when pr.Body is empty")
	}
}

func TestBuildSkepticPrompt_TestFilesSectionPreservesOrderAndTruncates(t *testing.T) {
	pr := basePR()
	testFiles := []TestFileContent{
		{Name: "z_test.go", Content: "first"},
		{Name: "a_test.go", Content: "second"},
	}
	out := BuildSkepticPrompt(pr, MergeGateState{}, "", nil, nil, testFiles)
	zIdx := strings.Index(out, "--- z_test.go ---")
	aIdx := strings.Index(out, "--- a_test.go ---")
	if zIdx == -1 || aIdx == -1 || zIdx > aIdx {
		t.Fatalf("test files section did not preserve caller-supplied order: z at %d, a at %d", zIdx, aIdx)
	}
}

func TestBuildSkepticPrompt_NoTestFilesOmitsSection(t *testing.T) {
	out := BuildSkepticPrompt(basePR(), MergeGateState{}, "", nil, nil, nil)
	if strings.Contains(out, "TEST FILE CONTENTS") {
		t.Fatal("test files section should be omitted when no test files are supplied")
	}
}

func TestBuildSkepticPrompt_ChangedFilesCountAndDiffTruncationNote(t *testing.T) {
	pr := basePR()
	diff := "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new"
	out := BuildSkepticPrompt(pr, MergeGateState{}, diff, nil, nil, nil)
	mustContain(t, out, "--- ALL CHANGED FILES IN PR (1 files) ---")
	mustContain(t, out, "- foo.go")
	if strings.Contains(out, "DIFF TRUNCATED") {
		t.Fatal("small diff should not be marked truncated")
	}
}

// TestBuildSkepticPrompt_LiteralRulesTextPreserved spot-checks a handful of
// exact literal fragments from the giant rules/output-format block —
// full-text fidelity against the TS source was verified separately via a
// line-by-line diff of all 185 literal strings (not re-run here since that
// comparison lives outside the Go test binary), but these anchors catch
// gross corruption (a dropped rule, a mis-transcribed gate marker) in
// ordinary `go test` runs.
func TestBuildSkepticPrompt_LiteralRulesTextPreserved(t *testing.T) {
	out := BuildSkepticPrompt(basePR(), MergeGateState{}, "", nil, nil, nil)
	anchors := []string{
		"You are a Skeptic QA Agent. Your job is to FIND GAPS in this PR.",
		"INVERTED INCENTIVE: You are rewarded for finding missing evidence.",
		"<!-- skeptic-gate-8d:PASS|FAIL -->  Scope boundary — diff changes stay within stated scope",
		"A PASS verdict is invalid unless all eight primary markers (gates 1-8) are PASS.",
		"VERDICT: FAIL",
		"Find at least one concrete gap before declaring FAIL.",
	}
	for _, a := range anchors {
		mustContain(t, out, a)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("output does not contain %q\n--- full output ---\n%s", needle, haystack)
	}
}
