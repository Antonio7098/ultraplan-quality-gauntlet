Building the CLI to reproduce suspected findings end-to-end (build output goes to /tmp, target untouched):Reproduced. Control case without the marker:Now let me empirically probe the lock-layer suspects using a Go test overlay (injects a test file without modifying the target tree):The steal-race reproduced instantly. Fixing my probe-1 setup bug and re-verifying:All three lock-layer probes confirmed. Verifying the target tree is untouched and baseline tests still pass:# Review result — `sprint-flow-state-adversarial`

Target: ultraplan-go @ `5d54525b` (frozen). Baseline `go test ./...` green (review/baseline/summary.json). Target tree untouched; all probes ran via `/tmp` + `go test -overlay`. Verification evidence: end-to-end CLI reproduction + injected overlay tests + code-path citation for each finding.

---

## F1 — One stale cleanup-uncertain marker permanently prevents web server startup; reconcile abort-on-first-error denies recovery to all other sprints

**Severity HIGH / Confidence HIGH (reproduced end-to-end)**

- **Claim:** A valid `.cleanup-uncertain.json` that reconcile cannot "change away" returns `ErrCleanupUncertain`, which is retained by design; `ReconcileOperations` propagates it, and `web.Run` treats any reconcile error as fatal. The marker is consumed only when reconcile itself changes durable state (`hasUncertainty && changed`, locks.go:101-108), so a shutdown-deadline marker recorded with no durable running residue (the common case for planning-stage/status operations, which leave no running attempts/tasks/phases) is **unconsumable by any automatic path** — catch-22.
- **Bad outcome:** `ultraplan serve` refuses to start, every boot, until an operator manually deletes the marker file. Additionally, one sprint's reconcile error (marker, corrupt marker, malformed `.run-state.json`) aborts the loop in web_usecases.go:569-576, so interrupted-mutation recovery never runs for sprints sorted after it.
- **Trigger:** Shutdown deadline exhausted with a non-terminal operation whose sprint has no running execute tasks / unexpired attempts / active QA phase (planning flows qualify).
- **Evidence:** locks.go:40-44, 101-108 (`ErrCleanupUncertain` retained); internal/app/web_usecases.go:563-576 (`return err` on first sprint); internal/web/server.go:76-81 (closes listener, returns error); consumption rule pinned by cleanup_uncertain_test.go:38-44; writer side internal/web/operations.go:493-501, 505-531. Contract tension: ultraplan-workspace TRD ~L2132 requires startup reconciliation to *handle* these cases without inferring success — refusing service to the whole dashboard is not handling.
- **Reproduction:** seeded valid marker in scratch workspace → `serve`: `EXIT=1 serve.start: reconcile interrupted operations: sprint cleanup remains uncertain: ...`; control without marker starts normally. Subsequent successful `sprint alpha 31-web status` (wrote flow-state.json) does not consume the marker; serve still blocked.
- **Existing controls:** error names the marker path; fail-closed retention is deliberate per pinned test — the defect is the fatal whole-server blast radius plus absence of any non-manual resolution path.
- **Regression test:** seed terminal-only sprint + valid marker → assert `ReconcileOperations` either consumes/tolerates per policy and remaining sprints are still reconciled; assert `web.Run` boots.

## F2 — Stale-lock steal race lets two processes hold the mutation lease simultaneously (mutual exclusion broken)

**Severity HIGH / Confidence HIGH (reproduced deterministically-enough: double-acquire in iteration 1, 5/5 runs)**

- **Claim:** In `acquireVerificationFileLock`, after reading a dead-owner snapshot the loser path removes the lock file **unconditionally** (`os.Remove(path)`, verification_lock.go:56) without re-validating that the bytes/inode it validated are still the ones being removed. Interleaving: P1 reads dead snapshot → P2 steals and creates a live lock → P1's remove deletes P2's **live** lock → P1 creates its own. Both hold "exclusive" leases.
- **Bad outcome:** concurrent mutation writers on one sprint exactly in the post-crash recovery scenario the steal logic exists for: interleaved flow-state saves mixing verification evidence (rename = last-writer-wins), duplicate runtime spend, `ReconcileInterruptedMutation` failing running execute tasks under a live executor (locks.go:48-57 converts *any* running task immediately), QA/review attempt records corrupted. Aftermath is partially detected by release's ownership tuple (verification_lock.go:89-91) but release errors are swallowed (`_ = fileLock.release()`, service.go:107), leaving foreign live-PID locks behind.
- **Trigger:** ≥2 processes contending while a valid dead-owner pidfile exists (crashed owner).
- **Evidence:** verification_lock.go:49-59; O_EXCL create serializes creation but not read→remove→create. Violates the workspace contract's stale-writer doctrine (TRD ~L2208 "fencing or equivalent stale-writer protection"; ~L2380 "a lock file containing only PID and timestamp is not sufficient authority").
- **Probe:** overlay test seeding a dead-PID lock, 16 goroutines racing acquire, counting overlapping live holders — DOUBLE-ACQUIRE observed in first iteration, reproduced in 5/5 runs (`go test -overlay`, no target modification).
- **Regression test:** the probe test as written (assert ≤1 concurrent holder across N workers × iterations over a seeded dead-owner lock), or a deterministic interleave seam. Fix direction: validate-then-remove by identity (re-read before unlink, or rename-if-unchanged via fd), or adopt `flock(2)` which kernels serialize.

## F3 — Crash during lock-file write leaves an unparseable pidfile that bricks all mutations permanently

**Severity MEDIUM / Confidence HIGH (behavior deterministic via probe)**

- **Claim:** A lock file created by `O_EXCL` but crashed/killed before (or torn during) content write contains empty/partial JSON. `readVerificationFileLock` fails → acquisition fails closed with `ErrVerificationConflict` **immediately**, before liveness/staleness can be considered (verification_lock.go:49-51). The loop never cleans it; `AcquiredAt` is recorded but never consulted anywhere; there is no age-based escape. Contrast: a fully-written dead-owner lock auto-heals.
- **Bad outcome:** every flow/execute/review/smoke/QA/verify operation on that sprint conflicts forever (until manual rm). Startup reconcile silently skips it (`(false,nil)` conflict path, locks.go:28-30), so nothing surfaces or repairs it. Machine-crash durability gap widens the window beyond the instruction-level one (lock file is never fsynced).
- **Trigger:** kill -9/power-loss in the create→write window; torn page on reboot; external truncation.
- **Probe:** empty and `{partial` lock contents → `acquireVerificationFileLock` returns wrapped `ErrVerificationConflict` persistently (overlay test PASS).
- **Regression test:** seed empty/partial pidfile → assert acquisition recovers per chosen policy (age-out on `AcquiredAt`, or treat zero-length as stealable while preserving fail-closed for non-empty garbage).

## F4 — Lease-free durable Status writes can revert newer verification evidence (lost update)

**Severity MEDIUM / Confidence MEDIUM-HIGH (mechanism certain; concurrency required)**

- **Claim:** `Service.Status` persists a refreshed state without acquiring the mutation lease (service.go:291-295), while all real writers save under the lease. Status copies prior `Review`/`Smoke`/`QA` pointers loaded at T0 (service.go:265-268) and writes them wholesale at T2; `SaveFlowState`'s evidence backfill protects **nil fields only** (state.go:204-218). Any terminal review/smoke/QA write landing between T0 and T2 is silently reverted in the authoritative record. Atomic rename prevents tearing, not lost updates.
- **Bad outcome:** completed review evidence (LastComplete/digest/fingerprint) erased → `VerificationStatus` flips fresh→missing → smoke review gate refuses → forced full review rerun; reverted `flow.QA` summary can contradict `verification/state.json`.
- **Trigger:** realistic within a single TUI session: TUI builds `dashboardUseCases` without `readOnly` (tui_commands.go:41-46), so refresh/status views write (help text even documents it), while an operation started from the same TUI runs on a different Service instance holding only the file lease Status never takes. Also cross-process: CLI `sprint status` / `project status` (write-enabled, app/sprint_commands.go:95, project_usecases.go:39, operations.go:418-493) racing another terminal's review completion. Web browsing is safe (readOnly:true, serve_commands.go:59).
- **Counter-evidence searched:** SaveFlowState's re-load-at-save backfill closes sub-window (b) except when its own load precedes the writer's commit; no other serialization exists (per-Service sync.Map is irrelevant both because instances differ and because Status never acquires).
- **Regression test:** interleave harness (or documented lease acquisition in Status when writing) pinning that a review terminal save concurrent with Status survives in flow-state.json.

## F5 — Lock filename join is ambiguous: unrelated sprints can share one lease file

**Severity LOW-MEDIUM / Confidence HIGH (deterministic probe)**

- **Claim:** Path is `Project+"--"+Slug+".lock"` (verification_lock.go:27) while `project.IsSafeName` permits `-` (project/discovery.go:75-85). `{a, b--c}` and `{a--b, c}` collide on `.ultraplan/locks/sprint/a--b--c.lock`.
- **Bad outcome:** spurious cross-sprint `ErrVerificationConflict` while the other sprint mutates; if one side's dead lock is stolen by the other, release refuses on the Project/Sprint tuple mismatch, leaving a live-PID lock that blocks the unrelated sprint.
- **Probe:** overlay test acquires for `{a,b--c}` then asserts `{a--b,c}` conflicts on the same file (PASS).
- **Regression test:** pin distinct lock paths for colliding-name sprints (e.g., length-prefixed or `/`-free escaped encoding of both components).

## F6 — Transient governed-input failure is converted into durable gate state by a read-path write

**Severity LOW / Confidence HIGH (mechanism)**

- **Claim:** Write-enabled `Status` computes `refreshed.Review.Stale = reviewErr != nil || len(findings) > 0 || …` (service.go:270-271) and persists it. `QAMap` gates on the persisted bit: `flow.Review.Stale ⇒ "a current Conformance Review record is required"` (qa_map.go:63-65).
- **Bad outcome:** one transient PrepareReview failure (git lock contention, momentarily missing input) during an unrelated status call blocks QA map/start until some later Status run succeeds under healthy conditions (self-healing exists but nothing forces it).
- **Regression test:** transient input failure during Status → assert persisted `stale` does not flip to true (or that QAMap failure class distinguishes transient-vs-content staleness).

---

## Defended / non-issues (searched, counter-evidence found)

- **DB-loaded states skip strict grammar** (plain `json.Unmarshal`, state_database.go:25-34): semantics still enforced by `ValidateFlowState` on the assembled state; import path migrates v1→v2 in memory before insert (storage_commands.go:153-155), so DB rows are v2; unknown fields are dropped at next save. Hardening note only.
- **DB-authoritative checkpoint-file failure returns file error though DB row committed** (state.go:222-228): signaling inconsistency; retries are idempotent; DB remains authority. No bad outcome traced.
- **v0/`"version":1` map strata hard-fail Status (exit 5) and abort saves; only reconcile tolerates them** (verified live): consistent with the documented "excluded from mutation/regenerate" posture; the TRD old-flow-state compatibility tests map to the reconcile pins that exist.
- **PID-reuse false-alive blocking a lease**: conservative direction, inherent to PID+timestamp design; folded into F3's no-time-escape discussion rather than double-reported.
- **EPERM-as-alive, 2h expiry constant, ignored `syncDir` errors, immediate QA-phase interruption vs attempt-expiry asymmetry**: all conservative under the lease invariant; no concrete failure path found beyond F2's precondition.
- **`preCodeContextFlowState` probing the file while DB is authoritative**: can only suppress refresh writes (conservative drift), never corrupts.
- **Reconcile ignoring `StatePath`/`Lstat` errors in the QA branch**: unreachable for discovery-resolved sprints.