Context pack read; target working tree matches the frozen commit for the reviewed paths. Now reading primary sources.Now the TUI, CLI run commands, and run-control lifecycle.All primary paths inspected; hypotheses tested against callers, guards, and tests. Findings below, strongest first.

---

## F1 — TUI study-cancel accepts a *stale, unrelated* Confirmation into the durable spine (or none at all)

**Claim.** `ActionCancel`'s study branch builds a fresh `OperationStudyCancel` request but `beginOperation` gates durable acceptance on whatever `m.model.Confirmation` happens to hold, not on the request being run (`internal/tui/app.go:139` → `app.go:235-255`). `Confirmation` survives tab switches and forward navigation (`model.go` — only `ActionBack`, `OperationMsg`, and `beginOperation` clear it), so it is routinely stale when `c` is pressed.

**Observable bad outcomes.**
1. *False durable record + poisoned alias:* user prepares operation A (e.g. "Run Flow to plan"), navigates to a study with an active run, presses `c`. `AcceptOperation(opctx, *staleConfirmation, digest)` creates a run-control row with `Target.Operation = sprint-flow` (A's kind/scope) plus an alias row for A's content digest, while the code actually SIGINTs the study loop; `FinishOperation` then records that flow run "completed" (`durable_operations.go:99-105, 286-299`). Any later genuine TUI confirm of A hits `Existing` and is refused with "matching durable operation already exists" (`app.go:245-250`) until retention evicts the row — `operation_aliases` rows are never deleted (grep of `internal/runcontrol/sqlite.go`: insert at :449, no delete anywhere).
2. *Vocabulary bypass:* with `Confirmation == nil` the same keystroke skips `AcceptOperation` entirely — a mutating interactive action lands no durable row, contrary to user-guide.md:376-377 ("Every … confirmed web/TUI operation … receives a durable run before work starts") and to the web door, where `study-cancel` is fully prepared/confirmed/accepted (`operation_handlers.go` → `mapOperationRequest` :716-717, `PrepareOperation` :328-331).

**Trigger/preconditions.** TUI composition always satisfies `DurableOperationManager` (`tui_commands.go:52`); reachability is the advertised flow "view active study run, press c".

**Counter-evidence searched.** `setTab`, `LoadMsg`/`RefreshMsg`, `ActionOpen` route pushes — none clear `Confirmation`; the other two `beginOperation` callers (:104, :155) pass `Confirmation.Request`, so only this site breaks the pairing invariant.

**Severity:** high (durable-state falsification + blocking of legitimate operations). **Confidence:** high (static path unambiguous; no test covers `ActionCancel` × stale `Confirmation`).
**Regression test:** drive `teaModel.Update` with a planted `Confirmation` for kind X, fire `ActionCancel` on a study route with a fake manager recording accepted requests; assert accepted `Request.Kind == study-cancel` (or that acceptance is skipped), and that `ActiveOperation` equals the accepted request.

## F2 — TUI content-digest regime permanently forbids re-running identical operations, including state-dependent status refreshes

**Claim.** TUI dedup key = sha256(CanonicalRequest + "\x00" + InputFingerprint) (`app.go:236-238`) is inserted into an append-only table and resolved forever (`sqlite.go:449`, `ResolveOperationAlias` :472-484, no eviction). Identical confirmed content therefore maps to `Existing` for the lifetime of the database — including after the original run reached a terminal state.

**Observable bad outcome.** Run "Sprint Status" from the TUI; press Enter again on the re-prepared identical confirmation: `AcceptOperation` returns `Existing`, the TUI prints "matching durable operation already exists" and performs **no work** (`app.go:245-250`). Yet the status result legitimately changes because its outputs depend on `.run-state.json`/flow-state/smoke-review facts that `governedOperationInputs` does **not** hash for non-QA kinds (`operations.go:663-686` — `.run-state.json` added only for `qa-*`), so the operator is served a silently frozen answer. Same block applies to re-running flows/QA dry-runs with unchanged governed inputs.

**Cross-door parity break.** Web binds identity to sha256(session+token) (`security.go:448-451`): every fresh prepare issues a new token, so identical content can be started unlimited times; CLI passes `""` (`durable_operations.go:55`): no dedup at all. One logical action, three incompatible durable identities landing in the same `confirmation_digest`/alias column — precisely the assignment's trust boundary, violated in the direction of "door determines whether you may act at all".

**Counter-evidence searched.** `TestDurableOperationDeduplicatesAcrossManagersAndFailsClosed` pins the mechanism, not the product semantics; no doc states identical content must be permanently un-re-runnable; web contract scopes replay to "the same accepted session/token" (local-web.md:137-138), i.e. in-flight only.

**Severity:** high (routine operations silently become unavailable on one of three doors). **Confidence:** high.
**Regression test:** app-level — accept+finish an operation with digest D, re-accept identical confirmation, assert a fresh run is created (or an explicit typed refusal with remediation); plus a TUI test that "Sprint Status" executes twice consecutively.

## F3 — Browser hub cancellation never persists `RequestCancellation`; cancellation state diverges across surfaces

**Claim.** For hub-owned durable operations, `cancelOperation` only flips the in-memory doc and cancels the context (`operations.go:347-374`); nothing calls `RequestCancellation` (grep: callers are CLI `run_commands.go:216`, TUI `app.go:110/124`, web *fallback* `operation_handlers.go:209/392`, `CancelQA` `sprint_usecases.go:993`). Terminal proposal then writes `terminal_reason="operation cancelled"` with `cancellation_state=none` and no cancellation/acknowledgement events in the journal (`lifecycle.go:70-134` untouched).

**Observable bad outcomes.**
- During the unwind window, `/api/v1/runs/{id}`, `run show`, `run follow`, and the TUI Runs view show `lifecycle=running, Cancellation: none` while `/api/v1/operations/{id}` shows `cancelling/user_request` — contradicting user-guide.md:377-380 ("The same ID, lifecycle, liveness, **cancellation state** … are shown on every surface").
- If the process dies in the window, the operator's cancel intent vanishes: startup reconcile marks the row `interrupted`/`cleanup_uncertain` (`lifecycle.go:340-412`) with no cancellation record or reason anywhere durable. Every other door survives that crash with `cancelling + reason` persisted first.

**Counter-evidence searched.** Manager control loop only *reads* `CancellationRequested` (`durable_operations.go:240-247`); drain intentionally skips it too, but drain has its own cleanup-uncertain protocol whereas interactive DELETE does not. Tests assert only hub-doc states (`operations_test.go:144-158`), no durable assertion.

**Severity:** medium. **Confidence:** high.
**Regression test:** full-stack HTTP test with real repository: start durable op, `DELETE /api/v1/operations/{id}`, assert within a tick `runs.Snapshot().Cancellation.State == requested` (fails today).

## F4 — TUI run-detail replay window never advances; runs with >200 events show only their oldest page forever

**Claim.** Every load/refresh/1 Hz tick fetches `RunEvents(ctx, id, OldestRetainedSequence−1, 200)` (`app.go:372-388`); `Events` returns ascending `sequence > after LIMIT n` (`sqlite.go:809-821`). The cursor is never advanced to the last seen sequence, so for any run exceeding 200 retained events (chatty flow/QA/study operations do within minutes), the rendered "Retained events" list (`views.go:335-342`) is permanently pinned to the oldest ≤200 events; progress, warnings, findings, artifacts, `cancellation`, and `terminal` events never appear while the header's `last=N` keeps growing.

**Counter-evidence searched.** `run follow` advances `after = event.Sequence` (`run_commands.go:174`); web SSE fallback advances identically (`operation_handlers.go:491`) — the triplicated logic diverged exactly here. `dedupeDurableEvents` keeps the tail *of what was fetched*, which is always the same head window. `run_view_test.go` fakes ignore the `after` argument, so tests cannot catch this.

**Severity:** medium (operator console blind to live durable state for long operations). **Confidence:** high.
**Regression test:** fake `RunEvents` capturing `after` across two refresh ticks with LastSequence > 200; assert the second call's `after` equals the previously highest returned sequence.

## F5 — Operations-door cancel fallback accepts any run-control target; active-operations merge exposes and permits cancelling other sessions' operations

**Claim.** `handleOperationCancel`/`handleHTMLOperationCancel` fall back to `CancelRun` with **no** `Target.Kind == "operation"` filter (`operation_handlers.go:206-228, 381-409`), while status/events fallbacks enforce it (:416, :452). Since `controlledRuntime.StartRun` persists rows with kinds `sprint`/`study`/`runtime` (`run_control.go:392-415`), `DELETE /api/v1/operations/{child-run-id}` cancels a non-operation run through the wrong vocabulary door — asymmetric with the same resource's GET. Additionally, `handleActiveOperations` merges *all* workspace operation-kind rows into any session's collection (:185-204), and the durable fallback then lets that session stream (`followDurableOperationEvents`) and cancel them — contradicting local-web.md:139-142 ("returns the current browser session's active operations … never exposes another session's operations").

**Counter-evidence searched.** Loopback threat-model disclaimer (local-web.md:111-115) bounds the security impact, but the sentence at :142 is a CURRENT-CONTRACT statement about this endpoint's own response, independent of attacker model; the merge appears motivated by post-restart recovery, which the doc separately promises without licensing cross-session exposure.

**Severity:** low (contract inconsistency + misleading control surface, single-user local context). **Confidence:** high on mechanics, medium on intent.
**Regression test:** DELETE a seeded `Target.Kind="sprint"` run id via `/api/v1/operations/{id}` and assert rejection; two-session test asserting session B's durable op is absent from A's active list.

## F6 — readOnly split: browsing the "read-only terminal dashboard" rewrites flow-state.json; help texts contradict

**Claim.** serve forces `readOnly:true` in both compositions (`serve_commands.go:59`, `web_usecases.go:429`); TUI leaves it false (`tui_commands.go:41-46`), so `Dashboard → SprintSummaries → service.Status` runs with status writes enabled and persists `SaveFlowState` (`sprint_usecases.go:482`, `service.go:291-294`) merely from opening the dashboard. Top-level help promises "Open a read-only terminal dashboard." (`app.go:268`) while `tuiHelp` discloses "Refresh and sprint status may recompute deterministic sprint flow-state.json status" (`tui_commands.go:88-90`); the contradiction is reconciled nowhere.

**Bad outcome.** Workspace mutation (file content/mtime, dirty-tree noise) from an action documented as read-only; surfers who read the main help get a behavior the disclosure they were never shown permits. `RefreshMayWrite` projection exists but only inside sprint summaries.

**Severity:** low-medium (documented-behavior violation with real side effect). **Confidence:** high.
**Verification:** open TUI against a fixture workspace, checksum `flow-state.json` before/after dashboard load with a deliberately stale status subrecord.

---

### Defended non-issues
- `hub.start` (empty dedupKey): test-only; production uses `startConfirmed` exclusively.
- `startConfirmed` keeping the pre-accept `cancel` on the record: safe — `accepted.Context` is a child of that parent, propagation intact (`durable_operations.go:130`).
- Event-persistence failure mid-operation: `RecordOperationEvent` fails fast and cancels the owned context itself (`durable_operations.go:213-215`); TUI drop-on-error is backstopped.
- Transport vs app QA lockdown and identifier admission (`validIdentifier` excludes `.`/`..`): consistent layering.
- Hub `finish` missing the `timed_out` mapping vs `FinishOperation`: unreachable today — no deadline exists in the hub record-context chain.
- Reason-spelling divergence (`user_request` vs `user_requested` vs prose): no consumer filters/matches reasons; display-only today.
- Preparation-store consume-on-mismatch: deliberate anti-brute-force, correctly ordered under hub lock.