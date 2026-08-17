# jleechan-coder-silent-false-parks-h92r — cross-repo silence-watcher fix

## Live incident (2026-07-17)

All 6 af-drive lanes (df-149..154) parked `PARKED_HUMAN_HELD` reason=
`coder_silent` while their coders were ACTIVELY WORKING. Transcripts
237-1848 lines each, real deliverables (PR #288/#289 conflict fixes
pushed + green, #281 E6-blocker fix, #222 daemon-tests diagnosis+fix,
#285 correct wait-on-dependency).

The silence heuristic (in `jleechanorg/dark-factory`'s daemon
`tick.rs` Dispatched autonomy check) only polled the REMOTE branch tip
via `gh api .../branches/<branch>`. Long-running Claude coder sessions
buffer locally between `git push` cycles (5-15 min cadence, sometimes
much longer for non-trivial diffs) — the remote can appear stale for

> 30 min while the coder's own Claude transcript directory under
> `~/.claude/projects/<slug>/` is actively growing.

This is the same class of bug as the 2026-07-11 finding (daemon's
passive silence-watcher tracks its own overlay, disagreeing with real
session state). Related: jleechan-52gs (AO spawning-status desync),
jleechan-2s0h precedent.

## Where the actual fix lives

The silence-watcher source code is in
[jleechanorg/dark-factory](https://github.com/jleechanorg/dark-factory),
merged into `main` as commit **`ce0e72a7650d5c43e6021602b291f162ec8cec81`**
(merge commit on the default branch — the original feature branch
`fix/jleechan-coder-silent-false-parks-h92r` was deleted post-merge, so
the canonical pointer is the merge SHA, NOT the branch name):

- `daemon/src/tick.rs` — Dispatched autonomy check now consults TWO
  liveness signals (remote branch tip mtime AND coder's own Claude
  transcript MTIME). A coder is treated as LIVE if EITHER signal is
  fresh; only a coder with NEITHER signal having moved in 30 minutes
  is parked (fail-closed preserved).
- `daemon/src/adapters.rs` — implements
  `worktree_transcript_last_activity_epoch` against the coder's own
  `~/.claude/projects/<slug>/` transcript dir, returning the maximum
  mtime observed across the slug directory's `*.jsonl` files.
- `daemon/src/tools.rs` — new `Sessions::worktree_transcript_last_activity_epoch`
  trait method with default `Ok(None)` impl ("cannot verify") so
  existing impls and fakes keep their original behavior.
- `daemon/tests/common/mod.rs` — `FakeSessions` gains a `transcript_activity`
  scripting map. `claude_project_slug(worktree_path)` derives the
  per-session slug from the worktree path.
- `daemon/tests/tick_integration.rs` — two regression tests:
  - `test_wedge_detection_dispatched_coder_silent_saved_by_transcript_activity`
    (RED→GREEN — 2026-07-17 incident replay: remote is stale but
    transcript mtime is fresh → bead must NOT park)
  - `test_wedge_detection_dispatched_coder_silent_stale_transcript_still_parks`
    (symmetry guard / fail-closed: BOTH signals stale → bead parks as
    before, defeating any naive "any transcript record ever" impl)

## Why this agent-orchestrator probe exists

The probe originally lived in `jleechanorg/worldarchitect.ai`
(PR #8421) alongside the product/game code. Issue
[jleechanorg/worldarchitect.ai#8623](https://github.com/jleechanorg/worldarchitect.ai/issues/8623)
flagged three problems with that placement: it coupled infrastructure
liveness verification to the product repo (portability cost when
onboarding another auto-factory repo), and it polluted the project's
GHA workflow with runner-host-specific process checks. The resolution:

1. **This repository (`jleechanorg/agent-orchestrator`) owns the probe.**
   PR #24 moves `scripts/coder_silent_false_park_probe.sh` and its
   five-case fail-closed test harness into the orchestration layer
   adjacent to dark-factory, so any agent-orchestrator operator — or
   any auto-factory verifier — can confirm the silence-watcher fix is
   live without touching the worldarchitect.ai product repo. The probe
   verifies (a) the dark-factory checkout contains merge commit
   `ce0e72a7` as an ancestor of HEAD, (b) BOTH new regression test
   names appear in the cargo output, AND (c) both tests report `ok`.
   Bare substring matching on `coder_silent` is NOT enough — the
   pre-existing `test_wedge_detection_dispatched_coder_silent` test
   matches the same substring and could mask a stale checkout missing
   the new tests (this is the regression the codex blocker on PR #8421
   specifically demanded).

2. **The worldarchitect.ai side has been removed.** PR
   [jleechanorg/worldarchitect.ai#8625](https://github.com/jleechanorg/worldarchitect.ai/pull/8625)
   cleans up `jleechanorg/worldarchitect.ai` and adds a regression
   test (`tests/test_ops_probe_removal.py`) that fails CI if the probe
   script, its shell test, or the `coder_silent_false_park_probe`
   string in `.github/workflows/test.yml` reappear in that repo.

3. **A polluted dark-factory attempt is superseded.** PR
   [jleechanorg/dark-factory#479](https://github.com/jleechanorg/dark-factory/pull/479)
   attempted the same relocation but is polluted (21 files /
   +512/-1622, 10 unrelated commits including a 1622-line deletion
   to `skeptic-gate.py`). This PR (#24) is the canonical home;
   `dark-factory#479` should NOT be merged.

The actual algorithmic fix lands in dark-factory `main` as commit
`ce0e72a7` (the daemon's source-of-truth repo). The factory verifier
confirms the fix by running this probe against any dark-factory
worktree that contains the merge commit.

## Operator runbook

To verify the silence-watcher fix is live against any dark-factory
worktree that contains the merge commit:

```bash
./scripts/coder_silent_false_park_probe.sh
```

Exit code 0 = both new regression tests found and PASS (transcript-mtime
fix is live in the dark-factory checkout).
Exit code 1 = one or more required tests missing/failing (fix missing/broken).
Exit code 2 = prerequisite missing (rust toolchain missing, no
dark-factory checkout, or the checkout's HEAD does not have `ce0e72a7`
as an ancestor).
