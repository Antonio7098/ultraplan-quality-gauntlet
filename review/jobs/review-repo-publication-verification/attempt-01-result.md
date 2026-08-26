Context pack read. Now tracing callers, tests, and config wiring myself.Final verification pass on two claims before writing up.# Review: `repo-publication` — Git stage publication

Independence note: I read the context pack first, then re-derived everything from source. The pack's "unknowns" (diverged-upstream retry, hook interactions, Windows no-lock build, `Publications` rendering) were resolved below; two of them turn out to hide real defects.

---

## Finding 1 — Push target derived from a branch name that is stale by push time (TOCTOU across the publish lock)

**Claim:** `Publish` resolves the branch once before acquiring the publish lock (publisher.go:87-94), but `commitPaths` deliberately re-resolves the commit target under the lock via `symbolic-ref -q HEAD` (publisher.go:249-253). The push step then builds its refspec and upstream lookup from the *pre-lock* value: `refs/heads/<branch>` for upstream detection (publisher.go:140), `HEAD:<upstreamRef>` (publisher.go:154), and `HEAD:refs/heads/<branch>` with `--set-upstream` in the fallback path (publisher.go:157). Nothing re-validates that `<branch>` is still current.

**Bad outcome:** If the user switches branches during the window between line 87 and the push — a window that includes the *unbounded* lock-wait loop in lock_unix.go:22-39 (another ultraplan process holding `ultraplan-publish.lock`, or simply any slow intermediate step) — ultraplan commits correctly to the new current branch B2 (CAS vs. freshly-read parent), then pushes B2's HEAD to `refs/heads/<B1>` on the remote, and in the fallback path re-binds B1's upstream (`--set-upstream`). That publishes commits belonging to one branch under another branch's name on an externally visible, effectively irreversible remote. Non-fast-forward rejection limits but does not prevent this (fast-forward case, or B1 not yet existing remotely → branch created).

**Trigger/preconditions:** mode ≠ `off`; branch switched (or detached-and-reattached) between line 87 and push. Plausible because stage publication happens after minutes-long agent stages and behind contention.

**Counter-evidence searched:** CAS protects only the commit, not the refspec; no re-resolution of `branch` after lock acquisition exists anywhere in publisher.go; no test covers branch switching. Docs describe "pushes the current branch" (configuration.md:222), which this can violate.

**Severity:** Medium-low. **Confidence:** high on mechanism, medium on real-world frequency.

**Regression test:** Externally acquire `<git-common-dir>/ultraplan-publish.lock` (flock), start `Publish` in a goroutine against a repo on branch A with a working remote, switch the repo to branch B while it blocks, release the lock, assert either failure or that the pushed refspec names B. Today it pushes `HEAD:refs/heads/A`.

**Fix:** Re-resolve `branch --show-current` after `acquireLock` and use that value for upstream lookup and refspec construction.

---

## Finding 2 — Publication errors embed raw git output and bypass the codebase's own git-output redaction standard

**Claim:** `gitInput` folds the entire `CombinedOutput` into the error verbatim (publisher.go:273-279: `fmt.Errorf("git %s: %s", args, detail)`). The rest of the codebase treats git stderr as credential-bearing: `study init` clones run output through `redactGitOutput`, which regex-strips `scheme://user:pass@host` userinfo and bounds length (init_clone.go:44-65, tested at init_test.go:143-156). `gitpublish` applies no equivalent, and terminal errors are printed raw: `app.fail()` does `fmt.Fprintln(stderr, err.Error())` with no redaction (app.go:229-235). Progress-channel emission is also unsafe: flow.go:152/172/218 put `publishErr.Error()` into `FlowProgress.Message`, and render-side `RedactValue` is keyword-based (redaction.go:28-41 checks for "token"/"password"/"ghp_" etc.) so a remote URL like `https://alice:hunter2@host/org/repo.git` contains none of the markers and passes through.

**Bad outcome:** With `git.stage_completion=commit-and-push`, any failed push whose diagnostics echo the configured URL (classic PAT-in-URL remotes; git prints `fatal: unable to access 'https://user:token@host/...'` in auth/network failures) leaks the embedded credential to stderr, CI logs, TUI operation views, and web operation results. Keyword redaction does not catch it; the project's dedicated regex exists precisely because keywords don't.

**Trigger/preconditions:** failed push + credential-bearing remote URL configuration.

**Existing controls / counter-evidence:** progress messages pass through `RedactValue` at render time (sprint_commands.go:875), which helps only when telltale words appear; logging and health paths redact, but the primary command-error path (`fail`) does not.

**Severity:** Low-medium (secret exposure). **Confidence:** high on inconsistency and leak path; leak requires credential-in-URL configuration.

**Fix/regression test:** Route `gitInput`'s error detail through the same `credentialURLPattern` redaction plus a length bound; test with a fake credential-helper-free remote URL containing userinfo and assert `[redacted]@` appears in the wrapped error.

---

## Finding 3 — Smoke harness publication assumes the harness root is inside a git repo, discovered only after the run fully succeeded

**Claim:** `publishSmokeStage` publishes author-changed harness files with `Root: prepared.HarnessRoot` (publication.go:100-109). `CommandPublisher.Publish` hard-fails if that root is not inside a work tree (publisher.go:79-82: "resolve repository from …"). Nothing up front establishes that requirement: `prepareSmokeStatic` validates manifest, containment of cwd/runs/issues roots, and the review gate (smoke_protocol.go:165-210) but never checks repo-ness; docs list smoke prerequisites without mentioning git (planning-smoke.md:10). Harnesses are registered by absolute path in `project-index.md` and need not live in any repository.

**Bad outcome:** With stage completion enabled and a non-empty `AuthorChangedPaths` (the normal case whenever the author agent touches suite files — set from the actual diff at smoke_author.go:83), every `RunSmoke` completes discovery, authoring, execution, verdict synthesis, artifact persistence, **and roadmap delivery marking** (smoke.go:39-48 runs before publication at :49-59), and *then* exits non-zero via `errors.Join(err, publishErr)` (smoke.go:57). The recorded state says delivered/pass while the command reports failure — a permanent retry loop until the user relocates or initializes a repo, and mixed state visible to automation that gates on exit codes.

**Observable path:** smoke.go:34 (success) → :45 mark roadmap → :54 `publishSmokeStage` → publisher.go:81 error → smoke.go:57 non-zero exit.

**Counter-evidence searched:** fail-closed-on-publication-error is the documented doctrine for *push* failures ("leaves the local commit in place", configuration.md:226); it does not cover "the target was never a repository," which is a precondition failure detectable pre-run in this codebase's own pre-flight style. No doc imposes the requirement, so users enabling the opt-in flag get a surprise permanent failure.

**Severity:** Medium (feature-bricking operability, misleading terminal state). **Confidence:** high on mechanism; medium-high that non-repo harnesses are common (tests themselves use `t.TempDir()` harnesses, only surviving because the publisher is nil there — no test ever exercises `publishSmokeStage` at all).

**Fix:** Validate repo-ness of `HarnessRoot` in `prepareSmokeStatic` (fail before running), or treat a non-repo root as a skip-with-diagnostic in `visiblePublication`, and document the constraint.

---

## Finding 4 — Verification gaps: the riskiest push path and most adapter publishers have zero coverage; one test name misleads

**Claims (bundled, all verified by exhaustive grep):**
- The only real-git tests are the two in publisher_test.go. Both exercise the *fallback* push (`--set-upstream <policy.Remote>`); the **upstream-configured branch is never tested** — including `%(upstream:remotename/remoteref)` parsing and the `HEAD:<upstreamRef>` push (publisher.go:140-154). This untested branch contains the only error-truth wart on the surface: when `refErr == nil && upstreamRef == ""`, the message renders `%!w(<nil>)` (publisher.go:149-152).
- Of five adapter publishers, tests pin only `publishPlanningStage` and `publishExecuteStage` request shapes (sprint/publication_test.go) and `publishExecution` (study/publication_test.go). `publishReviewStage`, `publishSmokeStage`, `publishRunLoopState`, `publishRunAllSummary` have no direct tests. In particular, the conditional roadmap.md inclusion in the smoke commit (publication.go:115-117) is unpinned — a regression dropping roadmap.md would pass CI while delivery marks silently stop being published.
- Locking is completely untested: `acquireLock`/`release` (lock_unix.go) have no concurrency test, though in-process flock contention is easily reproducible.
- The "always ordered after durable state commits" invariant is asserted nowhere; it survives only by call-order accident (it currently holds at every call site I traced: flow.go:169 after `runFlowStage`, execute.go:327→342 after `WriteExecuteSummary`, review.go:630→633 after `saveReviewState`, smoke.go:35→54 after `saveSmokeAttempt`/`commitSmoke`, run_loop.go:459-466 after `save()`+`SyncRunHistory`).
- `TestPublisherRetriesPushWithoutDuplicateCommit` (publisher_test.go:47) does not test a retry mechanism; it asserts that manually rerunning after a fixed failure doesn't duplicate the commit. The name implies automatic retry logic that doesn't exist.
- Minor companion: early `Publish` failures return zero-value `Result{}`, which sprint/study adapters append as phantom entries (`{"repository":"","committed":false,...}`) to machine-readable `Publications` (sprint/publication.go:51-54, study/publication.go:24-26) — distinguishable from real entries, but noise a JSON consumer must special-case.

**Why this matters (bugs current verification allows):** findings 1's stale-refspec behavior and 2's leak both sit entirely in uncovered code; the `%!w(<nil>)` wart survived precisely because nothing executes that line.

**Severity:** Low individually, but this is the main reason defects 1–3 exist undetected. **Confidence:** high (absence verified by grep across all `_test.go`).

---

## Finding 5 — Inherited `GIT_DIR`/`GIT_WORK_TREE` etc. are forwarded into every publication git invocation

**Claim:** `gitEnvironment` forwards the full parent environment minus its two overrides (publisher.go:284-302). `-C dir` does not override `GIT_DIR`, so an inherited location variable redirects *all* commands — including `rev-parse --show-toplevel`, `update-ref`, and `push` — to a different repository than the requested root. Everything stays self-consistent inside the wrong repo, so owned-path checks validate against the wrong root; the `All:true` execute publish has no path list to catch it.

**Bad outcome:** Running ultraplan under an environment exporting `GIT_DIR` (git hooks; GitHub Actions exports an absolute `GIT_DIR` after `actions/checkout`) makes `commit-and-push` commit and push the *foreign* checkout to *its* origin.

**Trigger:** stage_completion ≠ off + parent env carries location-affecting `GIT_*` vars (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY`).

**Counter-evidence:** relative `GIT_DIR` values resolve after `-C` and often coincide harmlessly; ordinary shells don't set these; escape checks fail closed for explicit path sets. Hence low severity, but the fix is a one-line filter in `gitEnvironment`.

**Severity:** Low. **Confidence:** high mechanism, medium plausibility.

---

## Defended non-issues

- **Whole-branch push scope** (push sends all local commits on the branch, not just owned-path commits; already-complete stage reruns trigger such pushes): documented contract — "pushes the current branch, using its upstream when present" (configuration.md:222, user-guide.md:307) — and required for the pinned push-failure recovery semantics (publisher_test.go:47). Opt-in mode. Not a defect.
- **Run-loop halts scheduling on publication errors** (run_loop.go:345-347 treats consumer-goroutine publish failures like persist failures): matches documented fail-closed semantics; task state is durable and resumable, so consequence is an early stop, not loss.
- **Windows no-op lock** (lock_other.go): CI is linux-only (`.github/workflows/ci.yml`, ubuntu-latest); unsupported platform, dead-in-practice stub.
- **flock is advisory vs. external git processes:** inherent; the CAS `update-ref <ref> <commit> <parent>` bounds damage to a clean, detected failure.
- **`CombinedOutput` merging stderr into parsed stdout** (warnings could corrupt `rev-parse`/`for-each-ref` values): speculative under normal operation with `LC_ALL=C`; folded into Finding 4's coverage note rather than reported standalone.
- **Temp index/lock file residue after SIGKILL:** negligible hygiene, no correctness impact.