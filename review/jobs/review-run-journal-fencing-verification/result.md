Context pack read. Now reading the primary sources.Primary sources read. Now reading retention, sanitize, metrics, interfaces, and the app-level callers.App wiring reviewed. Now the remaining tests and helpers.App spine reviewed. Quick look at remaining small files, then I'll run the baseline suite.Full source read. Now running the baseline test suite to confirm green before probing hypotheses.Baseline green. Now writing probe tests via `go test -overlay` (virtual file injection — target tree untouched) to verify three hypotheses empirically.All three hypotheses confirmed empirically. Now verifying counter-evidence on `_txlock` support in the pinned driver, and re-checking two remaining candidates.`_txlock=immediate` and `_defensive` confirmed supported by the pinned driver (driver.go:170, sqlite.go:385). Target tree untouched. Final counter-evidence sweep: who injects `Clock` in production, and who consumes `List` with cursors.All evidence gathered — three hypotheses empirically confirmed via overlay probes (target tree untouched, `git status` clean), counter-evidence checked on the rest. Final report:

---

# Review: `run-journal-fencing` — verification/operability lens

Method: read context pack, then all of `internal/runcontrol` + app wiring (`run_control.go`, `durable_operations.go`, `run_commands.go`, `web/run_handlers.go`) and every test. Ran baseline suite (green). Wrote three probe tests executed via `go test -overlay` (files in `/tmp/opencode/probes`, target repo byte-identical to frozen SHA). Every candidate below was stress-tested against callers, guards, and existing tests before reporting.

## Findings

### F1 — Reconcile batch budget lets an ownerless backlog fully starve expired-owner arbitration (empirically confirmed)
**Claim.** `Reconcile` spends its entire batch limit on unclaimed runs; when ≥ `DefaultReconcileBatch` (64) ownerless acceptances are past grace, the expired-lease scan is skipped entirely that pass (lifecycle.go:355-359: `remaining := limit - report.Scanned; if remaining == 0 { return }`). Pass 1 also counts only *wins* toward `Scanned` (lifecycle.go:345-349), so raced rows consume query budget invisibly.

**Bad outcome.** A run whose owner died stays `lifecycle=running`, `current_attempt_id` set, lease long expired, for additional reconcile cycles — while operators see `ReconciliationBacklog` (which mixes both populations into one number, sqlite.go:883-887) shrinking and cannot tell which class is lagging. With sustained ownerless arrivals (e.g., a crash loop accepting then dying pre-claim), expired-owner arbitration is delayed indefinitely.

**Trigger.** >64 unclaimed acceptances aged past grace at one scan.

**Evidence.** Probe: seeded 70 unclaimed expired acceptances + 1 claimed run with dead owner (PID gone). Pass 1: `scanned=64 terminal=64`, dead-owner run still `running`; probe would have returned `interrupted`. Verified no test covers the batch-limit interaction (context-pack gap, now shown consequential).

**Counter-evidence.** Backlog drains geometrically (≤64/pass), so delay is bounded (~backlog/64 ticks) unless arrivals sustain; arbitration itself stays conservative. No safety violation — pure liveness/observability.

**Severity:** low-medium (operability). **Confidence:** high (demonstrated).
**Regression test:** seed limit+N expired unclaimed + 1 expired dead-owner run; assert first `Reconcile` reaches the owned run within N batches or that Health splits the backlog by class.

### F2 — Per-run journal saturation fails ALL appends permanently, including reserved recovery types — violating the stated quota/liveness invariant (empirically confirmed)
**Claim.** `compactRunJournal` (retention.go:58-95) returns `CodeQuota` when a run exceeds 4096 events/16 MiB and nothing removable remains; this error aborts the whole `Append` transaction **regardless of draft type** (sqlite.go:687-689). The hard-quota pre-gate admits reserved types (sqlite.go:618-622), but they then die in compaction. The documented invariant — "hard gate … always permitting reserved recovery types" — is false at per-run capacity.

**Bad outcome.** Once a run accumulates 4096 non-removable events (warning/lifecycle/cancellation/recovery/omission), every further append — progress, finding, artifact, warning alike — fails `CodeQuota`. The app spine treats that as fatal: cancels the run context and proposes `persistence_degraded` (app/run_control.go:237-241, 322-331) — killing a *healthy* operation with a mislabeled outcome. Workspace-level `Compact` cannot help: it only processes terminal runs (retention.go:115,130), and this run is active. Only terminalization unlocks cleanup.

**Trigger.** One run reaching 4096 reserved-type events. Plausible producer: `runtimeEventDraft` maps every runtime `error`/`warning` event to `EventWarning` (app/run_control.go:421-423) — a noisy agent on a long operation emitting thousands of distinct warnings saturates the journal; `RecordOperationEvent` similarly emits warnings per failed phase. Tail-risk probability, permanent-until-terminal consequence.

**Evidence.** Probe: raw-seeded 4096 `warning` events, `last_sequence=4096`; `Append(warning|lifecycle|omission|progress)` each returned `CodeQuota: required durable event history reached its bounded capacity`. `RequestCancellation`/`ProposeTerminal` still succeeded (no compaction call) — consistent with F2 being scoped to `Append`.

**Existing controls.** Fail-stop is loud (typed error, metrics, terminal proposal succeeds), so no silent corruption; but the recovery story for the reserved-types contract does not exist.

**Severity:** medium-low. **Confidence:** high (demonstrated end-to-end except the app-spine mapping, which is code-read).
**Regression test:** saturate a journal with reserved types; assert either that reserved appends succeed (fix: skip compaction for reserved drafts, or make compaction best-effort) or that the failure mode and terminal mapping are explicitly pinned as intended.

### F3 — Injected-clock expiry predicates compare RFC3339Nano text, expiring live leases early / dead ones late (empirically confirmed; latent)
**Claim.** `expiredTimestampPredicate` (sqlite.go:1139-1144) switches to **string comparison** whenever a `Clock` is injected: `column <= ?` with `formatTime(...)`. RFC3339Nano trims trailing fractional zeros, so lexicographic order ≠ chronological order within a whole second (`'.' (0x2E) < 'Z' (0x5A)`).

**Demonstrated.** Lease written at `…T10:00:00.5Z` with 15 s lease ⇒ `lease_expires_at="2026-08-26T10:00:15.5Z"`; clock advanced to `…T10:01:00Z` ⇒ cutoff `"…T10:00:15Z"`. Numeric truth: still valid 0.5 s. Predicate result: **expired** (`expired_count=1`). The mirror case (no-fraction lease vs fractional cutoff) misses expiry by up to ~1 s.

**Reachability today.** None in production — both `OpenSQLite` call sites pass no `Clock` (verified by grep), so the julianday numeric path always runs. But `SQLiteOptions.Clock` is a public option seam, and the clock-jump test (lifecycle_test.go:233) passes *only* because every fixture time is whole-second aligned — current verification actively masks the flaw rather than covering it.

**Severity:** low (latent). **Confidence:** high (mechanism demonstrated; reachability honestly zero today).
**Regression test:** repeat the clock-jump test with sub-second offsets, or store/compare epoch integers in the injected path.

### F4 — `List` keyset pagination silently skips unseen runs under `updated_at` churn
**Claim.** The cursor predicate is strict-less on the *mutable* sort key `(updated_at, run_id)` (lifecycle.go:230). Heartbeat bumps `updated_at` every 5 s per active run (lifecycle.go:48-49); appends, claims, and cancellations do too. A not-yet-listed active run whose `updated_at` advances between pages jumps ahead of the cursor and is never returned for the rest of that pagination walk.

**Bad outcome.** An operator paging through a busy workspace (CLI `run list --after`, web runs page chaining `NextRunsURL`, run_handlers.go:306-311) never sees some currently-active runs — the exact rows most relevant during incident triage. Duplication is impossible (keys only increase), so the miss is silent.

**Trigger.** Multi-page listing while any not-yet-listed run is live. No test exercises pagination under mutation.

**Counter-evidence.** Bounded to paginated reads; single-page consumers unaffected. Semantics arguably "newest-first snapshot", but skipping ≠ reordering.

**Severity:** low-medium (observability). **Confidence:** high on mechanism (code-certain), medium on practical frequency.
**Regression test:** page size 2 over 3+ runs; bump an unseen run's `updated_at` between fetches; assert it appears on a later page (will fail today).

### F5 — Reconciliation evidence loss is silent: `_ = r.recordReconciliation(...)` drops write failures
**Claim.** Both reconciliation call sites ignore evidence-insert errors (lifecycle.go:352, 411). Decisions (including terminal outcomes like `interrupted`/`cleanup_uncertain`) commit without their `reconciliation_log` row; neither metrics nor logs nor Health record the loss. The support export then presents an incomplete audit trail as if complete.

**Bad outcome / trigger.** Under `SQLITE_FULL` (disk pressure — exactly when reconciliation fires hardest) or permission degradation, every decision in the pass loses its durable evidence; TRD §19/§20 requires reconciliation decisions be recorded. Operators diffing `run diagnostics --support-export` against actual terminal states find unexplained terminals with no reconciler trace.

**Existing controls.** The reader-side allowlist (lifecycle.go:556-565) guards content, not presence; `finishReconciliationLog` logs counters only.

**Severity:** low. **Confidence:** high (path certain).
**Regression test:** force the insert to fail (e.g., drop the table via fault injection); assert a metric/diagnostic surfaces, or that Reconcile reports evidence-write failures in `ReconcileReport`.

## Defended non-issues (hypotheses raised and refuted)

1. **`_txlock=immediate` possibly unsupported (pack unknown #4)** — refuted. modernc.org/sqlite v1.57.0 parses it (sqlite.go:385-388, driver.go:170) and errors on invalid values; `_defensive` likewise supported. Additionally, `TestSQLiteRepositoryConcurrentWritersAllocateMonotonicSequence` (2 repos × 20 goroutines, zero tolerated errors) and the real-subprocess test would fail if write serialization broke — the pack's "unverified" framing understates existing indirect pinning.
2. **Zombie-owner mutation after lease expiry** — safe. `verifyFence` ignores expiry, but each mutation carries an independent guard: Append (terminal check + `last_sequence` CAS), Heartbeat (`outcome IS NULL` + runs CAS), ProposeTerminal (`terminal_outcome IS NULL` CAS). Claim-once means generation fencing never arbitrates two live writers; the reconciler reusing the dead attempt's fence (pack unknown #7) is sound *because* ownership is immutable.
3. **Sequence allocation races** — safe: immediate-tx serialization + CAS `RowsAffected` checks, pinned in-process and cross-process.
4. **Cancellation↔terminal races, completion-after-cancel freezing `cancellation_state='requested'`** — single-winner CAS holds; test-pinned.
5. **Reconcile decision matrix** — conservative as specified (stall never terminalizes, success never inferred, probe failure → uncertain); pinned including real subprocess exit and idempotent repeats.
6. **Quota/fault semantics** — soft/hard gates and no-stale-success guarantees hold under SQLITE_FULL, `query_only`, and closed pools (fault_test.go).
7. **Pack unknowns #5, #11, #6** — upgraded by this review from "unstated/untested" to confirmed behaviors F1, F2, F5 respectively.

No target or workspace files were modified; probe artifacts live in `/tmp/opencode/probes/`.