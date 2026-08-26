The repo has uncommitted changes; my review must be against the frozen commit. Let me check whether my target files differ from the frozen commit.Working tree matches frozen commit for all primary files except review.go (I'll use `git show` for that one). Now reading the core files.Initial observations noted. Now reading service.go Status/DeriveStages and supporting files.Tests confirm nothing recovers a corrupt lock. Now I'll prove the two main findings with an overlay test (virtual file, target tree untouched).Both findings proven by execution. Checking a few remaining paths to complete coverage.# Surface review: `sprint-flow-state` — findings

Review performed independently against frozen `5d54525`; working tree matches frozen commit for all primary files except `internal/sprint/review.go` (frozen version read via `git show`). Two findings were reproduced by executing tests via `go test -overlay` (virtual file; target tree untouched).

---

## F1 — Corrupt/empty lease pidfile permanently wedges all sprint mutations; reconcile silently skips the sprint

**Claim.** A crash between `O_EXCL` create and lock-info write leaves a zero-length or partial pidfile. `readVerificationFileLock` then fails closed on every future acquisition, and nothing in the codebase ever recovers such a file.

**Observable bad outcome.** Every flow/execute/review/smoke/verify/QA/reconcile operation on that sprint returns `ErrVerificationConflict ("unreadable lock …")` indefinitely. Worse, `ReconcileInterruptedMutation` maps conflict to `(false, nil)` (`locks.go:28-30`), so web-startup reconciliation never repairs the sprint's interrupted evidence either — durable running attempts stay "running" forever, with no actionable error naming the fix.

**Trigger / preconditions.** Process death (kill -9, power loss, OOM) inside the create→write window of `acquireVerificationFileLock` (`verification_lock.go:33-44`: file created at :33, JSON written at :35-37); or any external truncation of `.ultraplan/locks/sprint/<p>--<s>.lock`.

**Evidence.** `verification_lock.go:49-51` — unreadable/invalid existing lock ⇒ `ErrVerificationConflict` with no staleness path; grep confirms no other reader/remover of the locks directory; `service.go:101-104`; `locks.go:26-31`. Tests pin dead-PID replacement (`verification_lock_test.go`) but never corrupt files.

**Reproduction (executed).** Overlay test wrote an empty and a truncated (`{"project":"alpha","pid":`) lock file: acquire fails with "unreadable", reconcile returns `(false,nil)`, a seeded expired review attempt (dead PID, 4h-stale heartbeat) remains `ActiveAttempt=running` after reconcile, and the wedge persists across retries. PASS both subcases.

**Counter-evidence searched.** Fail-closed on foreign garbage is deliberate; EPERM-as-alive correctly prevents cross-user theft; release ownership tuple prevents wrong releases. None of these provide recovery for self-inflicted crash residue.

**Severity:** High (availability of every gated mutation for the sprint; startup healing disabled) · **Confidence:** High (reproduced).
**Regression test:** empty + truncated lock file must be classified stale (removed/retried), or reconcile must surface an explicit error instead of `(false,nil)`; assert acquisition succeeds afterward.

---

## F2 — Status refresh is an unsynchronized read-modify-write that can silently erase completed Review/Smoke/QA evidence

**Claim.** `Service.Status` loads flow state (`service.go:250`), copies `Review/Smoke/QA` pointers from that snapshot (:266-268), then saves without holding the mutation lease or any compare-and-swap (:291-294). Any lease-holding writer committing between load and save is overwritten by stale bytes; `SaveFlowState`'s evidence backfill cannot help because the incoming pointers are non-nil (`state.go:204-218`).

**Observable bad outcome.** A review completion (verdict/digest/`LastComplete`) or QA pointer persisted in the window is reverted: smoke's review gate subsequently fails ("a current review is required", `smoke_protocol.go:181-183`), QA map/resume are blocked, stage records can regress — expensive verification work must be rerun. No error is reported; the loss is silent.

**Trigger / preconditions.** Any statusWrites-enabled Status racing a verification/planning writer:
- CLI `sprint status` (`app/sprint_commands.go:88,95`), project rollups (`project_usecases.go:39`), operational kinds (`operations.go:418,439,475,493` — `NewOperationalUseCases` has `readOnly=false`, `operations.go:908`);
- TUI `SprintSummaries` (`sprint_usecases.go:482`) refreshes on **every operation event** (`tui/app.go` OperationMsg/OperationEventMsg → `refreshCmd`), using a *different* Service instance than the operation runner (`operation_runner.go:80` creates its own), so even the same-process `sync.Map` guard does not apply.
The load→save window includes `PrepareReview` plus artifact reads/validation (ms–s).

**Evidence + reproduction (executed).** Re-enactment of the exact interleaving (load → concurrent completion save → status save): final state shows `Review.Status=running, Fingerprint=old-fp`, `LastComplete=nil` — the completed record was silently discarded. No CAS, UpdatedAt check, or digest guard exists anywhere in the file save path.

**Counter-evidence searched.** Web dashboards use `WithoutStatusWrites` (`serve_commands.go:59`, `sprint_usecases.go:1010`) — but TUI/CLI/operations do not; rename atomicity prevents tearing, not lost update; `VerificationStatus` is read-only and irrelevant here.

**Severity:** Medium-High (silent loss of authoritative gate input) · **Confidence:** High mechanics (reproduced); medium-high real-world frequency.
**Regression test:** interleaving test as above asserting the later-arriving status refresh cannot clobber a strictly-newer persisted record (fix options: detect changed bytes since load before rename, take the lease for status writes, or stamp-and-compare `UpdatedAt`).

---

## F3 — One unreadable record in any sprint aborts workspace-wide reconciliation and blocks web server startup

**Claim.** Reconcile hard-fails per sprint on: invalid `.cleanup-uncertain.json` (`locks.go:40-44`), malformed execute run state (:64-66), malformed/unsupported flow state (:98-100). `ReconcileOperations` aborts the whole loop on first error (`web_usecases.go:569-576`), and the web server closes its listener and refuses to start (`web/server.go:76-80`).

**Observable bad outcome.** A single corrupt/hand-edited/newer-schema `flow-state.json`, malformed `.run-state.json`, or damaged marker in one old sprint makes `serve` unbootable until manual repair; sprints later in discovery order are also never reconciled. Presentation surfaces deliberately degrade ("status unavailable", `sprint_usecases.go:485-491`) while reconciliation denies the entire product.

**Controls / counter-evidence.** Fail-closed marker consumption is deliberate and pinned (`cleanup_uncertain_test.go` retains the marker on `ErrCleanupUncertain`); legacy v0/v1 strata *are* tolerated (`locks_test.go:46,81`); live-owner conflicts correctly skip. But there is no per-sprint error isolation, and no test pins whole-boot denial either way.

**Severity:** Medium (product-wide availability from one bad file) · **Confidence:** High on code path; medium on contract judgment (fail-fast could be intended, but contradicts the product posture everywhere else).
**Regression test:** two-sprint fixture (A: malformed flow-state; B: running task) asserting B is still reconciled and/or server startup proceeds.

---

## F4 — DB-authoritative saves report failure after the DB row is committed

`state.go:219-228`: DB write succeeds (authoritative), then terminal-checkpoint file write failure returns an error anyway. Terminal commits (`smoke.go:477-480`, review persistence, `locks.go:71`) then report `smoke_reconciliation`/persistence failure although evidence is durable, prompting needless reruns; reconcile rerun is idempotent so harm is bounded. Confirms context-pack unknown #2 as real. **Severity:** Low-Medium · **Confidence:** High on semantics. Verification: seeded DB record + unwritable sprint dir asserting the double-failure reporting.

## Defended non-issues / minor notes

- **PID reuse** (lock liveness false-alive; expiry falls back to 2h heartbeat): both failure directions are conservative and bounded (`verification_lock.go:95-101`, `verify.go:467-479`). Accepted risk.
- **Lease filename ambiguity**: `Project+"--"+Slug` collides for legal names (`IsSafeName` permits `-`): project `a`+sprint `b--c` vs project `a--b`+sprint `c`. Effect is spurious cross-sprint conflicts (fail-safe direction); low likelihood (`verification_lock.go:27`).
- **Requirements Failed never clears once artifact becomes valid out-of-band** ("already complete" skip writes no state, `flow.go:128-157`; DeriveStages preserves Failed only for requirements, `service.go:1499-1504`): presentation inconsistency only — no gate consumes requirements stage status (only code-context does, `code_context.go:245-255`).
- **Stale 6-stage checkpoint after `storage.migrate`** suppresses Status-refresh writes mid-sprint (`preCodeContextFlowState` reads file only, `service.go:249`); DB truth unaffected, gates read DB-first. DB records provably never hold v1/pre-code-context shapes (import loads via `LoadFlowState` first, `storage_commands.go:142-162`).
- **`syncDir` errors ignored / unix-only `syscall.Kill`**: durability nuance and platform assumption consistent with the linux-only target.
- Verified sound: atomic temp+fsync+rename publication with prior-bytes preservation under injected hook failure; v1 migration strictly read-time-only; store `Load` orders items by ordinal and `Save` deletes absent item keys; attempt-expiry can only persist when the lease is free (live owners hold it); cleanup-marker consumption rules match the pinned tests.

The two repro tests live at `/tmp/opencode/gauntlet/zz_gauntlet_wedge_test.go` (runnable via `go test -overlay /tmp/opencode/gauntlet/overlay.json ./internal/sprint -run TestZZGauntlet`) and double as regression-test skeletons for F1/F2.