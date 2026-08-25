# Context Pack: `repo-publication` — Git stage publication

Target: ultraplan-go @ 50d6f0d25ff273f4cb956f11e6030376108110bc (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: governed-sprint-delivery. Risk: normal. Descriptive only — no defect judgments.

## 1. Purpose

Opt-in publication of exactly the paths owned by a completed stage, always ordered after that stage's durable state writes. `git.stage_completion` selects three modes:

- `off` (default): no Git mutation; every publish helper degrades to a no-op (`Result{Skipped:true}` or nil publisher).
- `commit`: create a local commit on the current branch containing only the stage-owned paths.
- `commit-and-push`: additionally push the current branch to its upstream (or the configured remote, setting upstream when none exists), bounded by a timeout with credential prompts disabled.

Mechanisms owned here: temp-index staging that preserves the user's staged index; compare-and-swap `update-ref` against the expected parent commit; an advisory `flock` publish lock per physical repository; remote-name validation before shell-free git argv invocation; roadmap delivery marking folded into smoke workspace publication. A push failure stops the command but never reopens a completed product stage; rerunning finishes the push without duplicating the commit (docs/configuration.md:222-227).

## 2. Entrypoints and control flow

### 2.1 Wiring (`internal/app/git_publication.go:10`)
- `stagePublisher(cfg)` returns `nil` when `cfg.Git.StageCompletion == string(ModeOff)`; otherwise `gitpublish.New(Policy{Mode, Remote, PushTimeout})`. The duration parse error is discarded (`timeout, _ := time.ParseDuration`) — config validation (`config.go:483-486`) rejects invalid/non-positive/`>30m` durations earlier in the pipeline.
- Injected via `sprint.Service.WithPublisher` (`internal/sprint/service.go:60`, field at :42) and `study.WithPublisher` (`internal/study/service.go:100`) at:
  - `app/sprint_commands.go:88` (`runSprint` default service) and `:710` (`sprintRuntimeService`, used by runtime-backed flow/execute/review/smoke commands).
  - `app/operation_runner.go:80` (operation-runner service for web/TUI sprint operations).
  - `app/study_commands.go:351` (`runLoopService`), `:699` (`runAllService`), `:877` (`executionService` for single analysis/synthesis runs).

### 2.2 `CommandPublisher.Publish` (`internal/platform/gitpublish/publisher.go:68`)
1. `ModeOff` → `(Result{Skipped:true}, nil)`.
2. Root must clean to non-empty and not `"."`.
3. Resolve repository via `git rev-parse --show-toplevel`; absolute path recorded in result.
4. Branch via `git branch --show-current`; empty (detached HEAD) → error.
5. `publishPaths` (:169): `All=true` → `["."]`; otherwise each requested path is cleaned, made absolute, converted relative to repo root; `..` escape → error; deduplicated, slash-normalized, sorted.
6. Common dir via `rev-parse --git-common-dir` (abs-ified); lock path `<common>/ultraplan-publish.lock`.
7. `acquireLock` (`lock_unix.go:17`, linux/darwin): open `O_CREATE|O_RDWR 0600`, then `flock(LOCK_EX|LOCK_NB)` retry loop every 100 ms until acquired or ctx done; `EWOULDBLOCK`/`EAGAIN` retried, other errno fails immediately; release = `LOCK_UN` + close. Non-unix build (`lock_other.go:14`): ctx check + file creation only — no exclusion primitive.
8. If paths non-empty → `commitPaths` (:200):
   - Message required (trimmed). Identity appended as `\n\nUltraPlan-Publication: <identity>` trailer.
   - `parent = rev-parse HEAD`.
   - Temp file created in common dir (`ultraplan-index-*`), closed and unlinked immediately; the *path* is then used as `GIT_INDEX_FILE` env for subsequent git calls; deferred remove.
   - `read-tree <parent>` seeds the temp index with the full parent tree.
   - `add -A -- <paths>` stages only owned paths into the temp index.
   - `write-tree`; if equal to `parent^{tree}` → return `(parent, false, nil)` — nothing owned changed, no commit.
   - `commit-tree <tree> -p <parent>` with message on stdin (plumbing; author/committer from inherited env/git config).
   - Re-resolve branch ref via `symbolic-ref -q HEAD`.
   - `update-ref <ref> <commit> <parent>` — CAS against expected old value; concurrent movement of the branch since `parent` was read fails with "branch changed while committing".
   - `reset -q HEAD -- <paths>` on the **real** index reconciles the user's index entries for owned paths to the new HEAD; all other staged state is untouched (pinned by publisher_test.go:39).
9. If no paths requested (or empty set after filtering) and `result.Commit == ""` → record current `rev-parse HEAD` without committing.
10. Mode != `commit-and-push` → return (local commit only, `Pushed=false`).
11. `validRemoteName(policy.Remote)` (:304): `[A-Za-z0-9._/-]`, no leading `-`; same charset rule as config-side `validGitRemoteName` (`config.go:540`).
12. `pushCtx = WithTimeout(ctx, policy.PushTimeout)` (default 2m at :62-64; config caps ≤30m).
13. Upstream resolution: `for-each-ref --format=%(upstream:remotename) refs/heads/<branch>`; if non-empty (validated), also fetch `%(upstream:remoteref)` and `push --porcelain -- <remote> HEAD:<upstreamref>`; else `push --porcelain --set-upstream -- <policy.Remote> HEAD:refs/heads/<branch>`.
14. Push error with `pushCtx.Err() == DeadlineExceeded` → explicit "push timed out" error; success → `Pushed=true`.

### 2.3 Git invocation plumbing (:263-302)
- `exec.CommandContext("git", "-C", dir, args...)` — argv form, no shell. `CombinedOutput` is embedded into errors ("git <args>: <output>"). Environment = `os.Environ()` minus overrides, plus `GIT_TERMINAL_PROMPT=0` and `LC_ALL=C`, plus caller extras (`GIT_INDEX_FILE` during index operations). Credential prompting is disabled process-wide for these invocations.

### 2.4 Sprint publication adapters (`internal/sprint/publication.go`)
- `publishPlanningStage(ctx, projectRef, sprintRef, stage)` (:11): nil publisher → `(nil,nil)`; resolves the sprint via `resolveMutationSprint`. Paths: `<sprint>/flow-state.json` plus either
  - area-reasoning: reads planning inputs, resolves catalog, builds `BuildReasoningManifest` (`reasoning.go:36`); any findings abort publication before any Publish; each manifest `ReasoningTemplates[].OutputPath` (workspace-normalized, joined under `s.root`) is added; or
  - other stages: `ArtifactPath(root, sp, stage)` single artifact;
  - code-context additionally appends `.workspace.json` (`execute_target.go:27`).
  One `Publish` on workspace root `s.root`; message `ultraplan: sprint <p>/<slug> complete <stage>`; identity `sprint/<p>/<slug>/<stage>`.
- `publishExecuteStage` (:57): two sequential publishes — implementation target first (`Root=target.Path, All:true`, identity suffix `/implementation`), then workspace (`execute.md` + `.run-state.json`, suffix `/workspace`). Implementation error short-circuits; workspace is not published in that case.
- `publishReviewStage` (:80): `review.md` + `flow-state.json` on workspace root.
- `publishSmokeStage` (:93): optional harness publish first when `result.AuthorChangedPaths` non-empty (paths joined under `prepared.HarnessRoot`, identity suffix `/harness`); error short-circuits. Then workspace publish of `smoke.md` + `flow-state.json`, plus `<projects>/<project>/roadmap.md` only when `Verdict==SmokePass && !DiagnosticOnly` (:115-117).
- `visiblePublication` (:123): filters out skip results (`Repository=="" && Skipped`) from returned slices.

Call sites and ordering (publication strictly after durable persistence):
- Flow multi-stage loop (`flow.go`): already-valid stages publish too (:149); freshly completed stages publish after `runFlowStage` returns (:169); single-stage `FlowStage` publishes at :215. Failure emits progress state "publish-failed" and returns the error.
- Execute (`execute.go:341-347`): only when `allExecuteTasksResolved`, after `WriteExecuteSummary`/`SaveExecuteRunState`; runs inside the mutation lease (`Execute` acquires at execute.go:131).
- Review (`review.go:633`): after `atomicWriteReview` (:622) and `saveReviewState` (:630); lease acquired at review.go:411.
- Smoke (`smoke.go:39-58`): `project.MarkRoadmapSprintDelivered` runs BEFORE publication (:45); publication follows via `prepareSmokeStatic` + `publishSmokeStage`; whole sequence under the mutation lease (acquired smoke.go:25). Roadmap mark failure aborts before publication.

### 2.5 Study publication adapters (`internal/study/publication.go`)
- `publishExecution(result, extraPaths...)` (:10): gates on publisher set AND `Status==ExecutionStatusCompleted`; paths = `OutputPath` + extras (callers pass run-state.json etc.); message/identity `study/<name>/<kind>/<subject>` where subject is dimension ref (+ source name). Appends to `result.Publications` unless skipped.
- `publishRunAllSummary` (:30): gated on `RunAllStatusCompleted`; publishes `SummaryPath`.
- `publishRunLoopState` (:45): publishes run state + history + history-summary paths for a study.
- Call sites:
  - End of `RunAnalysis` (`run.go:83,102`) and `Synthesize` (`synthesize.go:80,100`), gated by `!req.DeferPublication`; only reached when validation passed and status Completed.
  - Run-loop sets `DeferPublication:true` (`run_loop.go:316,323`) and instead funnels completed `ExecutionResult`s into a buffered channel consumed by one background goroutine (:249-262) — publications are serialized through that consumer while task execution continues. Enqueue happens after `applyExecutionResult` + `recordHistory` (:334-338). The channel is drained (`<-publicationDone`) before the final `save()` + `SyncRunHistory`, then terminal `publishRunLoopState` runs (:450-473).
  - `RunAll` publishes the summary last (`run_all.go:50`) after group execution and `WriteSummary`.

## 3. Inputs / outputs

Inputs: effective config `Git{stage_completion, remote, push_timeout}` (defaults `off`/`origin`/`2m`, `config.go:190`; env mapping `ULTRAPLAN_GIT_STAGE_COMPLETION` / `ULTRAPLAN_GIT_REMOTE`? — see §11 unknowns; validation `config.go:475-486`); request `{Root, Paths, All, Message, Identity}` assembled by domain adapters; live git state (HEAD, branch, worktree/index contents, upstream configuration); artifacts written by the just-completed stage.
Outputs: commits appended to the current branch of the addressed repository (workspace repo, execute target worktree repo, smoke harness repo); pushes to remotes incl. possible `--set-upstream`; updated real-index entries for owned paths; lock-file existence; `Result{Repository,Branch,Commit,Remote,Committed,Pushed,Skipped}` records carried on domain results (`FlowResult.Publications`, `ExecuteResult.Publications`, `ReviewResult.Publications`, `SmokeResult.Publications`, `ExecutionResult.Publications`, `RunLoopResult.Publications`); CLI/web errors on publication failures.

## 4. Authoritative state

- Git refs and object databases of up to three repo classes per operation: workspace repo (`s.root`), execute target repo (`target.Path` — the dedicated sprint worktree created by code-context, tracked via `.workspace.json`), smoke harness repo (`prepared.HarnessRoot`). Study publications address `study.Path` subdirectories; `Publish` resolves upward via `--show-toplevel`, so those commits land on the containing repository's branch.
- Lock artifact: `<git-common-dir>/ultraplan-publish.lock` — per physical repo (shared across linked worktrees because the common dir is shared). Content-free coordination file, mode 0600, never deleted by this code.
- Roadmap delivery mark: `projects/<project>/roadmap.md` status line rewritten to `delivered` by `MarkRoadmapSprintDelivered` (`project/roadmap_status.go:15`): parse-gated (duplicate slug → error; missing slug with existing governed sections → error; zero sections tolerated as legacy), idempotent when already delivered, atomic write (temp+fsync+rename preserving mode, :83-110).
- Config resolution state: `effective.Config.Git.*` with documented precedence (defaults → workspace config → env; pinned by config_test.go:158-182).

## 5. Invariants (as implemented)

- No publisher injected or mode `off` ⇒ zero git invocations; all helpers short-circuit before resolving sprints where noted (study helpers gate on `s.publisher == nil` first; sprint `publishPlanningStage` checks before resolve).
- Only stage-owned paths are staged, via a private temp index seeded from parent; unrelated staged/unstaged user changes survive (except `reset -q HEAD -- ownedPaths` reconciling exactly those index entries post-commit).
- Unchanged owned content ⇒ tree equality ⇒ no commit (`Committed=false`, Commit=parent).
- Ref update is CAS-guarded against parent drift; detached HEAD refused both at entry and at ref resolution.
- UltraPlan publishers serialize per physical repo via flock; the CAS adds cross-checkout safety within the window between parent read and ref write.
- Remote names validated twice (config load; pre-push for both configured and upstream-derived names).
- Every push is timeout-bounded, prompt-disabled, porcelain-flagged; no force flags are ever passed (non-fast-forward pushes fail normally).
- Ordering: publication occurs only after the stage's durable writes (§2.4/§2.5 call sites); smoke's roadmap mark precedes publication of roadmap.md; run-loop publications drain before terminal run-state publication; dry-run, cancelled, failed, and validation-invalid outcomes never reach `Publish`.
- Push failure leaves the local commit intact; retry produces no duplicate commit (no-op path + test pin).

## 6. Trust boundaries

- Pushes to configured remotes are externally visible and effectively irreversible; remote identity comes from repo git config (upstream) plus validated name strings — URLs are never supplied by UltraPlan input.
- All git interaction is shell-free argv; remote-name charset validation prevents option-injection via leading `-`.
- Path inputs must resolve inside the repository root (rel-based containment check); `All=true` intentionally commits the entire target tree and is used for the dedicated execute worktree.
- Smoke harness paths (`AuthorChangedPaths`, harness-produced data) are joined under `HarnessRoot` before the containment check inside `publishPaths`.
- The child git processes inherit the ambient environment (minus two overrides), including any user-configured `GIT_*` variables, credential helpers, and hooks that git itself honors (e.g., pre-push hooks fire on push; plumbing `commit-tree` does not run pre-commit/commit-msg hooks).
- Commit messages embed internal identifiers (project/slug/stage/study names); the trailer key is fixed (`UltraPlan-Publication:`).

## 7. External effects & lifecycle semantics

- External effects: local commits on the addressed branches; pushes (including first-push upstream establishment); roadmap delivery marks; real-index reconciliation of owned paths; persistent lock file.
- Cancellation: honored while waiting on the lock (100 ms poll against ctx); push cancellable via deadline; cancellation mid-commit leaves at most an unreferenced commit object (ref moves only at `update-ref`), temp index removed by defer.
- Partial-completion windows: `publishExecuteStage` may commit the target repo before a workspace failure; `publishSmokeStage` may commit harness before workspace failure; multi-stage flow may have published earlier stages before a later stage's publish fails. Each completed unit stays committed; callers receive the joined/partial error.
- Retry/restart: rerunning the same command re-publishes; unchanged trees no-op; failed push retries without duplicate commit. There is no queue or journal of unpublished work — recovery is by rerunning the idempotent stage-level publish.
- Concurrency: two UltraPlan publishers serialize on flock; arbitrary external git activity is not excluded (advisory lock among UltraPlan processes only); CAS covers ref races on the committed branch.
- Error semantics: publication failure surfaces as command/operation error (flow emits "publish-failed" progress; smoke joins via `errors.Join`; study run-loop records first error and fails the run-loop after draining). Stage completion itself is never rolled back (docs/user-guide.md:307, docs/configuration.md:226).

## 8. Immediate surface dependencies

- Sprint flow-state authority: flow-state.json is written by the stage before publication and is itself part of published path sets.
- Execute run-state writers (`SaveExecuteRunState`, `WriteExecuteSummary`) and the code-context-created target worktree (`.workspace.json`).
- Reasoning manifest builder/validation (`BuildReasoningManifest`) gating area-reasoning output path collection.
- Smoke protocol preparation (`prepareSmokeStatic`, `smokePrepared.HarnessRoot`, `SmokeResult.AuthorChangedPaths`, verdict/diagnostic-only fields).
- Project roadmap parsing/marking (`ParseRoadmap`, `MarkRoadmapSprintDelivered`).
- Product config loading/validation and env mapping for `git.*` keys.
- App service factories wiring publishers (§2.1) and the mutation-lease subsystem (`acquireMutationContext`) enclosing sprint publications.
- Study validation pipeline (`ValidateSourceReport`/`ValidateFinalReport`) gating publication on Completed status; run-loop scheduler owning the publication consumer goroutine.

## 9. Contracts (CURRENT-CONTRACT evidence)

- docs/configuration.md:220-227 ("Git stage publication"): mode semantics; owned-paths-only commits; execute whole-worktree exception because the worktree belongs to one sprint; agents prohibited from running `git add`/`git commit`/`git push`; push failure does not reopen a completed stage; rerun finishes the push without duplicating; no credential prompts; `git.push_timeout` bounds each attempt.
- docs/user-guide.md:305-307: publish each valid completed stage before starting the next; planning artifacts and study reports commit only owned paths; execute commits the complete worktree plus summary/run state; review and smoke publish canonical evidence even on failing verdicts; dry runs/cancelled/invalid output never publish; upstream preferred, else `git.remote` with upstream set.
- docs/configuration.md:194: config rejection contract for invalid modes, whitespace/empty remote names, timeouts above 30m (enforced config.go:475-486).
- Workspace side: no dedicated publication contract found under projects/ultraplan-go for this feature; TRD.md:2237 discusses filesystem/SQLite/server publication-mode comparison as future direction (FUTURE-INTENT context only). Implementation docs above are the operative contract.

## 10. Tests (evidence map)

- internal/platform/gitpublish/publisher_test.go:
  - TestPublisherCommitsOnlyOwnedPathsAndPreservesIndex (:13): owned+untracked paths committed; unrelated user-staged file remains staged; `UltraPlan-Publication:` trailer present.
  - TestPublisherRetriesPushWithoutDuplicateCommit (:47): failing remote → local commit retained, not pushed; after remote fix, second Publish pushes the identical commit with `Committed=false`; remote head equals first commit.
- internal/sprint/publication_test.go:
  - TestPublishPlanningStageUsesOwnedWorkspacePaths (:26): exact path set {requirements.md, flow-state.json}; rooted request, not All.
  - TestPublishExecuteUsesTargetBeforeWorkspace (:55): target-first ordering, `All` on target, scoped paths on workspace.
- internal/study/publication_test.go:
  - TestPublishExecutionIncludesReportAndRunStatePaths (:19): path order and identity string `study/demo/analysis/02-runtime/source`.
  - TestPublishExecutionIgnoresFailedStage (:42): validation-failed result triggers no publish.
- internal/sprint/smoke_test.go:368: roadmap `> Status: delivered` asserted after passing smoke.
- internal/platform/config/config_test.go:158-189: defaults, env/workspace precedence (`Sources["git.stage_completion"]=="env"`), and validation rejections for bad mode/remote/timeout.
- Baseline evidence: `go build ./...` passes at the frozen commit (verified this session); full test baselines live in review/baseline/.

## 11. Explicit unknowns / open questions (for later reviewers)

- Env mapping confirmed at config.go:137-139: `ULTRAPLAN_GIT_STAGE_COMPLETION`, `ULTRAPLAN_GIT_REMOTE`, `ULTRAPLAN_GIT_PUSH_TIMEOUT` map to `git.*` fields alongside workspace-config keys.
- Whether study directories always live inside a git repository (publications assume `--show-toplevel` resolves upward) and what happens for non-repo studies (Publish would fail on step 3; no adapter-level guard observed).
- Push-retry semantics after remote divergence (non-fast-forward rejection) — docs promise retry-without-duplicate for the failed-push case but do not address diverged upstreams.
- Hook interactions: pre-push hooks would fire (git-native); plumbing commit path bypasses pre-commit/commit-msg hooks — neither behavior is documented explicitly.
- Non-flock platforms (`lock_other.go`): mutual exclusion absent; deployment prevalence unknown.
- Consumers of `Result.Remote`/`Publications`: domain results carry them, tests assert them; no app-layer rendering located (possible web/TUI consumption not traced exhaustively).
- `timeout, _ := time.ParseDuration` at git_publication.go:14 relies entirely on upstream config validation; direct calls bypassing `Validate` would silently default to 0 (→ publisher default 2m) rather than error.
