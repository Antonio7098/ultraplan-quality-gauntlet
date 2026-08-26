Working tree matches the frozen commit for all Go source (diff only touches workspace artifacts). Starting the review with the core vocabulary files.Now the TUI model (to trace `Confirmation` lifecycle), the web hub, and handlers.Now the TUI model and keys — I need to trace when `m.model.Confirmation` can be stale/non-nil relative to the request being begun.The TUI `Confirmation` lifecycle looks suspicious. Checking key bindings and how the confirmation flow is presented, plus whether navigation clears it.Retention defaults confirmed (7d full + 30d tombstone). Now the web handlers — mapping, cancellation paths, and the SSE fallback loop.All primary paths inspected. Compiling the final review.

---

# Findings — `shared-usecase-vocabulary`

## F1 — TUI begins an operation using whatever `Confirmation` is resident, accepting a durable run whose `Target.Operation` describes work that is never executed (and swallowing the action the operator requested)

**Claim.** `teaModel.beginOperation(req)` never compares `req` against `m.model.Confirmation`, yet uses that stored confirmation — its canonical request, fingerprint digest, and whole `AcceptOperation` payload — whenever it is non-nil (`internal/tui/app.go:232-243`). One caller passes a request that is unrelated to the stored confirmation: the `ActionCancel` study-cancel branch builds `OperationRequest{Kind: OperationStudyCancel, Study}` fresh (`app.go:132-141`).

**Observable bad outcomes.**
1. Durable record lies: run-control accepts a row with `Target.Operation = "<stale kind>"` (e.g. `study-start` / `execute-start`), claims it, emits `lifecycle=running`, then executes *study-cancel* (SIGINT via `sharedOperationRunner`, `operation_runner.go:152-162`) and finishes it `succeeded`. The authoritative record of what ran is wrong.
2. The requested cancellation can be silently skipped: if the stale confirmation's digest was already used, `AcceptOperation` returns `Existing=true` and `beginOperation` returns early (`app.go:245-250`) — no SIGINT is sent, and the screen shows "matching durable operation already exists" instead of any cancellation status.
3. Alias hijack: the digest of the stale confirmation is consumed by this bogus accept, so a later legitimate confirm of that same operation is alias-blocked ("matching durable operation already exists") pointing at the junk row.

**Trigger.** (a) prepare any operation (e.g. Enter on "Run Loop [RUNTIME]" → parallelism form → confirmation lands via `ConfirmationMsg`, route-guarded only at arrival, `model.go:213-222`); (b) navigate without pressing Esc — `setTab` clears `Preview` but **not** `Confirmation` (`model.go:359-374`; tab keys fall through `app.go`'s `default` to `model.Update`); (c) on the study route with an active loop, item 0 is "View Run [ACTIVE]" (`ViewRun != ""`, `model.go:542-545`) and `Selected` defaults to 0; (d) press `c` (help footer: "c request cancellation", `keys.go:58`). Route is `RouteStudy` (not `RouteRun`), nothing is `Running`, so control reaches `app.go:132-141`. The confirmation overlay does render full-screen (`views.go:35-36`), which lowers but does not eliminate exposure — `c` is never intercepted while a confirmation is pending.

**Counter-evidence searched.** Esc does clear `Confirmation` (`model.go:265-266`), but is not required for any other navigation key; `ParallelForm` gating (`app.go:63-99`) is closed by the time the confirmation exists; `RunOperation` cannot catch it because the passed request carries empty `ExpectedFingerprint`, skipping staleness checks (`operations.go:407`); no test exercises a mismatched pair (`model_test.go` covers only matching flows); `AcceptOperation` performs no server-side re-validation of the confirmation against current inputs. Web door cannot replay this: tokens are single-use, session-bound, and consumed inside `startConfirmed` (`security.go:413-437`).

**Severity:** high (durable-vocabulary integrity violation on the exact invariant this surface owns — "Target.Operation stores the kind that ran"). **Confidence:** high on mechanism; medium on field frequency.

**Regression test:** drive the teaModel with the key sequence above against a fake `DurableOperationManager` recording `AcceptOperation` arguments; assert the accepted `Confirmation.Request.Kind == OperationStudyCancel` (fails today) and that cancellation still occurs when the digest collides.

## F2 — TUI's content-addressed digest permanently blocks re-running an identical mutating operation, diverging from the web and CLI doors

**Claim.** TUI dedup key = `sha256(CanonicalRequest ‖ "\x00" ‖ InputFingerprint)` (`tui/app.go:236-238`). `ResolveOperationAlias` has no lifecycle filter (`runcontrol/sqlite.go:472-484`), and alias rows survive until the run row is deleted — ≥ `FullHistory + TombstoneHistory` = 7d + 30d by default (`retention.go:142-150`, `model.go:423-424`). So once `smoke-start` (for example) has been confirmed once via TUI, the identical request can never be re-run from the TUI until the row expires — including retrying a **failed** run.

**Observable bad outcome.** Smoke gate fails (harness flake). Operator retries from the TUI: prepare succeeds, confirm → alias conflict → `Existing=true` → "matching durable operation already exists" pointing at the *failed* run. Nothing is running; the message misleads and the work is blocked through this door. The web door allows the identical repeat (each prepare issues a fresh token; dedup key is `sha256(session‖token)`, `security.go:403-408,448-451`), and CLI creates a new run every invocation (empty digest, `durable_operations.go:55`). Same logical action, three doors, three incompatible durable semantics — the exact trust-boundary the assignment names. The blocked window is aggravated because smoke writes (`smoke.md`, flow-state freshness) are not hashed inputs for smoke kinds (`operations.go:663-686` adds extras only for `qa-*`), so nothing changes the digest.

**Counter-evidence searched.** Cross-manager dedup of a *running* operation is deliberate and tested (`durable_operations_test.go:92-112`); retention/tombstone truthfulness is documented (user-guide). But no contract documents TUI content-level idempotency, its lifetime, or its divergence from API-IDEMP-001-style replay on the sibling doors; `local-web.md` step 3 defines replay only for the web session/token pair. Existing-state rendering also leaks vocabulary mixing (`OperationState(accepted.Lifecycle)` casts lifecycle strings like `succeeded` into the `complete/failed/cancelled` family, `tui/app.go:247`).

**Severity:** medium. **Confidence:** high on behavior; medium that it is unintended (intent is undocumented either way).

**Regression test:** accept digest D, finish the run terminal, accept D again through a fresh manager, and assert the documented policy (today: second returns `Existing=true` forever); plus a parity test running the same logical `smoke-start` twice through web fixtures to show two runs while TUI yields one.

## F3 — `verify-start` / `smoke-start`: confirmation states "requested model X"; execution silently drops the model

**Claim.** `PrepareOperation` sets `ModelSource` via `operationRuntimeIdentity`, whose first branch returns `"requested model "+req.Model` for **every** runtime kind, including smoke and verify start (`operations.go:356-358, 636-639`; `Runtime=true` at `operations.go:309,314`). The shared runner never forwards `req.Model` for these kinds: `OperationSmokeStart` builds a service with no model plumbing (`operation_runner.go:79-96`; `sprint.SmokeRequest` has no model field, `smoke_types.go:58-67`), and `OperationVerifyStart` constructs `VerifyRequest{Review:{Focus,Restart}, Smoke:{...}}` with no `ModelOverride` although the embedded `ReviewRequest` supports one (`operation_runner.go:97-113`; `verify.go:19-25`). Transport admits `options.model` for all non-QA kinds (`operation_handlers.go:618`, QA lockdown only at `:734-735`).

**Observable bad outcome.** An API client requests a model for `verify-start`; the confirmation DTO and canonical request (which feeds the fingerprint) assert "requested model X"; execution runs review/smoke on configured defaults. Confirmed contract ≠ executed semantics — misleading success on a mutating, evidence-producing operation. (First-party forms only attach model inputs to `sprint-flow` and `study_run_loop`, which do honor it — `sprint.html:36,109`, `study.html:56`, `operations.js:9-10` — so exposure is API-level.)

**Counter-evidence.** QA rejects caller models outright at two layers; execute/review/flow/stage/study all honor the override — the drop is specific to these two kinds and unpinned by any test.

**Severity:** medium. **Confidence:** high.

**Regression test:** prepare + run `verify-start`/`smoke-start` with `Model:"vendor/x"` against a recording runtime adapter; assert either the override reaches the sprint layer or prepare refuses/ignores the field explicitly.

## F5 — Web operations DELETE falls back to cancelling *any* run id without the `Target.Kind=="operation"` guard every other fallback applies

**Claim.** `handleOperationCancel`/`handleHTMLOperationCancel` fall back to `h.runs.CancelRun(...)` when the hub lacks the record, with no target-kind or session scoping (`operation_handlers.go:206-213, 391-396`). Status (`:411-420`), events (`:447-453`), HTML status redirect (`:362-368`) and the active-operations merge (`:195`) all enforce `snapshot.Target.Kind == "operation"`. `durableOperationDocument` then projects whatever snapshot it gets as an operation document (`:424-435`).

**Observable bad outcome.** `DELETE /api/v1/operations/<id>` for a non-operation run-control row (e.g. an externally accepted target from another tool sharing the workspace DB) succeeds and returns an operation-shaped document for it — cancelled through the wrong door, with the response asserting `kind`/operation semantics that don't hold. Bounded by the loopback single-operator model and random ids (not listed: `activeOperations` filters kind), hence low rather than medium.

**Severity:** low. **Confidence:** high on inconsistency; consequence bounded.

**Regression test:** cancel fallback against a fixture snapshot with `Target.Kind != "operation"`; expect refusal symmetric with the status/events guards.

## Low-severity notes

- **Timeout disclosure asymmetry (low, high confidence).** Smoke preview in prepare omits the caller's `Timeout` string entirely (`operations.go:305` — `SmokeRequest.Timeout` is a `Duration`, never populated), so the confirmation's "timeout: X (source: Y)" always reflects settings defaults, while the runner parses and applies `req.Timeout` — and silently treats unparseable values as "no override" (`operation_runner.go:82-84,103-105`). A confirmed timeout is applied but not disclosed; an invalid one is accepted and ignored. Regression: prepare/run round-trip asserting the scope line matches the effective run timeout.
- **Cancellation reason vocabularies diverge with no canonical owner (low, latent).** Persisted reasons include `user_request` (hub, `operations.go:630`; handlers `operation_handlers.go:207,390`), `user_requested` (web fallback `:209,392`; TUI `app.go:110,124`; runs API `run_handlers.go:898,923`), free-form `"QA cancellation requested"` (`sprint_usecases.go:993` — fails the CLI's own `canonicalCancellationReason` set, `run_commands.go:394-401`), and `server_shutdown`. Repository validates format only (`lifecycle.go:77-80`); today nothing branches on values (display-only; the `Reason=="server_shutdown"` checks are on the separate product cleanup records). Latent trap for any future filter/dashboard grouping; worth a single canonical fold point.
- **`omission` durable events are projected as `recovery_required` SSE frames** in the durable fallback (`operation_handlers.go:523-526`), though routine progress-coalescing omissions (`durable_operations.go:196-207`) are normal bookkeeping. No client currently special-cases `recovery_required` (`static/js` contains only the subscription list), so today it is vocabulary noise; will become a wrong signal the moment a consumer honors the name.

## Defended non-issues (searched, counter-evidence found)

- **`hub.start(session, prepared)` with empty dedup key** is test-only (all callers in `operations_test.go`); production reaches `startConfirmed` exclusively.
- **Empty-session cancel bypass** in `cancelOperation` (`operations.go:350`) is reachable only in-process by `drainAndWait`.
- **Three outcome mappings** (`terminalOutcome` over `runtimepkg.Result` + parent ctx; `FinishOperation` over `(state, err)`; hub `finish` over the same) overlap but agree on the cases I traced (cancel/timeout/fail/succeed); they consume different input shapes produced by different pipelines, and no path feeds one pipeline's result to another's mapper.
- **Progress coalescing excludes counters from the key** (`durable_operations.go:171`) — within the 250 ms window count-only deltas are folded, but omissions preserve the fact and count durably; deliberate bounded-loss design.
- **Cross-session visibility of durable runs** in web (active list/cancel) mirrors the TUI/CLI, which see the whole workspace; consistent with the documented local single-operator session policy (local-web.md:113).
- **TUI event drop on persistence error** (`tui/app.go:288-291`): the manager cancels the owned operation on append failure (`durable_operations.go:213-215`), so the dropped display event is followed by a visible cancelled/failed terminal rather than silent divergence.
- **readOnly composition split**: functionally confined to dashboard/status paths (`usecases.go:130-132`); the mutating runner deliberately builds non-read-only services because those operations legitimately write. Only residue is the top-level help contradiction (`app.go:268` "read-only terminal dashboard" vs `tui_commands.go:90`'s accurate disclosure, disclosed again at confirmation time, `operations.go:280`) — doc nit, not a behavioral defect.