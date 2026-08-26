Baseline confirmed green (`go build ./...`, `go test ./internal/sprint/` ok at frozen commit `c6f01cf`). Below is the full review: three findings (each counter-evidence-checked) plus defended non-issues.

---

## Finding 1 — `Service.Status` performs an unleased read-modify-write of the authoritative flow state and can durably revert concurrent lease-holding writers (lost update)

**Claim.** Every stage operation serializes flow-state mutations through the mutation lease (`locks.go:13-15`: "shared by flow, execute, review, smoke, and verify"), but `Status` — which also *writes* flow state when `statusWrites` is true (default) — acquires neither lease layer. Its load→derive→save sequence is last-writer-wins and can overwrite a concurrent writer's transition, including reverting a just-committed terminal review/smoke record back to a running attempt.

**Observable bad outcome.** Process A (`sprint review`) holds the lease and commits `Review.Status=Completed` with verdict/digest/`LastComplete` and clears `ActiveAttempt` (`review.go:1251-1276` via `SaveFlowState`). Process B (`sprint status` CLI, or TUI summary refresh — both write-enabled: `app/sprint_commands.go:88-95`, `app/tui_commands.go:41-46` where `readOnly` is unset) loaded the pre-commit state and saves afterward. Durable result: verdict erased, `Review.Status=running` with an `ActiveAttempt` owned by A's now-dead PID. The next `ReconcileInterruptedMutation` converts it to `timed_out`/failed (`locks.go:67-75`, `verify.go:481-495`) — a passing review silently becomes failed history and must be rerun (expensive LLM work). Intermediate variants: heartbeat/checkpoint regressions (`review.go:866-874` heartbeats reverted), and persisted `Stale=true` from B's transient `PrepareReview` failure is trusted verbatim by the QA map gate (`qa_map.go:63` reads `flow.Review.Stale`; smoke recomputes instead at `smoke_protocol.go:184-196` — the asymmetry makes the pollution stick for QA).

**Trigger/preconditions.** Any `Status` invocation with default `statusWrites` overlapping a writer's critical section. The window spans `PrepareReview` inside `Status` (`service.go:270`), i.e., tens–hundreds of ms per refresh, across a review/smoke run of minutes–hours. Cross-process by construction (sync.Map guard is per-`Service` instance and unused here anyway).

**Evidence / execution path.** `service.go:229-295`: `LoadFlowState` (250) → `refreshed := NewFlowState(...)` copying prior pointers (264-268) → `if s.statusWrites && !legacyCodeContextState { SaveFlowState(refreshed) }` (291-295). No `acquireMutation` anywhere in the path (`acquireMutation`, `service.go:89-110`, uncalled). `SaveFlowState` file branch unconditionally renames new bytes; no compare-and-swap (`state.go:242-292`, rename at 286). Evidence preservation backfills only nil pointers (`state.go:204-218`), so B's stale non-nil `Review` copy wins.

**Counter-evidence searched.** Web dashboards are mitigated (`app/serve_commands.go:59` and `web_usecases.go:429` set `readOnly:true` ⇒ `WithoutStatusWrites`), and `WithoutStatusWrites` exists precisely because the write is known-dangerous — but CLI/TUI keep it enabled. No lock, hash-guard (DB branch only), revalidation, or retry exists in the file branch. No test exercises Status concurrent with a writer; `TestSprintMutationLeaseIsSharedAndCompositeSafe` covers only lease users. No documentation declares Status-vs-writer races unsupported.

**Severity:** Medium-high (silent loss of expensive verification evidence; misleading `timed_out` history; QA-gate poisoning via persisted Stale). **Confidence:** high on mechanism (deterministic interleaving), medium on field frequency.

**Regression test.** Seed a valid sprint; run a loop of `NewService(root).Status(...)` (writes on) in one goroutine while another completes `saveReviewState` with a terminal result; assert final durable `Review.Status==completed && ActiveAttempt==nil`. Deterministic variant: replay the exact interleaving (load like Status, commit terminal, then perform Status's save) and assert non-reversion. Fix shape: put Status's save under the existing lease, or CAS via the already-present `BeforeRename` seam (re-read canonical bytes; abort if changed since load).

---

## Finding 2 — Write-enabled `sprint status` durably converts v1 flow state despite the read-time-only upgrade contract, persisting synthesized `"legacy-unverifiable"` evidence

**Claim.** The v1→v2 migration is specified and pinned as memory-only (`state.go:68-73`; pinned by `verify_test.go:78-82`: file still contains `schemaVersion 1` after load). `Status`'s compatibility exemption covers only pre-code-context-shaped **v2** files (`preCodeContextFlowState`, `state.go:115-126`, requires `SchemaVersion==2`), so a `schemaVersion:1` file sails through the write gate (`service.go:249` → false; `service.go:291` proceeds) and is rewritten to disk as v2.

**Observable bad outcome.** Running the read-command `sprint status` (CLI or TUI summary) on a v1 workspace irreversibly replaces the historical v1 bytes with v2 containing migration placeholders: `ArtifactDigest`/`InputFingerprint` = `"legacy-unverifiable"`, forced `Stale=true` (`state.go:155-189`). Those placeholders become durable gate input: digest comparison against the real `review.md` hash can never match (`verify.go:188-190`), so previously accepted review evidence is permanently stale and the QA map refuses (`qa_map.go:63`). Original v1 fingerprints/timestamps (`UpdatedAt` restamped at `state.go:247`) are lost. This contradicts the surface's own compatibility posture: reconcile explicitly tolerates-and-preserves legacy strata byte-for-byte (`locks.go:98`, pinned by `locks_test.go:81-114`), while status silently upgrades the same stratum.

**Trigger/preconditions.** v1 file whose migrated form passes `ValidateFlowState` (same stage enum/path shapes; the migration synthesizes validator-satisfying `LastComplete` records). Conditional on that; otherwise Status errors without writing.

**Counter-evidence searched.** No version guard in `Status` or `SaveFlowState` beyond `ValidateFlowState` requiring v2 post-migration (which the refreshed state satisfies by construction, `NewFlowState`). No test covers Status over a v1 file; the load-purity pin stops at `LoadFlowState`. Nothing documents status-driven conversion as the sanctioned forward-migration path; the assignment contract and context pack both describe the v1 upgrade as read-time-only and the stratum as excluded from mutation.

**Severity:** Low-medium (compatibility/history violation; gating impact is conservative — forces re-review rather than admitting bad evidence). **Confidence:** high on mechanics, medium on harm.

**Regression test.** Write a valid v1 fixture; call `NewService(root).Status(...)`; assert either the file still parses with `schemaVersion==1` (posture preserved) or that conversion is explicitly asserted as intended — currently it is neither tested nor guarded.

---

## Finding 3 — Cleanup-uncertain consumption ignores `OperationID`/`Kind`; any unrelated durable change silently resolves a recorded uncertain cleanup

**Claim.** `ReconcileInterruptedMutation` removes `.cleanup-uncertain.json` whenever *any* change occurred (`locks.go:101-105`), but never inspects the marker's `OperationID`/`Kind` (`cleanup_uncertain.go:19-26`) against what was reconciled. An expired review attempt from an unrelated earlier crash flips `changed=true` and consumes the marker recording an unresolved shutdown-deadline cleanup of a different operation.

**Observable bad outcome.** Shutdown exhausts its deadline mid-cleanup of operation X; X's residue is non-canonical (temp files), so reconcile finds nothing for X. Separately, a stale attempt expires and is converted; the marker is deleted; the operator signal that X's cleanup was never verified is lost with no trace.

**Counter-evidence searched.** The design may intend sprint-scoped coarse acknowledgment ("some reconciliation progressed"); the pinned test covers only the refuse-without-change case (`cleanup_uncertain_test.go:38-44`), not scoping. Fail-open-after-progress is defensible — hence low severity — but the carried `OperationID`/`Kind` fields imply intended specificity that the consumer drops.

**Severity:** Low. **Confidence:** medium (contract intent unclear). **Verification:** reconcile with a marker for Kind A plus an expiring attempt attributable to Kind B; assert marker semantics (retained until its own kind reconciles, or documented as coarse and drop the fields).

---

## Defended / non-issues (checked, with disproof)

- **DB-authoritative load skips `DisallowUnknownFields` and v1/pre-code-context handling** (`state_database.go:24-35`): rows are produced only by validated saves or the import path (`storage.migrate` loads via `LoadFlowState`, which migrates, then saves v2); malformed/assembled states hit `ValidateFlowState` (`state.go:28`). No production writer can deposit a six-stage or v1 row.
- **PID-reuse liveness**: lock theft on recycled PID yields a spurious `ErrVerificationConflict` (availability, fail-closed, self-heals when the unrelated process exits); attempt expiry on a recycled owner-PID falls through to the 2-hour heartbeat window rather than false-expiring. Both failure directions are conservative.
- **Unreadable/empty lock file fails closed permanently** (`verification_lock.go:49-51`): deliberate ownership-trust tradeoff; exposure window is the µs between `O_EXCL` create and write.
- **`release()` errors discarded** (`service.go:107`): ownership-mismatch refusals correctly leave a third party's lock; a leaked own-lock self-conflicts until process exit — rare transient-I/O precondition only.
- **Alive-but-slow attempt owners are not falsely expired**: reconcile cannot run under a live lease (`locks.go:27-31`), and expiry needs dead-PID *or* >2h silence; persisted transitions occur only post-crash.
- **`DeriveStages` marks non-empty-but-unvalidated handbook/index/reasoning/plan artifacts Complete** (content validation in snapshots covers only requirements/code-context, `store_fs.go:46-59`): presentation-level skew only — every gate re-validates content at decision time (`flowStageAlreadyValid` `flow.go:284-316`, code-context prerequisite `code_context.go:237-255` validates content, plan fingerprint in run state, smoke/QA digest checks).
- **Web-startup halt on retained `ErrCleanupUncertain` or malformed residue** (`internal/web/server.go:76-81`): fail-closed by design, retention test-pinned; per-sprint status failures are tolerated in listings (`sprint_usecases.go:485-510`).
- **`saveSmokeAttempt` replaces an orphaned running attempt without `LastAttempt` trace** (`smoke.go:212-214`): startup reconcile owns that conversion normally; residual gap is record-keeping only.
- **Linux-only `syscall.Kill`, ignored `syncDir` errors, DB-checkpoint error precedence**: match the repo's platform/durability posture; no concrete failure beyond documented assumptions.