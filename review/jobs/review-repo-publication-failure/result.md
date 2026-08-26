Surface files are byte-identical between frozen commit and HEAD, so I can review the working tree. Reading the primary sources now.Context pack is thin as promised. Now tracing call sites, ordering, and the study run-loop consumer.Sources traced end-to-end. Now verifying baseline test health and probing the specific git behaviors my hypotheses depend on.Both hypotheses confirmed at the git level. Now let me empirically reproduce the suspected partial-progress defect (commit succeeds → index reconcile fails → retry never heals) against a verbatim copy of the publisher code.chmod didn't force the failure (rename-based rewrite). Using git's own `index.lock` exclusion instead — which is also the more realistic trigger (concurrent user git operation).**Defect #1 confirmed end-to-end with verbatim code.** After a post-commit reconcile failure (here: concurrent git process holding `index.lock`), the documented rerun heals the ref but permanently leaves a phantom staged revert of UltraPlan's own commit in the user's index — because the no-change retry path (publisher.go:241-242) never re-runs the reconcile.

Continuing the hunt: remaining threads are ssh-grandchild liveness on push timeout, RunAll summary staleness, and synthesize ordering.Investigation complete. All ordering claims verified against call sites; two defects survive counter-evidence hunting; the rest defended below.

---

# Review: repo-publication (Git stage publication)

Baseline: surface files byte-identical to frozen `5d54525`; `go build ./...` clean; `gitpublish` + `sprint` publication tests green. All hypotheses were probed against real git behavior and, for the primary defect, reproduced end-to-end against a verbatim copy of the package.

## Finding 1 — Post-commit index-reconcile failure is permanent: documented rerun heals the ref but leaves a phantom staged revert in the user's index

**Claim.** In `commitPaths` (internal/platform/gitpublish/publisher.go:200-261), the branch move (`update-ref` CAS, :253) and the user-index reconcile (`reset -q HEAD -- paths`, :256-257) are two separate steps, and the reconcile is reached only when a *new* commit is created. On any rerun where the working tree already matches HEAD — which is exactly what the previous attempt produced — `tree == parentTree` returns early (:241-242) and the reconcile never re-executes. So a failure of the reconcile step after a successful commit diverges the user's real index from HEAD for owned paths, and the documented recovery ("Rerunning the stage pushes that commit without creating a duplicate", docs/configuration.md:226-227) explicitly reports success without repairing it. No other code reconciles this state (searched `diff --cached|diff-index|reconcile` across `internal/sprint`, `internal/study`: nothing touches the git index).

**Observable bad outcome (reproduced).** With a concurrent process holding `.git/index.lock` during the window between `update-ref` and `reset`:

```
attempt1: Committed:true err=git publish: reconcile index after commit:
          fatal: Unable to create '.git/index.lock': File exists...
post-failure staged entries: "owned.txt\nstaged.txt\n"     <- owned.txt is a phantom staged REVERT of UltraPlan's own commit
attempt2: Committed:false err=<nil>                        <- rerun "succeeds"
after-retry staged entries: "owned.txt\nstaged.txt\n"      <- divergence persists
HEAD touched: "owned.txt"                                  <- commit itself is fine
```

The user staged only `staged.txt`; from then on `git status` shows an unexplained staged change reverting the published stage content. If the user commits their staged work (a normal action), that revert is committed and can be pushed upstream, silently undoing the publication. Divergence also arises from disk-full or any reset failure.

**Trigger/preconditions.** Mode ≠ off; reconcile fails after CAS succeeded (most plausibly `.git/index.lock` contention — the flock at `<git-common-dir>/ultraplan-publish.lock` covers ultraplan-vs-ultraplan only, not user/editor git processes); then one rerun of the same stage.

**Evidence.** publisher.go:241-242 (early return skips reconcile), :256-259 (reconcile inside commit-only path); recovery contract docs/configuration.md:226-227; test gap: `publisher_test.go` pins happy-path preservation (:13-45) and retry dedup (:47-80) but never exercises reset failure. Reproduction script held `.git/index.lock` open and ran the verbatim package twice.

**Execution path.** flow.go:149/169/215 → publishPlanningStage → Publish → commitPaths(update-ref ok, reset fails) → error returned → stage already durably complete → next flow invocation hits `flowStageAlreadyValid` → republish takes no-change early return → index stays diverged forever.

**Existing controls / counter-evidence searched.** flock scope (doesn't cover user git); CAS protects refs only; no validator flags owned-path index divergence; retry path deliberately skips all index writes. Nothing disproves the claim; the reproduction is deterministic.

**Severity / confidence.** Medium-low severity (silent, persistent corruption of user staging state that can propagate upstream via ordinary user action), high confidence (reproduced).

**Regression test / fix direction.** Test: hold `.git/index.lock` through first `Publish` (assert reconcile error), release, re-`Publish`, assert `git diff --cached --name-only` == `["staged.txt"]`. Fix: make the no-change path verify-and-repair — e.g., compare real-index blob vs HEAD blob for each owned path and run the targeted `reset` only when the worktree matches HEAD but the index doesn't (preserves deliberate user staging of owned paths), or persist a "reconcile pending" marker written before `update-ref` and cleared after successful `reset`.

## Finding 2 — Bounded push leaks past its bound: timeout/cancel kills only `git`, orphaning ssh/credential grandchildren

**Claim.** `gitInput` uses bare `exec.CommandContext` (publisher.go:268), which SIGKILLs only the direct `git` child; `push` over SSH spawns `ssh` (and credential helpers) as grandchildren that survive. `GIT_TERMINAL_PROMPT=0` (publisher.go:285) disables terminal prompts for git's HTTP paths but not for SSH passphrase/host-key prompts — `ssh` reads those from `/dev/tty` even with stdin/stdout piped (`CombinedOutput`). When `PushTimeout` fires (:138, :160-161), the command reports a timeout but the grandchild keeps running, potentially holding an interactive prompt on the controlling terminal; retries stack additional orphans. The codebase owns the correct pattern — `configureOwnedProcess` sets `Setpgid` for group teardown (internal/platform/process/process_unix.go:12) — but gitpublish doesn't use it, and no Pdeathsig/Setpgid exists anywhere in the publication path (grepped).

**Bad outcome / trigger.** `commit-and-push` against a remote whose SSH auth needs interaction or hangs mid-negotiation: push "times out after 2m" as designed, yet an orphaned `ssh` continues holding the session/tty; subsequent automated retries (flow reruns) accumulate processes. Liveness/resource ownership failure within the reviewed lens, though bounded in blast radius.

**Counter-evidence searched.** No wrapper around git invocation; no batch-mode enforcement (`grep BatchMode|GIT_SSH` → empty); no group-kill on this path. Timeout itself does bound the ultraplan command — the leak is strictly the descendants.

**Severity / confidence.** Low severity, high confidence on mechanism (standard `exec.Cmd` semantics; not executed live since it needs a hanging remote).

**Verification for fix.** Test with a fake `GIT_SSH_COMMAND` sleeping > PushTimeout: after `Publish` returns timeout, assert no descendant processes remain (`/proc` scan or `pgrep -g`). Fix: reuse the `Setpgid` + negative-pgid group kill pattern from `internal/platform/process`, or set `cmd.Cancel`/`WaitDelay` with group teardown.

## Defended non-issues (checked, not defects)

- **Unknown push outcomes are safe.** Cancellation/deadline mid-push may leave the remote updated while reporting failure; reruns re-push the identical SHA ("Everything up-to-date") because content-identical retries take the `tree == parentTree` path and never create a second commit. Pinned by `TestPublisherRetriesPushWithoutDuplicateCommit`.
- **Ordering doctrine holds everywhere.** Planning: publish after `runFlowStage` persistence incl. the already-complete republish path (flow.go:142-175, 213-220). Execute: after `SaveExecuteRunState`+`WriteExecuteSummary` (execute.go:314-346), resumable without re-running tasks (`allExecuteTasksResolved`). Review: after `saveReviewState` (review.go:630-637). Smoke: after `commitSmoke` + roadmap mark (smoke.go:31-59). Study loop: task results enqueued only after `applyExecutionResult` persist + `recordHistory` (run_loop.go:334-338).
- **Study deferred-consumer goroutine is race-free.** Buffered channel sized to task count can't block; `result.Publications` is touched by the main goroutine only after `<-publicationDone` (close/receive happens-before); consumer errors route through the same `recordErr` fail-fast as task errors. A publish failure does skip the final force-save/history-sync/run-state publish (run_loop.go:454), but incremental persists keep state ≤250 ms stale and the next run reconciles/resumes — consistent with the file's stated resumability contract.
- **Windows no-lock build** loses mutual exclusion but not correctness: unique temp indexes plus atomic `update-ref` CAS mean the loser always errors cleanly ("branch changed while committing"); worst case is wasted work, not corruption.
- **Roadmap path arithmetic** `Dir(Dir(sp.Path))/roadmap.md` matches the store layout `<root>/projects/<project>/sprints/<slug>` (service.go:1206; store_fs.go:59) and is identical in smoke.go:44 and publication.go:116; `MarkRoadmapSprintDelivered` failing earlier means the file exists by publish time.
- **Config hardening is complete at load**: mode enum, remote-name charset, and positive ≤30 m push timeout all validated (config.go:475-486); shell-free argv invocation plus leading-dash rejection covers injection; `--set-upstream` fallback and upstream-remote preference match the documented contract; `for-each-ref`-derived remoteref can't split argv (prefixed `HEAD:`).
- **Non-git workspace with mode ≠ off**, detached-HEAD targets, and `RunAll` publishing a stale summary after `WriteSummary` warning are all loud-failure edges that error rather than corrupt; opt-in configuration makes them acceptable.

Cleanup: probes confined to `/tmp/opencode/pubprobe`; target repo and workspace untouched.