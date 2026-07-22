package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func TestNewSkepticCommand_Registration(t *testing.T) {
	root := newSkepticCommand()
	if root.Use != "skeptic" {
		t.Fatalf("Use = %q, want skeptic", root.Use)
	}
	verify, _, err := root.Find([]string{"verify"})
	if err != nil {
		t.Fatalf("expected a verify subcommand: %v", err)
	}
	wantFlags := []string{"pr", "repo", "dry-run", "model", "trigger-sha", "prompt", "exclude-paths", "request-id"}
	for _, name := range wantFlags {
		if verify.Flags().Lookup(name) == nil {
			t.Errorf("verify command missing --%s flag", name)
		}
	}
	if f := verify.Flags().Lookup("pr"); f == nil || f.Shorthand != "n" {
		t.Errorf("expected --pr to have shorthand -n")
	}
	if f := verify.Flags().Lookup("repo"); f == nil || f.Shorthand != "r" {
		t.Errorf("expected --repo to have shorthand -r")
	}
}

func TestRunSkepticVerify_RejectsNonPositivePR(t *testing.T) {
	for _, pr := range []int{0, -1} {
		cmd := &cobra.Command{Use: "verify"}
		cmd.SetContext(context.Background())
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		err := runSkepticVerify(cmd, skepticVerifyOptions{pr: pr})
		if err == nil {
			t.Fatalf("expected error for pr=%d, got nil", pr)
		}
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("expected pr=%d validation error to be a usageError, got %T: %v", pr, err, err)
		}
	}
}

func TestSkepticBotAuthor_DefaultAndEnvOverride(t *testing.T) {
	orig, hadOrig := os.LookupEnv("GH_SKEPTIC_BOT_AUTHOR")
	t.Cleanup(func() {
		if hadOrig {
			os.Setenv("GH_SKEPTIC_BOT_AUTHOR", orig)
		} else {
			os.Unsetenv("GH_SKEPTIC_BOT_AUTHOR")
		}
	})

	os.Unsetenv("GH_SKEPTIC_BOT_AUTHOR")
	if got := skepticBotAuthor(); got != "github-actions[bot]" {
		t.Errorf("default skepticBotAuthor() = %q, want github-actions[bot]", got)
	}

	os.Setenv("GH_SKEPTIC_BOT_AUTHOR", "custom-bot[bot]")
	if got := skepticBotAuthor(); got != "custom-bot[bot]" {
		t.Errorf("overridden skepticBotAuthor() = %q, want custom-bot[bot]", got)
	}
}
