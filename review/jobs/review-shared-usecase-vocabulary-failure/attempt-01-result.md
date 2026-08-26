No Go source differs between the frozen commit and HEAD — the working tree is reviewable directly. Starting with the core vocabulary files.Core files read. Now the web hub, preparation store, and handlers.Hub read — several candidates noted. Now the preparation store, handlers, and TUI operation lifecycle.Hub and handlers reviewed. Now the TUI operation lifecycle and the shared runner.TUI reviewed. Now the runner, run use cases, and composition roots.Now the run-control vocabulary side: cancellation, aliases, constants.`RunQA`/`ResumeQA` on webUseCases appear uncalled by handlers — verifying, then checking the contract tests and how the hub treats durable cancellation.Hub-cancel tests use fakes only — no durable-parity coverage. Now checking authoritative contracts for cancellation/dedup semantics to test these candidates.The shutdown contract is marked "Proposed" — noting status. Now checking user-guide parity claims and any documented TUI dedup semantics.Final verification round: run-ID format (for the `op_` legacy prefix interaction) and the run_handlers SSE cursor handling.One more check — how the TUI stores Confirmations (whether read-only repeats also hit the alias path):Review complete. All paths traced against the frozen tree (Go source identical between `5d54525` and HEAD). Here are the results.

---

# Findings — `shared-usecase-vocabulary` (failure/concurrency lens)

## F1 — Web-door cancellation of durable operations bypasses the durable cancellation protocol entirely (no request, no reason, no acknowledgement persisted)

**Claim.** When a browser cancels an operation that was durably accepted (`DELETE /api/v1/operations/{id}`, HTML equivalent, or server-shutdown drain), the hub only cancels the in-process context. The run-control row never enters `cancelling`, never records `cancellation_state/reason`, never emits a `cancellation` event, and is finished directly via `ProposeTerminal(TerminalCancelled, "operation cancelled")`. The TUI and CLI doors persist the full protocol (request → acknowledgement → terminal).

**Observable bad outcomes.**
- After a web cancel, `ultraplan run show/follow/list`, the TUI Runs view, and the durable SSE fallback show `cancellation_state=none`, empty `cancellation_reason`, and a journal that jumps `running → terminal(cancelled)` with generic reason `"operation cancelled"` — while the same action cancelled from TUI/CLI shows `requested → acknowledged`, lifecycle `cancelling`, and a persisted reason. user-guide.md:374–380 promises "the same ID, lifecycle, … cancellation state … shown on every surface".
- Operator intent is destroyed by a crash mid-cancel: if the process dies after `record.cancel()` but before `FinishOperation`, reconciliation marks the run `interrupted` with no record that cancellation was requested; a restarted operator cannot distinguish "user wanted this dead" from "server died". Via TUI/CLI the persisted `cancellation_requested` survives the crash.
- Shutdown drain (reason `server_shutdown`) leaves the same empty trace; docs/plans/server-shutdown-run-cancellation-contract.md §3.2/§3.5 requires persisting `cancellation_requested` + `reason: server_shutdown` "where durable operation state exists" and forbids representing shutdown cancellation without the shutdown reason (status "Proposed", so cited as supporting, not primary).
- Cross-session visibility: `handleActiveOperations` merges run-control lifecycles including `cancelling`; hub-cancelled ops skip that state, so the merged view disagrees with what a TUI/CLI-cancelled op would show.

**Trigger/preconditions.** Any operation started through the web hub with `DurableOperationManager` present (the serve composition always provides one), cancelled by its owner session or by drain.

**Execution path.** operation_handlers.go:207/:390 → operations.go:347–374 `cancelOperation` (sets in-memory doc state, `cancelOnce` → local `cancel()` only) → accepted.Context (child of that ctx, durable_operations.go:130) cancelled → controlOperation exits at durable_operations.go:230–231 without `AcknowledgeCancellation` (nothing to acknowledge) → operations.go:234–241 `h.run` → `FinishOperation` → durable_operations.go:286–298 → `ProposeTerminal` → sqlite.go:769–776 updates only terminal columns; cancellation columns untouched (verified in ProposeTerminal body).

**Counter-evidence searched.**
- No second path persists the request: the `CancelRun` fallback in operation_handlers.go:209/:392 fires only when the hub lookup *errors*, i.e., for foreign/non-hub runs — hub-resident durable ops take the shortcut.
- Existing tests cannot catch it: `TestOperationHubLifecycleCancellationAndSessionOwnership` and drain tests use `fakeWebOperations` with no `DurableOperationManager`; `durable_operations_test.go` covers accept/event/finish but never context-cancellation finishing. No cross-door parity test exists (context-pack unknown #1 confirmed).
- Defensible design reading ("durable truth = terminal outcome, transient cancel detail is hub-only") was checked against contracts and rejected: the repository deliberately implements request/acknowledge CAS semantics (lifecycle.go:70–194) used by every other door, and the terminal reason loses the operator's reason (`user_request` vs `server_shutdown` are indistinguishable — both become `"operation cancelled"`).

**Severity / confidence.** Medium / high (mechanism fully traced; contract basis current-tree user-guide + local-web.md:145–146).

**Verification/regression.** Hub test wired to a real `runcontrol.OpenSQLite` repository + `newDurableOperationManager`: start via `startConfirmed`, cancel via `cancelOperation`, wait terminal, assert `snapshot.Cancellation.State == CancellationAcknowledged` and a `cancellation` event with the canonical reason exists. Today it asserts `none`/absent — demonstrating the defect; post-fix it pins parity with tui/app.go:110 and run_commands.go:216.

---

## F2 — TUI content-digest aliases make identical re-confirmation impossible for at least the retention window (~37 days default), while web repeats freely and CLI never dedups

**Claim.** The TUI derives the dedup digest purely from confirmed content (`sha256(CanonicalRequest + "\x00" + InputFingerprint)`). For operations whose hashed inputs don't change between attempts — notably `sprint-status`, whose own flow-state.json rewrite is *not* a governed input — every repeat produces the same digest, hits the alias conflict, and is refused. Alias rows live as long as the run row: deletion happens only via `ON DELETE CASCADE` at `FullHistory + TombstoneHistory` past finish (7d + 30d defaults), and tombstoning keeps the row. There is no override, force, or expiry on the TUI path.

**Observable bad outcome.** Operator confirms `sprint-status` in the TUI (it mutates flow-state.json status), later wants a refreshed status after execute-state changed underneath (execute state files aren't hashed for sprint-status), presses confirm again → `Existing=true` → "matching durable operation already exists" pointing at the stale completed run, forever (weeks). Same for repeated `validate`, `qa-dry-run`, `qa-recover` etc. — every TUI confirmation stores a Confirmation (model.go:213–222, unconditioned on `Mutates`) and routes through the digest. The identical re-run succeeds with one click on the web door (fresh random token per prepare → fresh digest → new alias) and always creates a new run via CLI (empty digest, durable_operations.go:55). Three doors, three different duplicate semantics for the same logical action — the exact trust-boundary invariant this surface owns.

**Trigger/preconditions.** Any second TUI confirmation whose canonical request + governed-input fingerprint are unchanged, within ~37+ days of the first attempt (config-dependent).

**Exact evidence.** internal/tui/app.go:235–254 (digest formula, Existing refusal at :245–249); internal/app/operations.go:663–697 (QA-only additions of flow-state.json/.run-state.json — excluded for `sprint-status`); internal/runcontrol/sqlite.go:445–455 (alias insert, no expiry column populated), :472–484 (unconditional resolve), :306–314 (CASCADE only); internal/runcontrol/retention.go:129–150 (row deletion cutoff = FullHistory+TombstoneHistory); model.go:423–424 (defaults 7d/30d).

**Counter-evidence searched.**
- `TestDurableOperationDeduplicatesAcrossManagersAndFailsClosed` proves cross-manager dedup is *intended*; it pins the mechanism, not indefinite blocking of legitimate repeats. Nothing in docs states TUI repeats should be blocked; local-web.md:137–138 documents replay semantics only for the token-based web door, where repetition remains possible by design.
- Considered "fingerprint usually changes": false for `sprint-status` (its write target isn't hashed), for read-only kinds, and for any retry where planning docs are stable.
- Considered escape hatches: none — re-preparing yields the same Confirmation; OperationMsg clears Confirmation; no force flag exists in the TUI.

**Severity / confidence.** Medium-low / high on mechanism, medium on defect-vs-intent (dedup intended; permanent block with no override and cross-door asymmetry is the defect).

**Verification/regression.** Test at app level: accept digest D, finish successfully, advance a fake clock past `PreparationTTL` and well past any interactive-plausible interval but before retention expiry, accept D again → currently returns `Existing`; assert instead that a terminal predecessor's alias either expires on a short bounded TTL or that resolution ignores terminal predecessors for non-idempotent kinds. Alternatively pin the product decision with a documented TTL on `operation_aliases.expires_at` (column already exists, never written).

---

## F3 — Triplicated progress-coalescing logic has already drifted: the interactive-operation implementation drops distinct completion counts and labels them "equivalent"

**Claim.** `durableOperationManager.RecordOperationEvent` coalesces consecutive `progress` events within `ProgressCoalesceWindow` (250 ms) using a key that excludes `Completed`/`Total`/attempt/token fields, while the runtime-child implementation (`runtimeEventDraft` + `payloadHash` in controlledRuntime.StartRun) hashes the full sanitized payload — including counters — and therefore never collapses count-progressions. A third variant lives in web hub projection conventions. Only the interactive variant silently discards real progress distinctions.

**Observable bad outcome.** Fast-moving operations (study run-loop task completions, review worker counts) that emit two progress events differing only in `Completed` within 250 ms get the second dropped; the eventual omission record asserts `"equivalent progress coalesced"` for events that were not equivalent. `run follow` / durable run pages under-report progression granularity for TUI/web-started operations relative to runtime-child journals of the same logical work — observable when comparing an execute-start operation journal with its nested controlled-runtime child journal.

**Evidence.** internal/app/durable_operations.go:167–181 (key built from Stage/Task/PhaseState/SafeSummary/EventType/EventKind/Tool/Reason/Detail only; `count` written into payload at :184 but absent from the key), vs internal/app/run_control.go:215–227 (content-aware hash of full payload; comment explicitly says "tool calls have distinct payload hashes and will not coalesce").

**Counter-evidence.** Bounded harm: window is 250 ms; flush carries the newest event's counters plus an omission count, so totals eventually converge; work is unaffected. That bounds it to observability drift rather than correctness of execution — hence Low.

**Severity / confidence.** Low / high (divergence verified line-by-line; impact modest).

**Verification/regression.** Unit test: emit two `RecordOperationEvent` calls with identical summary but `Completed: 1` then `Completed: 2` inside the window; assert two durable events (post-fix) or at minimum an omission reason that doesn't claim equivalence. Mirrors the existing coalescing tests on the runtime path.

---

## F4 — Synchronous durable acceptance on interactive threads: hub accepts under the global hub mutex, TUI accepts on the UI event loop

**Claim.** `startConfirmed` holds `h.mu` across `manager.AcceptOperation`, which performs `repository.Accept` (including an automatic `Compact(ctx, 64)` once storage ≥ 80 % of hard quota, sqlite.go:384–390), `Claim`, and the lifecycle-running append. During that window every hub operation — status polls, SSE subscribes/replays, cancellations, active-operation lists — blocks behind SQLite work and possible multi-run compaction. Symmetrically, the TUI calls `AcceptOperation` synchronously inside `Update` (tui/app.go:232–268), freezing the whole terminal UI for the same DB work.

**Observable bad outcome.** Under quota pressure (the exact condition that triggers in-Accept compaction), a browser start stalls every live SSE stream's subscribe/cancel path and can delay cancellation requests for unrelated operations; a TUI confirm visibly hangs without feedback. Both are bounded (local SQLite, ≤64-run compaction batches) but occur precisely in the degraded regime where operators are most likely to be watching/cancelling.

**Evidence.** internal/web/operations.go:150–218 (`h.mu.Lock()` at :151; `confirm()`/`AcceptOperation` at :171–182; deferred unlock), sqlite.go:384–395 (compaction inside Accept); internal/tui/app.go:232–255 (accept inline in `Update`), bubbletea Update executing on the program's event loop.

**Counter-evidence.** Expensive prepare work (smoke preflight, full input hashing) correctly happens outside the lock (handlers call `PrepareOperation` before `startConfirmed`); capacity/draining checks stay consistent because everything is serialized; typical-case latency is small. Reported as Low because the trigger requires quota pressure, but the coupling of a global presentation lock to storage compaction is a concrete liveness hazard, not style.

**Severity / confidence.** Low / medium-high.

**Verification/regression.** Test injecting a slow `Repository` (or forcing the 80 %-quota branch with a tiny `HardQuotaBytes`): assert `status`/`subscribe` calls complete while a start is in flight (post-fix moves accept out of the critical section or off the loop), and a TUI-level test that confirm renders a pending state instead of blocking `Update`.

---

## Defended / non-issues (investigated, counter-evidence found)

- **Event-persistence failure handling is consistent across doors.** `RecordOperationEvent` append failure cancels the owned context from inside the manager (durable_operations.go:213–215); TUI's apparent "ignore and continue" (tui/app.go:288–291) is backstopped by the manager-owned cancel, and the web hub's explicit `record.cancel()` converges to the same cancelled-terminal outcome.
- **SSE exposure of raw tool fields is pre-sanitized.** `ToolArguments/ToolResult/ToolError` reach `publishAppEvent` only after app-layer `captureToolObservation` applies `redactObservableValue` + `boundedPayloadValue` (run_control.go:636–653, 686–728); the durable payload receives the same normalized values, so live and durable projections agree.
- **Crash between Accept and Claim cannot strand a run.** `Reconcile` terminates unclaimed runs as `interrupted` after `ReconciliationGrace` (45 s) with CAS protection (lifecycle.go:320–354, 427–479).
- **Double-start via concurrent replays is impossible.** Token consumption and dedup-map/alias insertion are serialized under `h.mu`, with the durable alias unique constraint as the cross-process backstop; the `Existing` early-return path is effectively unreachable in production web flows (per-process session secrets + 2-min token TTL), so its quirks are latent, not live.
- **`op_` legacy prefix cannot shadow real runs** — durable IDs are `run_…` opaque base32 (id.go:37–43), and `legacyOperationID` gating only affects 410 messaging.
- **Drain/shutdown terminal races are guarded.** `terminalOperationState` checks in `finish`/`markCleanupUncertain` plus `cancelOnce` prevent double-close and double-decrement; cleanup-uncertainty is persisted to product storage before the hub projects the terminal (operations.go:497–500), matching the shutdown contract's ordering requirement.
- **Durable-fallback SSE reconnect gaps are handled client-side**: the browser distinguishes `replay_gap`/`cursor_ahead` errors from synthesized `recovery_required` frames (app.js:1665, sse.js:3), so hub-vs-durable reconnect contract differences are consumed intentionally.
- **Cross-session cancellation of durable runs via the fallback** is consistent with the product's single-operator loopback trust model (CLI `run cancel` has no ownership check either); not reported as a vulnerability.
- **`webUseCases.RunQA/ResumeQA` are production-dead**; if ever wired, they fail closed (`qaOwnershipFromContext` errors without durable ownership in the HTTP ctx) rather than starting unfenced QA work.