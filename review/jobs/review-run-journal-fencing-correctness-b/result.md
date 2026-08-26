The context pack gives me a solid map. Now I'll prove the highest-value hypothesis with a reproduction test (via `go build -overlay` so the target tree stays untouched): a clean user cancellation with one pending coalesced progress event should terminate as `cancelled`, but I predict it records `persistence_degraded`.Finding 1 confirmed by execution. Now let me verify two more hypotheses the same way: (a) a backward wall-clock step making durable snapshots permanently invalid, and (b) journal saturation with non-removable event types defeating the "reserved recovery writes always permitted" promise.The saturation test tripped even earlier than predicted (append #522 itself failed) — let me pin the exact reserved-type behavior just below the boundary.The boundary was razor-thin. Let me push clearly past it with larger per-event payloads, then probe the reserved write.All three mechanisms confirmed by execution. Let me verify the target tree is untouched and do final counter-evidence checks on the remaining candidates.All verification complete; target tree untouched (`git status` clean, reproduction ran via build overlay from `/tmp/opencode`). Final report:

---

# Findings: `run-journal-fencing` — independent correctness review

## F1 — Clean cancellations and deadlines are durably recorded as `persistence_degraded` when coalesced progress is pending
**Severity: high (durability-core truthfulness) · Confidence: high · Confirmed by executed reproduction**

- **Claim:** `controlledRuntime.StartRun` flushes trailing coalesced omissions using the operation context (`internal/app/run_control.go:305`, `appendRunEventWithRetry(runCtx, …)`). When `StartRun` returned *because* that context was cancelled — user/operator/shutdown cancellation acknowledged by the control loop (`run_control.go:271-278`) or parent deadline — the flush always fails with `context.Canceled`, which seeds `persistenceErr`. The function then takes the persistence-failure branch (`run_control.go:322-331`) and proposes `TerminalPersistenceLost` ("durable event persistence failed") on a detached context. That proposal wins terminal arbitration legitimately (fence still authoritative), so the **wrong outcome is committed immutably**, and the pending omission count is silently dropped.
- **Reproduction (overlay test, real SQLite):** runtime emits two identical `tool_call_update` progress events (second coalesces), then blocks; `RequestCancellation("user_requested")` succeeds; control loop acknowledges (events seq 2, 3 = cancellation/ack). Resulting journal: `terminal outcome="persistence_degraded" reason="durable event persistence failed"`; `StartRun` returns `persist progress omission: context canceled` instead of the cancellation. Expected: `cancelled`.
- **Trigger/preconditions:** ≥1 identical progress payload coalesced within `ProgressCoalesceWindow=250 ms` and not yet flushed when cancellation/deadline lands. Chatty tool-observation streams (the reason the coalescer exists, comment at `run_control.go:215-216`) make this routine.
- **Counter-evidence searched:** `FinishOperation` in `internal/app/durable_operations.go:78` deliberately uses a fresh 30 s background context for exactly this flush — the asymmetry confirms intent and isolates the defect to the runtime path. No test covers cancellation with pending omissions (`grep` over `internal/app/*_test.go`: zero coalescing tests). TRD §18C requires ONE arbitrated terminal result reflecting truth; §20.1 requires stale writers fenced — here the writer isn't stale, the *context choice* is wrong.
- **Fix/regression:** flush via detached context (mirror `FinishOperation`), or treat flush `context.Canceled` as skip-and-continue to normal outcome mapping. The reproduction test above is the regression test.

## F2 — Journal saturation permanently rejects ALL further appends, including reserved recovery types
**Severity: medium-high · Confidence: high · Confirmed by executed reproduction**

- **Claim:** `compactRunJournal` (`retention.go:58-95`) runs inside every `Append` transaction once a run exceeds 4096 events or 16 MiB retained payload. If nothing is removable (inline compaction only deletes `progress/message/omission`), it returns `CodeQuota` **after** the insert+CAS, rolling back the new event — including reserved types (`warning/lifecycle/cancellation/recovery/omission`) that the hard-quota gate explicitly exempts (`sqlite.go:620-621`, `713-720`). The failure is permanent: nothing ever becomes removable again.
- **Reproduction:** 255 `finding` events × ~64 KiB payloads saturate the journal; the next append of any type fails `event_limit: required durable event history reached its bounded capacity`; run remains `running`; only `RequestCancellation`/`ProposeTerminal` (which don't compact) still succeed.
- **Production path:** `runtimeEventDraft` maps agentwrap `finding`/`artifact` events to these non-removable types (`run_control.go:424-427`); QA-heavy studies can plausibly exceed 16 MiB. First failed append triggers the spine's fail-fast cancel (`run_control.go:237-240`) → operation dies mid-work. Even tombstone compaction never removes `artifact`/`finding` (`retention.go:185-188`), so no automatic recovery exists short of run deletion after full+tombstone aging.
- **Contract broken:** "quota/liveness coupling … always permitting reserved recovery writes." The gate honors it; the compactor defeats it. This elevates context-pack unknown #11 to a demonstrated defect.
- **Fix/regression:** exempt reserved types from the compaction-failure path (e.g., skip `compactRunJournal` for reserved drafts, or evict findings/artifacts under pressure), plus the saturation regression test above.

## F3 — A backward wall-clock step makes recent runs durably unreadable and strands live operations
**Severity: medium · Confidence: high mechanism (executed), medium trigger likelihood**

- **Claim:** `Snapshot.Validate` requires `UpdatedAt >= AcceptedAt` (`model.go:346-348`). Every mutation stamps `updated_at = r.now()` (real wall clock in production, `sqlite.go:1124`). After any backward clock step Δ (NTP step, VM resume), mutations on runs accepted within Δ commit fine (no time guard in any CAS), producing rows that **fail validation on every read, forever until the clock re-passes `accepted_at`**.
- **Reproduction:** accept at 12:00:00; step clock back 1 minute; `RequestCancellation` commits; subsequent `Snapshot` → `validate: snapshot.timestamps must start at acceptance and move forward`.
- **Cascade:** control loops poll `Snapshot` every 1 s → first failure calls `setPersistenceErr` → operation cancelled (`run_control.go:263-269`); `ProposeTerminal` also loads+validates → winner proposal fails → run stranded active; after lease expiry the reconciler probes the *live* owner → exact-match → `markStalled` forever (stall never terminalizes by design) → permanent manual-intervention state. The package pins clock-jump safety for leases (`TestReconcileClockJumpNeverExpiresAnOwnerEarly`) but nothing defends snapshot monotonicity against real steps.
- **Fix/regression:** clamp comparisons rather than reject (e.g., tolerate `UpdatedAt` within skew of `AcceptedAt`, or stamp `updated_at = max(now, accepted_at)`), with a step-simulation test.

## F4 — Zombie owner processes are classified "live" forever; provably-dead owners never reach `interrupted`
**Severity: low-medium · Confidence: high mechanism, medium prevalence**

- `probeNativeProcess` (linux, `process_linux.go:16-45`) extracts only starttime from `/proc/<pid>/stat`; the state field (`Z`) is ignored. Darwin's `kinfo_proc.P_stat` is equally unchecked (`process_darwin.go`). A zombie retains its original PID+starttime, so `reconcileProcessDecision` sees an exact birth match and only marks stalled (`lifecycle.go:491-495`) — every cycle, indefinitely. A zombie is *certainly* dead (terminated, unreaped); the conservative matrix exists to avoid inferring death wrongly, but here death is provable and still not acted on. Backlog/StalledRuns never drain; combined with F5 this churns reconciliation evidence forever. Common trigger: containers/supervisors with unreaping PID-1.
- Regression: extend the decision-matrix table with a zombie probe case once probes surface process state.

## F5 — `reconciliation_log` grows without bound for stuck runs; stall churn also corrupts list ordering
**Severity: low-medium · Confidence: high**

- Every reconcile cycle records an evidence row per candidate (`lifecycle.go:411`), and *every* process with an active operation reconciles every 10 s (`run_control.go:289-298`, `durable_operations.go:255-261`). For a long-lived stalled run (the designed non-terminal outcome), that's N_processes rows/10s indefinitely; there is no pruning anywhere (grep: only CASCADE via run deletion, which never arrives for alive-but-stalled owners — see F3/F4). `markStalled` additionally bumps `runs.updated_at` each cycle (`lifecycle.go:498-501`), pinning such runs to the top of the `updated_at DESC` keyset ordering used by `run list` (`lifecycle.go:230-233`) and invalidating clients' pagination cursors continuously.

## F6 — One vanished row aborts an entire `List` page
**Severity: low · Confidence: high**

- `List` re-reads each page row through `Snapshot` (`lifecycle.go:258-264`); a run deleted by concurrent retention produces `ErrNotFound` for the whole query instead of skipping the row. Transient CLI/web listing failure during retention windows.

## Defended non-issues (investigated, counter-evidence found)
- **Deferred-tx sequence/claim races:** all allocations are CAS-guarded (`last_sequence=?`, `current_attempt_id IS NULL`, `terminal_outcome IS NULL`); safe even if `_txlock=immediate` were ineffective — and it is effective (verified in modernc.org/sqlite@v1.57.0 module source, DSN parsing at `sqlite.go:385`).
- **Cancellation/terminal race:** single-winner CAS pinned by tests; request-after-terminal is idempotent read-commit.
- **Probe error → `cleanup_uncertain` on possibly-live owners:** conservative-by-contract (`interfaces.go:17-20`) and test-pinned; not a defect.
- **Hard-quota heartbeat refusal stopping healthy owners:** documented design; terminal arbitration remains ungated, so runs cannot strand.
- **`julianday` vs RFC3339Nano strings:** SQLite accepts the ISO-8601 `T` separator and fractional seconds; production expiry math is sound (injected-clock string-compare skew is sub-second and test-only).
- **Sanitization chain:** producer denylist + storage allowlist with omission accounting behave as documented (tests confirm hostile-field drops).

**Verification status:** baseline untouched (`git status` clean; all reproduction ran through `go test -overlay` from `/tmp/opencode`). F1, F2, F3 proven by execution; F4–F6 proven by source/path analysis with exhaustive counter-evidence search.