Frozen commit is an ancestor of HEAD and the only intervening commit touches nothing under `internal/`, so the working tree matches the frozen target for this surface. Now reading the primary sources.Context pack read. Now inspecting primary sources directly. Starting with the durable operations manager, shared runner, and the contract tests.Two hypotheses already: (1) smoke confirmation never passes `req.Timeout` into the preview while the runner honors it; (2) `AcceptOperation` derives the execution context from the caller's ctx — need to check what ctx each door passes. Reading the web hub next.The hub detaches from request context via `rootCtx` — good. Now checking the security layer (digest computation, preparation store) and the TUI door.TUI door read. Several candidates: silent event-drop on persistence error (vs web cancelling), study-cancel bypassing durable acceptance via the `x`-key path, and the run-detail refresh fetching only the oldest 200 events. Checking the TUI model's event handling and the RunUseCases paging semantics next.`Events` returns ascending order limited to 200, and the TUI replaces its whole event pane each tick. Checking how the run-detail view renders `DurableEvents`, then verifying whether runs realistically exceed 200 events.The run-detail pane confirms: no paging, replaced wholesale each second — a real tail-visibility defect for >200-event runs. Now verifying the alias/dedup regime (retry blocking), the preparation store, and the contract test pins.Alias conflicts surface regardless of the existing run's terminal state. Checking whether compaction/tombstone ever removes alias rows (which would bound the TUI retry block), plus how `Accept` is reached from `beginDurableCLICommand` with empty digest.Alias rows are never deleted anywhere — compaction only flips `record_state`. Now checking: (a) whether tests pin the TUI Existing behavior, (b) how `RunSmoke` computes EffectiveTimeout vs the request Timeout, (c) whether web transport forwards `timeout`.Transport mapping and error table read. Now the contract tests, CLI follow loop, and outcome mappings.Middleware confirms mutations require signed sessions, so the empty-session cancel bypass is drain-only — defended. Now verifying build health and running the relevant test packages to establish baseline before writing findings.All baselines green. Evidence collection complete — here are the reviewed results.

---

# Findings — `shared-usecase-vocabulary` deep review

## F1. TUI content-digest dedup permanently blocks retrying any failed runtime operation (cross-door parity break)

**Claim:** The TUI acceptance digest is `sha256(CanonicalRequest + "\x00" + InputFingerprint)` (`internal/tui/app.go:236-238`). Alias rows are inserted into `operation_aliases` with a uniqueness constraint (`internal/runcontrol/sqlite.go:449-454`) and are **never deleted anywhere** (no `DELETE FROM operation_aliases` exists; compaction only flips `runs.record_state`). `AcceptOperation` resolves any alias conflict to `Existing=true` regardless of the prior run's state (`internal/app/durable_operations.go:106-112`). Therefore re-confirming byte-identical operation content through the TUI can never start new work again.

**Observable bad outcome:** An operator's `execute-start`, `review-start`, or `sprint-flow` fails transiently (runtime provider down) *before* any governed document changes — the base governed inputs for sprint kinds are planning docs only; `.run-state.json`/`flow-state.json` enter the fingerprint only for QA kinds (`operations.go:663-697`). Re-preparing produces the identical fingerprint, the identical digest hits the surviving alias of the **failed** run, and the TUI renders `"matching durable operation already exists"` (`tui/app.go:245-250`) forever. Retry via the TUI door is structurally impossible. The web door allows retries by design (digest = `sha256(session+"\x00"+token)`, token single-use with 2-min TTL, `security.go:448-451`, `security.go:413-437`) and CLI has no dedup — same logical action, three different replay semantics, only one of which traps the operator.

**Trigger/preconditions:** Any runtime-backed TUI operation that terminates without changing a governed input file, followed by any later identical confirmation.

**Counter-evidence searched:** `TestDurableOperationDeduplicatesAcrossManagersAndFailsClosed` (`durable_operations_test.go:92-124`) pins dedup across managers but never re-accepts the same digest after a terminal outcome — the trap is untested. No TTL, retention, or tombstone path removes aliases. Web-side replay is scoped correctly per its contract.

**Severity:** High (operator-facing dead end on the failure-retry path, the most common recovery flow). **Confidence:** High.

**Regression test:** In `internal/tui` or `internal/app`: accept digest D, `FinishOperation(..., failed)`, then `AcceptOperation(same confirmation, D)` again and assert a *new* run is created (or assert the product intent differently — today it returns `Existing=true` with the dead run's lifecycle). Equivalent test at the `beginOperation` level with a fake manager.

## F2. TUI `c`-key study-cancel executes an unconfirmed mutation under a **stale confirmation's durable identity**

**Claim:** `ActionCancel` (`c`) calls `beginOperation(app.OperationRequest{Kind: OperationStudyCancel, Study: study})` directly (`tui/app.go:132-140`) — the only caller that passes no `Confirmation`. Inside `beginOperation`, the durable branch keys off `m.model.Confirmation != nil`, not off whether the confirmation matches `req` (`tui/app.go:235-238`):

```go
accepted, err := manager.AcceptOperation(opctx, *m.model.Confirmation, digest)
...
return m, tea.Batch(m.operationCmd(opctx, req, acceptedRunID, stream), ...)
```

A `Confirmation` prepared for a *different* operation survives tab switches (`setTab` never clears it; only Back/confirm/beginOperation do), so: prepare e.g. sprint-flow → press `u` (studies) → open an active study → select "View Run [ACTIVE]" → press `c`.

**Observable bad outcome:** `AcceptOperation` persists and claims a durable row with `Target.Operation="sprint-flow"` (the stale request), emits its lifecycle-running event, then `operationCmd` executes **study-cancel** (SIGINT to the study run-loop PID, `operation_runner.go:152-162`) under that run ID and finishes it as succeeded. The durable journal records a sprint-flow mutation that never executed, while the actual external effect — cancelling a study — has no truthful durable record and passed through no confirmation gate. Even without a stale Confirmation, this path performs a mutating action (SIGINT to a foreign process) with **no durable run at all**, unlike the web door where `study-cancel` is a full prepare→confirm→accept operation (`operation_handlers.go:716-717`), straining user-guide.md:376 ("every … confirmed web/TUI operation … receives a durable run").

**Counter-evidence searched:** No TUI test touches `ActionCancel`/`beginOperation`/study-cancel (`rg` over `internal/tui/*_test.go`: zero hits). No other code clears `Confirmation` on navigation. The web door always binds Target to the confirmed request.

**Severity:** High (durable-record falsification + unconfirmed external effect). **Confidence:** High on the code path; the UI sequence is reachable per `model.go:542-543` (item 0 is "View Run [ACTIVE]" when active) and `keys.go:48-49`.

**Regression test:** TUI-level test driving Update with a KeyMsg sequence (prepare → tab switch → open active study → `c`) against a fake manager asserting `AcceptOperation` receives a request whose Kind equals the executed action, plus a test that study-cancel without a prior confirmation still produces a durable row.

## F3. Smoke confirmation misreports the effective timeout it asks the operator to approve

**Claim:** `PrepareOperation` builds its smoke preview **without** `req.Timeout` (`operations.go:305`: `sprint.SmokeRequest{Level…, Suite…, Test…, ForceReview…, OverrideRationale…, DryRun: true}`), so the confirmation scope line `"timeout: %s (source: %s)"` reflects configured/manifest defaults. The runner honors the requested override: `sharedOperationRunner` parses `req.Timeout` into the real `SmokeRequest.Timeout` (`operation_runner.go:82-85`; likewise :102-105 for verify), and `smokeTimeout` returns `(req.Timeout, "request")` when set (`smoke_protocol.go:645-646`). The web transport forwards caller `timeout` (`operation_handlers.go:35`, `:619`).

**Observable bad outcome:** Operator prepares `smoke-start` with `options.timeout: "90m"`, sees `timeout: 30m0s (source: configured)` in the confirmation scope, approves — and the harness is killed at their unreviewed 90m (or runs 90m believing 30m was approved). What is displayed for approval differs from what executes, on the one option class that changes kill behavior. Additionally, an unparseable `timeout` silently degrades to "no override" (`time.ParseDuration` error discarded, `operation_runner.go:83`, `:104`) with no warning event or result flag — fake confidence in the override path. TUI never sets `Timeout` (grep confirms), so exposure is the web/API door.

**Counter-evidence searched:** No layer re-validates or re-displays the requested timeout at confirm time; `runtimeConfirmationDTO` shows only model fields (`operation_handlers.go:124-130`); contract tests pin kind/QA/SSE/error tables but nothing about timeout propagation.

**Severity:** Medium. **Confidence:** High.

**Regression test:** Prepare a smoke-start with `Timeout: "90m"` against a stub sprint service capturing the preview request: assert either the preview received the timeout (confirmation shows `source: request`) or the confirmation explicitly discloses that the displayed timeout excludes the request override; plus a runner test asserting invalid timeouts produce a visible warning rather than silent fallback.

## F4. Web prepare is contracted as "side-effect-free" but executes external harness processes

**Claim:** `docs/local-web.md:132`: "`POST /api/v1/operations/prepare` performs **side-effect-free** normalization, prerequisite inspection, affected-path projection, and fingerprinting." `PrepareOperation` for `smoke-dry-run`/`smoke-start` (and QA dry-run with suite=smoke) calls `RunSmoke(DryRun:true)`, which unconditionally spawns the workspace-configured smoke harness discovery subprocess (`s.processRunner.Run(...Commands.Discover...)`, `internal/sprint/smoke.go:97`) — external code execution against the target, up to `DiscoveryTimeout` (30s default), before any confirmation and with no durable record.

**Observable bad outcome:** Each unconfirmed prepare (and each start, which re-prepares first — `operation_handlers.go:151`, `:345`) launches harness processes invisible to run control. Capacity rejection happens *after* execution (`handleOperationPrepare` prepares before `preparations.issue` can reject, `operation_handlers.go:97-105`), so rejected prepares still ran the harness. Audit trail: nothing. Whether discovery mutates the target depends on the harness, which is exactly why the "side-effect-free" guarantee cannot be kept as written.

**Counter-evidence searched:** TUI/CLI doors share the same behavior (consistent cross-door), and the preview is arguably necessary to show real scope — the defect is the contract text plus the absence of any disclosure/record, not the preview concept. QA non-smoke dry-run and all other kinds are genuinely read-only.

**Severity:** Medium (contract violation with auditability gap; execution is session+CSRF-gated loopback). **Confidence:** High.

**Regression test:** Documentation-pin test or a stubbed process-runner assertion that `POST /api/v1/operations/prepare` for smoke kinds documents/executes discovery — minimally, fix the contract sentence; ideally emit a durable `preparing` event recording the preview execution.

## F5. TUI run-detail pane can never show events beyond the oldest 200 — live tail invisible for long runs

**Claim:** Every refresh computes `after = OldestRetainedSequence - 1` fresh and fetches `RunEvents(runID, after, 200)` (`tui/app.go:372-386`); `Events` returns ascending order with `LIMIT ?` (`sqlite.go:809-821`); the model **replaces** the pane each tick (`model.go:187`, `dedupeDurableEvents` keeps ≤200). The renderer prints exactly that list (`views.go:335-342`) with no paging.

**Observable bad outcome:** Any run emitting >200 retained events (a study run-loop with dozens of tasks, or QA with many shards — distinct task/shard IDs defeat progress coalescing since the coalesce key includes Task, `durable_operations.go:171`) shows the same first 200 events every second while the header simultaneously displays `last=<N>` for N≫200 (`views.go:330-331`). Progress, warnings, and the terminal transition are invisible in the TUI door, though `run follow` (loops until caught up, `run_commands.go:161-194`), the web SSE fallback (cursor advances, `operation_handlers.go:479-502`), and even the web run page (pagination via `NextEventsURL`, `run_handlers.go:337-348`) all show the tail. This is the concrete divergence inside the triplicated polling logic flagged for review.

**Counter-evidence searched:** No cursor state, scroll paging, or tail-window fetch exists anywhere in `internal/tui`. `RunViewShowPrevious` toggles task summaries, not durable events.

**Severity:** Medium (single-door observability failure precisely during long mutating operations, where it matters most). **Confidence:** High.

**Regression test:** Repository with 250 seeded events; drive `dashboardAndRuns`/refresh twice and assert the rendered event set includes sequences near `LastSequence` (today it deterministically does not).

## F6. Same event-persistence failure produces opposite semantics per door — and neither tells the truth

**Claim:** When `RecordOperationEvent` fails (non-`ErrWebUnavailable`), the web bridge cancels the whole operation (`web/operations.go:246-251`: `record.cancel(); return`) — no warning event, no counter, and `finish` then classifies the killed work as `cancelled` (`errors.Is(runErr, context.Canceled)`, `web/operations.go:288`). The TUI bridge silently drops the event and **continues running** (`tui/app.go:287-295`: bare `return`), so persistence loss leaves the operation executing with no durable progress and no UI signal; if the store recovers, the run completes "successfully" with missing journal entries.

**Observable bad outcome:** A transient SQLite write error mid-QA/mid-study yields, on the web, "operation cancelled" that no human requested (error-truth failure; cause recorded nowhere — not in diagnostics, counters, or events); on the TUI, dark execution with an event journal that understates what happened. Identical fault, contradictory durable outcomes — the trust boundary ("same logical action via different doors ⇒ equivalent durable semantics") breaks in both directions at once.

**Counter-evidence searched:** `RecordOperationEvent` already cancels the owned op internally on append failure (`durable_operations.go:213-215`), so the TUI's continue-and-drop path also races the manager's own cancellation — but the TUI wrapper neither propagates nor surfaces it. No test covers either bridge's failure branch.

**Severity:** Medium-low (requires storage failure, but consequence is mislabeled terminal state / silent journal gaps). **Confidence:** High on divergence and missing signal.

**Regression test:** Fake manager whose `RecordOperationEvent` returns an error once: assert web cancels with an explicit persistence-failure terminal reason/event, and TUI either aborts with the same reason or visibly flags degraded journaling — one shared policy, pinned by test.

## F7. Three outcome-mapping implementations disagree; hub drops `timed_out`

**Claim:** `FinishOperation` maps `context.DeadlineExceeded → TerminalTimedOut` (`durable_operations.go:289-290`) and `terminalOutcome` additionally treats timeout-category errors as timed out (`run_control.go:765-777`), but the hub's finish mapping has no deadline/timeout case at all (`web/operations.go:286-294`): a deadline error falls to `state = "failed"`.

**Observable bad outcome:** If any runner path surfaces `context.DeadlineExceeded` (or a result state the durable side reads as timed out), the SSE/browser operation document says `failed` while the canonical `/runs/<id>` page and durable terminal say `timed_out` — two truths for one outcome on the same screen. Latent today (smoke timeouts surface as typed smoke errors, not ctx errors), which is exactly why no test notices; the vocabulary contract (`local-web.md` stable states incl. implicit timeout semantics; `TerminalTimedOut` exists) is asserted nowhere across the three mappers.

**Severity:** Low (latent inconsistency, high confusion potential when triggered). **Confidence:** High on divergence, low on current reachability.

**Regression test:** Table test feeding identical `(result, err)` pairs to all three mappers asserting one outcome vocabulary; include `context.DeadlineExceeded`.

---

# Defended / non-issues (checked, with counter-evidence)

- **Cancellation reason spelling divergence (`user_request` vs `user_requested` vs CLI canonical set):** Real inconsistency (`operation_handlers.go:207/:209`, `run_commands.go:394-401`), but repository validation is format-only (`lifecycle.go:77-80`) and every consumer renders reasons as opaque display strings — no filter or branch matches spelling (`rg` over consumers). Display-only today; not reportable as a defect.
- **Empty-session cancel bypass:** `cancelOperation("", …)` skips ownership, but the security middleware guarantees non-empty signed sessions for handlers and mutations require `validSession` (`security.go:110-119`, `:151-152`); the bypass is reachable only from internal `drainAndWait`. Defended.
- **Cross-session visibility of durable operations** (`handleActiveOperations` merges run-control rows without session filter; durable cancel fallback has no ownership check): requires an authenticated local session on a loopback single-operator tool; consistent with the tool's threat model. Not raised.
- **Operations killed by HTTP request completion:** disproved — hub derives operation contexts from `rootCtx` (`server.go:83-85`, `web/operations.go:180`), and `RecordOperationEvent` uses `context.Background()`.
- **`hub.start` (empty dedupKey):** test-only; no production caller.
- **`OperationPartial`/`interrupted` unreachable by runners:** latent vocabulary, mapped consistently by both finishers; no consumer depends on production emission.
- **Fingerprint TOCTOU, `boundContent` rune-splitting, prepare-time re-hash cost, `controlOperation` full-repo reconcile cadence:** noted during review; none produces a concrete cross-door correctness failure beyond inherent mechanism limits, and the reconcile/heartbeat cadence belongs primarily to the spine surface.
- **`ErrStaleOperation` guidance copy** ("Inspect the active or stale study lock…" for non-study ops, `operations.go:827`): misleading text on a correct classification — cosmetic; folded into F7's table-test suggestion rather than reported standalone.
- **readOnly split:** serve forces `readOnly:true` twice (`serve_commands.go:59`, `web_usecases.go:429`) while the TUI leaves it false, so TUI refreshes rewrite `flow-state.json` (`usecases.go:130-132`, `service.go:291-294`). `tuiHelp` discloses this truthfully (`tui_commands.go:88-90`); the residual defect is the contradicting top-level line `app.go:268` ("read-only terminal dashboard") — an error-truth documentation bug worth fixing alongside F3/F4, not a behavioral defect on its own.

No target or workspace files were modified; baseline `go build`, `go vet`, and the web/app/tui test packages pass at the frozen tree.