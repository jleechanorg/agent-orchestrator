package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/skeptic"
	"github.com/aoagents/agent-orchestrator/backend/internal/skeptic/llmeval"
)

// skepticBotAuthor mirrors SKEPTIC_BOT_AUTHOR in skeptic.ts: the GH login
// findExistingVerdict/fetchMergeGateState treat as "the skeptic bot",
// overridable via GH_SKEPTIC_BOT_AUTHOR for deployments that post under a
// different identity.
func skepticBotAuthor() string {
	if v := os.Getenv("GH_SKEPTIC_BOT_AUTHOR"); v != "" {
		return v
	}
	return "github-actions[bot]"
}

type skepticVerifyOptions struct {
	pr           int
	repo         string
	dryRun       bool
	model        string
	triggerSHA   string
	prompt       string
	excludePaths string
	requestID    string
}

// newSkepticCommand builds `ao skeptic verify`. Unlike most CLI commands
// registered in root.go, this one talks directly to `gh` and the headless
// LLM CLIs rather than the local daemon over loopback HTTP — there is no
// daemon session/workspace state involved, so it does not take a
// *commandContext. Mirrors registerSkeptic in skeptic.ts.
func newSkepticCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skeptic",
		Short: "Skeptic agent commands — run independent PR verification",
	}
	cmd.AddCommand(newSkepticVerifyCommand())
	return cmd
}

func newSkepticVerifyCommand() *cobra.Command {
	var opts skepticVerifyOptions
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Run skeptic agent verification on a PR and post a VERDICT comment",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSkepticVerify(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.IntVarP(&opts.pr, "pr", "n", 0, "PR number")
	_ = cmd.MarkFlagRequired("pr")
	f.StringVarP(&opts.repo, "repo", "r", "", "Repository owner/repo (defaults to current repo)")
	f.BoolVar(&opts.dryRun, "dry-run", false, "Run the evaluation and print the verdict to stdout (skip posting to GitHub)")
	f.StringVarP(&opts.model, "model", "m", "", "Model to use for evaluation (codex, claude, gemini, minimax, agy)")
	f.StringVar(&opts.triggerSHA, "trigger-sha", "", "PR head SHA at dispatch time, embedded in the VERDICT comment for SHA matching")
	f.StringVar(&opts.prompt, "prompt", "", "Custom evaluation prompt, prepended to the default skeptic context")
	f.StringVar(&opts.excludePaths, "exclude-paths", "", `Pipe-separated glob patterns; if ALL changed files match, post VERDICT: SKIPPED without running evaluation. E.g. "**/*.md|docs/**"`)
	f.StringVar(&opts.requestID, "request-id", "", "Request ID from the triggering comment, included in the VERDICT comment body")
	return cmd
}

func runSkepticVerify(cmd *cobra.Command, opts skepticVerifyOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	if opts.pr <= 0 {
		return usageError{fmt.Errorf("invalid PR number: %d", opts.pr)}
	}

	triggerSHA := skeptic.NormalizeTriggerSHA(opts.triggerSHA)
	botAuthor := skepticBotAuthor()

	owner, repo, err := skeptic.ResolveRepo(ctx, opts.repo)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(errOut, "Fetching PR #%d state…\n", opts.pr); err != nil {
		return err
	}

	pr, err := skeptic.FetchPRMeta(ctx, owner, repo, opts.pr)
	if err != nil {
		return fmt.Errorf("failed to fetch PR: %w", err)
	}
	diff := skeptic.FetchDiff(ctx, owner, repo, opts.pr)
	reviews, err := skeptic.FetchReviews(ctx, owner, repo, opts.pr)
	if err != nil {
		reviews = nil
	}
	var existing *skeptic.ExistingVerdict
	if !opts.dryRun {
		existing, _ = skeptic.FindExistingVerdict(ctx, owner, repo, opts.pr, triggerSHA, opts.requestID)
	}

	designDoc, err := skeptic.FetchDesignDoc(ctx, owner, repo, opts.pr, pr.HeadRefOID)
	if err != nil {
		designDoc = nil
	}

	if _, err := fmt.Fprintf(errOut, "Fetched PR #%d: %q\n", opts.pr, pr.Title); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(errOut, "Fetching merge gate state (aligned with checkMergeGate)…"); err != nil {
		return err
	}

	testFiles, err := skeptic.FetchTestFileContents(ctx, owner, repo, opts.pr, diff, pr.HeadRefOID)
	if err != nil {
		testFiles = nil
	}
	state, err := skeptic.FetchMergeGateState(ctx, owner, repo, opts.pr, botAuthor)
	if err != nil {
		return fmt.Errorf("failed to fetch merge gate state: %w", err)
	}
	if _, err := fmt.Fprintln(errOut, "Merge gate state fetched"); err != nil {
		return err
	}

	// Early SKIP: if all diff files match exclude patterns, post
	// VERDICT: SKIPPED immediately without running the LLM evaluation.
	var excludePatterns []string
	if opts.excludePaths != "" {
		for _, p := range strings.Split(opts.excludePaths, "|") {
			p = strings.TrimSpace(p)
			if p != "" {
				excludePatterns = append(excludePatterns, p)
			}
		}
	}
	if len(excludePatterns) > 0 && skeptic.AllFilesExcluded(diff, excludePatterns) {
		skipVerdict := "VERDICT: SKIPPED — all changed files match exclude-paths (docs-only PR)"
		if _, err := fmt.Fprint(out, "\n=== SKIP — excluded files ===\n"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, skipVerdict); err != nil {
			return err
		}
		if opts.dryRun {
			return nil
		}
		commentID := 0
		if existing != nil {
			commentID = existing.CommentID
		}
		if _, err := fmt.Fprintf(errOut, "Posting skip verdict to PR #%d…\n", opts.pr); err != nil {
			return err
		}
		_, postErr := skeptic.PostVerdict(ctx, owner, repo, opts.pr, skipVerdict, commentID, botAuthor, triggerSHA, skipVerdict,
			&skeptic.VerdictBinding{HeadSHA: triggerSHA, RequestID: opts.requestID})
		if postErr != nil {
			return fmt.Errorf("failed to post verdict: %w", postErr)
		}
		if _, err := fmt.Fprintln(errOut, "Done! Skeptic verdict posted."); err != nil {
			return err
		}
		return nil
	}

	// Build and run evaluation.
	if _, err := fmt.Fprintln(errOut, "Running skeptic evaluation…"); err != nil {
		return err
	}
	prompt := skeptic.BuildSkepticPrompt(pr, state, diff, reviews, designDoc, testFiles)
	if opts.prompt != "" {
		prompt = "CUSTOM EVALUATION INSTRUCTIONS:\n" + opts.prompt + "\n\n---\n\n" + prompt
	}
	verdict, err := skeptic.RunEvaluation(ctx, llmeval.DefaultRunners(), prompt, opts.model)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(errOut, "Skeptic evaluation complete"); err != nil {
		return err
	}

	// Rule 10: deterministic evidence-authenticity override — the LLM might
	// ignore Rule 10, so this backstops any PASS with inauthentic evidence.
	finalVerdict := skeptic.ApplyEvidenceOverride(verdict, pr.Body)
	if finalVerdict != verdict {
		if _, err := fmt.Fprintln(errOut, "Override: LLM returned PASS but evidence authenticity check failed — forcing FAIL (Rule 10)"); err != nil {
			return err
		}
	}

	bound := skeptic.BindVerdictOutput(finalVerdict)

	if opts.dryRun {
		if _, err := fmt.Fprint(out, "\n=== DRY RUN — Verdict ===\n"); err != nil {
			return err
		}
		if bound.VerdictType == "" {
			if _, err := fmt.Fprintln(errOut, "Could not parse VERDICT from LLM output."); err != nil {
				return err
			}
			if _, err := fmt.Fprint(out, "\n=== Full LLM output ===\n"); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(out, bound.LLMOutput); err != nil {
				return err
			}
			return fmt.Errorf("could not parse VERDICT from LLM output")
		}
		if _, err := fmt.Fprintln(out, bound.VerdictLine); err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, "\n=== Full LLM output ===\n"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, bound.LLMOutput); err != nil {
			return err
		}
		if bound.VerdictType == "FAIL" || bound.VerdictType == "SKIPPED" {
			return fmt.Errorf("verdict: %s", bound.VerdictType)
		}
		return nil
	}

	if bound.VerdictType == "" {
		if _, err := fmt.Fprintln(errOut, "Could not parse VERDICT from LLM output. Posting raw output."); err != nil {
			return err
		}
	}

	commentID := 0
	if existing != nil {
		commentID = existing.CommentID
	}
	if _, err := fmt.Fprintf(errOut, "Posting verdict to PR #%d…\n", opts.pr); err != nil {
		return err
	}
	commentBody, err := skeptic.PostVerdict(ctx, owner, repo, opts.pr, bound.VerdictLine, commentID, botAuthor, triggerSHA, bound.LLMOutput,
		&skeptic.VerdictBinding{HeadSHA: triggerSHA, RequestID: opts.requestID})
	if err != nil {
		return fmt.Errorf("failed to post verdict: %w", err)
	}
	if _, err := fmt.Fprintln(errOut, "Done! Skeptic verdict posted."); err != nil {
		return err
	}

	// Verify both run-level (LLM output) and comment-level (GitHub comment)
	// evidence — surfaces INSUFFICIENT when evidence is missing or
	// inconsistent, fail-closed.
	claimResult := skeptic.VerifySkepticClaim(finalVerdict, commentBody)
	if _, err := fmt.Fprintln(out, skeptic.FormatClaimVerification(claimResult)); err != nil {
		return err
	}
	if claimResult.BlocksWorking {
		if _, err := fmt.Fprintf(errOut, "⚠  Claim verification: %s — agent 'working' status is NOT permitted until resolved.\n", claimResult.Outcome); err != nil {
			return err
		}
	}

	// Note: unlike the dry-run branch above, a FAIL/SKIPPED verdict here is
	// NOT a CLI error — the evaluation ran successfully and its verdict was
	// posted; that is a successful `ao skeptic verify` invocation. Mirrors
	// skeptic.ts's post branch, which has no process.exit(1) for a
	// non-PASS verdict (only the dry-run branch, where there is no posted
	// comment to inspect afterward, exits non-zero on non-PASS).
	return nil
}
