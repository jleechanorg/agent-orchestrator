package skeptic

import (
	"strings"
	"testing"
)

func TestCheckRunLevel(t *testing.T) {
	cases := []struct {
		name       string
		llmOutput  string
		wantResult EvidenceResult
	}{
		{"empty", "", EvidenceAbsent},
		{"whitespace only", "   \n  ", EvidenceAbsent},
		{"no verdict line", "some random output with no verdict", EvidenceAbsent},
		{"pass", "analysis...\nVERDICT: PASS", EvidencePass},
		{"fail", "analysis...\nVERDICT: FAIL — bad", EvidenceFail},
		{"skipped is infra fail", "VERDICT: SKIPPED — infra down", EvidenceFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckRunLevel(c.llmOutput)
			if got.Result != c.wantResult {
				t.Errorf("CheckRunLevel(%q).Result = %v, want %v (detail: %s)", c.llmOutput, got.Result, c.wantResult, got.Detail)
			}
			if got.Label != "run-level" {
				t.Errorf("Label = %q, want run-level", got.Label)
			}
		})
	}
}

func TestCheckCommentLevel(t *testing.T) {
	cases := []struct {
		name        string
		commentBody string
		wantResult  EvidenceResult
	}{
		{"empty", "", EvidenceAbsent},
		{"no marker", "VERDICT: PASS but no marker comment", EvidenceAbsent},
		{"marker but no verdict", "<!-- skeptic-agent-verdict -->\nno verdict line here", EvidenceAbsent},
		{"pass", "<!-- skeptic-agent-verdict -->\nVERDICT: PASS", EvidencePass},
		{"fail", "<!-- skeptic-agent-verdict -->\nVERDICT: FAIL — nope", EvidenceFail},
		{"skipped is infra fail", "<!-- skeptic-agent-verdict -->\nVERDICT: SKIPPED", EvidenceFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckCommentLevel(c.commentBody)
			if got.Result != c.wantResult {
				t.Errorf("CheckCommentLevel(%q).Result = %v, want %v (detail: %s)", c.commentBody, got.Result, c.wantResult, got.Detail)
			}
		})
	}
}

func TestVerifySkepticClaim_DecisionMatrix(t *testing.T) {
	passComment := "<!-- skeptic-agent-verdict -->\nVERDICT: PASS"
	failComment := "<!-- skeptic-agent-verdict -->\nVERDICT: FAIL — x"

	cases := []struct {
		name        string
		llmOutput   string
		commentBody string
		wantOutcome string
		wantBlocks  bool
	}{
		{"both pass -> PASS", "VERDICT: PASS", passComment, "PASS", false},
		{"run fail -> FAIL", "VERDICT: FAIL — x", passComment, "FAIL", true},
		{"comment fail -> FAIL", "VERDICT: PASS", failComment, "FAIL", true},
		{"both fail -> FAIL", "VERDICT: FAIL", failComment, "FAIL", true},
		{"run pass, comment absent -> INSUFFICIENT", "VERDICT: PASS", "", "INSUFFICIENT", true},
		{"run absent, comment pass -> INSUFFICIENT", "", passComment, "INSUFFICIENT", true},
		{"both absent -> INSUFFICIENT", "", "", "INSUFFICIENT", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := VerifySkepticClaim(c.llmOutput, c.commentBody)
			if got.Outcome != c.wantOutcome {
				t.Errorf("Outcome = %q, want %q", got.Outcome, c.wantOutcome)
			}
			if got.BlocksWorking != c.wantBlocks {
				t.Errorf("BlocksWorking = %v, want %v", got.BlocksWorking, c.wantBlocks)
			}
		})
	}
}

// Mutation check: a decision matrix that treated "fail beats absent"
// backwards (checked absent before fail) would misclassify the
// run-fail/comment-absent combination as INSUFFICIENT instead of FAIL. Per
// TS's verifySkepticClaim, ANY "fail" layer wins over an "absent" layer.
func TestVerifySkepticClaim_FailBeatsAbsent(t *testing.T) {
	got := VerifySkepticClaim("VERDICT: FAIL — x", "")
	if got.Outcome != "FAIL" {
		t.Fatalf("Outcome = %q, want FAIL (fail-layer must win over an absent comment layer)", got.Outcome)
	}
	if !got.BlocksWorking {
		t.Fatal("BlocksWorking must be true for FAIL")
	}
}

func TestFormatClaimVerification_ContainsKeyFields(t *testing.T) {
	result := VerifySkepticClaim("VERDICT: PASS", "<!-- skeptic-agent-verdict -->\nVERDICT: PASS")
	out := FormatClaimVerification(result)
	if !containsAll(out, "Claim Verification", "Run-level:", "Comment-level:", "Outcome:", "PASS", "claim verified") {
		t.Fatalf("formatted output missing expected content:\n%s", out)
	}

	failResult := VerifySkepticClaim("VERDICT: FAIL", "")
	failOut := FormatClaimVerification(failResult)
	if !containsAll(failOut, "blocks 'working' status report") {
		t.Fatalf("formatted FAIL/INSUFFICIENT output missing block warning:\n%s", failOut)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
