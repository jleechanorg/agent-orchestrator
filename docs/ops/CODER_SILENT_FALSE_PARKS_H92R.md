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

## Why this worldarchitect.ai PR exists

The auto-factory factory dispatcher assigns coder lanes per-repo and
watches a specific branch on jleechanorg/worldarchitect.ai
(`factory/jleechan-coder-silent-false-parks-h92r-r1`). The PR is
required for that factory lane to mark the bead closed. The actual
algorithmic fix lands in dark-factory `main` as commit `ce0e72a7`
(the daemon's source-of-truth repo); the worldarchitect.ai side
carries:

1. `scripts/coder_silent_false_park_probe.sh` — end-to-end regression
   probe operators run to confirm the fix is live WITHOUT re-reading
   the dark-factory diff. The probe verifies (a) the dark-factory
   checkout contains merge commit `ce0e72a7` as an ancestor of HEAD,
   (b) BOTH new regression test names appear in the cargo output, AND
   (c) both tests report `ok`. Bare substring matching on `coder_silent`
   is NOT enough — the pre-existing
   `test_wedge_detection_dispatched_coder_silent` test matches the same
   substring and could mask a stale checkout missing the new tests
   (this is the regression the codex blocker on PR #8421 specifically
   demanded).

2. This documentation file — cross-repo evidence pointer so the
   factory verifier can confirm the algorithmic fix landed in
   dark-factory and is reachable from the worldarchitect.ai side.

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
