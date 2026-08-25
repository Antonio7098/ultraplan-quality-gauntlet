# Context Pack: `web-operation-hub-sse` — Web operation hub, SSE, and shutdown drain

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: operator-interfaces. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

The local loopback dashboard exposes guarded mutations over existing app operations through a two-phase prepare/start protocol and streams their progress as Server-Sent Events. This surface owns:

1. **Prepare** (`POST /api/v1/operations/prepare`, HTML `/operations/prepare`): side-effect-free normalization of a browser-supplied operation spec into an app `Confirmation` (canonical request + governed-input fingerprint), bound server-side to a TTL'd single-use confirmation token.
2. **Start** (`POST /api/v1/operations`, HTML `/operations/start`): re-canonicalizes the caller's spec, recomputes the current fingerprint, and starts work only if the presented token is unexpired, unconsumed, session-matched, canonical-matched, and fingerprint-current. Start registers an in-memory `operationRecord` in the bounded ephemeral hub and launches one goroutine running the shared app use case.
3. **Durable acceptance**: when the app capability implements `app.DurableOperationManager` (production `serve` always wraps it), start persists run acceptance + owner claim + running lifecycle before the goroutine starts, and deduplicates by confirmation digest via a durable operation alias.
4. **Observe** (`GET /api/v1/operations/{id}` status; `GET /api/v1/operations/{id}/events` SSE): session-scoped status/result projection, replayable typed event stream with monotonic IDs, heartbeat, 30-minute lifetime cap, replay-gap accounting, and slow-subscriber eviction; falls back to polling the durable run journal when the hub does not retain the operation.
5. **Cancel** (`DELETE /api/v1/operations/{id}`, HTML form): idempotent single-shot cancellation through each record's context cancel function, reason-canonicalized; falls back to durable run cancellation for non-retained runs.
6. **Graceful drain** (`hub.drainAndWait`, called by `web.Run` on serve-context cancellation): marks draining (rejects new prepares/starts), cancels every non-terminal record once with reason `server_shutdown`, waits up to the 10 s `ShutdownTimeout` for terminal completion, and on deadline expiry first persists product-owned cleanup-uncertain markers (sprint/study `.cleanup-uncertain.json`) and only then projects in-memory `cleanup_uncertain` terminal state and closes subscribers.

The hub is explicitly transport-lifecycle state only; workspace artifacts, flow-state, execute/QA state, and the run-control journal remain authoritative (Sprint 31 requirements; docs/local-web.md "The hub is deliberately ephemeral").

## 2. Entrypoints and control flow

### 2.1 HTTP entrypoints (internal/web/operation_handlers.go)

Routing lives in routes.go:282-402 (`matchRoute`) with dispatch at handlers.go:761-786 and method allowlists at routes.go:252-271. Every request passes through `securityMiddleware.wrap` (security.go:102) before dispatch.

| Route | Method | Handler |
| --- | --- | --- |
| `/api/v1/operations/prepare` | POST | `handleOperationPrepare` (:78) |
| `/api/v1/operations` | GET/HEAD | `handleActiveOperations` (:185) |
| `/api/v1/operations` | POST | `handleOperationStart` (:132) |
| `/api/v1/operations/{id}` | GET/HEAD | `handleOperationStatus` (:169) |
| `/api/v1/operations/{id}` | DELETE | `handleOperationCancel` (:206) |
| `/api/v1/operations/{id}/events` | GET | `handleOperationEvents` (:230) |
| `/operations/prepare` | POST | `handleHTMLOperationPrepare` (:293) |
| `/operations/start` | POST | `handleHTMLOperationStart` (:330) |
| `/operations/{id}` | GET | `handleHTMLOperationStatus` (:362) |
| `/operations/{id}/cancel` | POST | `handleHTMLOperationCancel` (:381) |

### 2.2 Prepare (API variant)

1. Capability check (`h.hub == nil || h.hub.ops == nil` → error); `isDraining()` check → `errServerDraining`.
2. `decodeStrictJSON` (:593): requires `Content-Type: application/json` prefix, `DisallowUnknownFields`, exactly one JSON value; body already capped at 64 KiB by middleware `MaxBytesReader` (security.go:167).
3. `mapOperationRequest` (:610): scope identifiers validated via `validOptionalIdentifier` → `validIdentifier` (handlers.go:1035: ≤128 bytes, not `.`/`..`, no `/`\, identifier charset regex); kind aliases normalized (underscores→dashes; ~27 browser kinds incl. dry-run/resume/status variants → `app.OperationKind`); per-family scope rules (validate needs exactly one of project/study; `study-*` requires only study; others require project+sprint; QA kinds reject all options except `shard`, shard valid only for start/resume).
4. `ops.PrepareOperation(r.Context(), req)` (app/operations.go:211): clears any caller-supplied fingerprint, normalizes/trims, validates scope charset again (validateOperationScope :541), injects review parallelism default, builds per-kind `Confirmation` (scope/warning/runtime/mutates/mutation class/prerequisites/model source), computes `canonicalOperationRequest` (:577, JSON of the full request) and `fingerprintOperationInputs` (:663: sha256 over canonical JSON + runtime identity + governed file contents — ultraplan.yml, project/sprint governed Markdown, QA adds execute/review/flow-state inputs; symlink rejection; dir walk sorted). Sets `Request.ExpectedFingerprint` server-side only.
5. `preparations.issue(sessionID, confirmation)` (security.go:396): reap expired (TTL `PreparationTTL` = 2 m), capacity cap `MaxPreparations` = 128 post-reap else `errOperationCapacity`; stores record keyed by token `"confirm_"+randomRequestID` binding Session, Canonical, Fingerprint, full Confirmation, ExpiresAt = now+2 m, Consumed=false.
6. Response 200 `preparationDTO`: preparation id, projected spec (`mapOperationSpec` :744), affected paths, mutation class, runtime/harness maps when relevant, input fingerprint, expiry, confirmation token.

HTML variant identical through form decoding (`operationSpecFromForm` :536 incl. `newSprintRef` :555 slug building from sprint_number+sprint_name), CSRF `_csrf` form field check first, renders `operation-confirm` template instead of JSON.

### 2.3 Start

API and HTML variants share `hub.startConfirmed(session, dedupKey, confirm)` (operations.go:150):

- `dedupKey = confirmationDedupKey(session, token)` = sha256(session + "\x00" + token) (security.go:448).
- Under `h.mu`: `reapLocked` (drops records FinishedAt ≥ `TerminalRetention` 10 m plus their dedup entries, :565); **dedup check precedes draining/capacity checks** (:154-159): if the digest maps to a live record for the same session, return the cloned original document without consuming or starting anything (retry-after-network-failure semantics, pinned by operations_test.go:375-384); a foreign-session or stale mapping deletes the dedup entry and proceeds.
- Draining gate → `errServerDraining`; active gate `h.active >= MaxActiveOperations` (8) → `errOperationCapacity`; both increment `capacityRejections`. Capability-nil check.
- Lazy `confirm()` callback: `h.preparations.consume(token, session, current.CanonicalRequest, current.InputFingerprint)` (security.go:413) under the store mutex: unknown token or already-consumed → `errConfirmationReplayed`; expired → marks Consumed and returns `errConfirmationExpired`; session or canonical mismatch → marks Consumed, `errConfirmationMismatch`; fingerprint drift vs freshly prepared current → marks Consumed, `errStaleConfirmation`; success marks Consumed and returns the stored Confirmation. Note the start handler re-runs `PrepareOperation` *before* entering the lock to obtain `current` (:151, :345).
- New id `"op_"+randomRequestID`, regenerated on collision; `ctx, cancel := context.WithCancel(h.rootCtx)` where rootCtx is the server-owned `operationRoot` (server.go:83-85).
- **Durable path**: if `h.ops` implements `app.DurableOperationManager`, `AcceptOperation(ctx, prepared, dedupKey)` (app/durable_operations.go:97) persists `repository.Accept` with `OperationAlias=ConfirmationDigest=dedupKey`, `Claim`s a lease, appends the `running` lifecycle event (persistence failure here proposes `TerminalPersistenceLost` and fails closed), then derives a cancellable `operationCtx` tagged with ownership fencing and spawns `controlOperation` (heartbeat/lease-renewal/cancellation-poll/reconcile ticker loop :223). On `runcontrol.ErrConflict` with a digest it resolves the alias and returns `Existing=true` — start returns early with `{ID: runID, State: accepted.Lifecycle, DurableStatus: /runs/<id>}` and calls its own `cancel()` (no local record created). On `app.ErrWebUnavailable` the hub falls through to the purely ephemeral path keeping the `op_` id and original ctx; any other accept error aborts the start.
- Otherwise (and on the unavailable fallback): create `operationRecord{doc{State:"accepted", DurableStatus from prepared.DurableRefreshPath}, session, request, cancel, done chan, subscribers map, dedupKey}`, insert into `records`, register dedup, `active++`, append `snapshot` event `{state:"accepted"}`, clone doc, `go h.run(ctx, record)`; respond 202 with `Location: /api/v1/operations/<id>` and `Link </api/v1/runs/<id>>; rel=canonical`.

`h.run` (:221): under mu transitions accepted→running with StartedAt + progress event; then `ops.RunOperation(ctx, record.request, publishAppEvent)`. For runtime-backed kinds this reaches `sharedOperationRunner` (app/operation_runner.go:18) / dashboard use cases (sprint FlowStage/Execute/Review/Smoke, study loops, QA) — workflow authority outside this surface.

`publishAppEvent` (:245): if durable manager present, `RecordOperationEvent(context.Background(), id, event)` FIRST (:247) — coalesces byte-equivalent progress inside `runcontrol.ProgressCoalesceWindow` with omission accounting, persists sanitized event, returns committed flag; persist error → `record.cancel()` and stop; not committed → skip local fan-out. Then builds the payload map with every text field passed through `safeWebText` (redaction, see §6) and appends locally unless already terminal.

After `RunOperation` returns: if durable, `FinishOperation` with a fresh 30 s timeout on `context.Background()` (:235, detached from both root ctx and drain ctx), joining finish errors into `runErr` except `ErrWebUnavailable`; then `h.finish(record, result, runErr)` (:279).

### 2.4 Terminal projection — `finish` (:279)

Under mu, no-op when already terminal. State mapping (:286-294): `errors.Is(runErr, context.Canceled)` or result `OperationCancelled` → `cancelled`; any error or `OperationFailed` → `failed`; `OperationPartial` → `interrupted`; otherwise `succeeded`. Sets FinishedAt, projects the result via `projectOperationResult` (:577: redacted fields, content capped at 128 KiB pre-check then whole-result 256 KiB check that drops content/findings and bounds message), appends `terminal` event carrying the result, decrements active, closes `record.done`, closes and removes every subscriber channel (streams counter decremented).

### 2.5 Status, active list, cancel

- `status(session,id)` (:311): reap, session-scoped lookup, cloned doc. Handler fallback chain (:169): hub miss → `durableOperationStatus` via `runs.Run` for any operation-target run regardless of session (:411); legacy `op_`-prefixed ids → 410 `legacy_operation_not_retained` (:422).
- `activeOperations(session)` (:322): non-terminal session records sorted newest-first (nil StartedAt sorts last); handler then merges durable runs with lifecycle ∈ {accepted,queued,running,cancelling}, TargetKind=operation, limit 200 (:188) — these durable entries are NOT session-filtered.
- `cancelOperation(session,id,reason)` (:347): empty session bypasses the ownership match (used internally by drain); terminal → return doc unchanged, requested=false (HTTP 200); `cancelOnce.Do` sets State=cancelling, Reason=`canonicalCancelReason` (:625: passthrough for server_shutdown/timeout/recovery, else user_request), appends `cancel_requested`; counter incremented and the record's `cancel()` invoked outside the lock exactly once. Handler fallback: hub miss → `runs.CancelRun` durable cancellation (:208).

### 2.6 Subscribe / SSE delivery

`subscribe(session,id,lastID)` (:376) under mu:
- Reap; session-scoped lookup else `errOperationNotFound`.
- Capacity: `h.streams >= MaxConcurrentStreams` (32) or `len(record.subscribers) >= MaxSubscribersPerOperation` (8) → `errSubscriberCapacity` (429 Retry-After 2).
- Replay-gap accounting (:388): when `lastID > 0` and the ring is non-empty and `lastID < events[0].ID-1`, increments `replayGaps` and appends `recovery_required {oldest_retained_id, newest_retained_id, refresh_path}` followed by a `snapshot` of current state/reason (both then included in this subscriber's replay).
- Replay slice = buffered events with ID > lastID. New channel of `SubscriberQueueSize` (32). Terminal record → immediately closed channel returned (replay-only connection, not registered). Otherwise registered; `streams++`.
- `unsubscribe` closure (once-guarded, mutex-protected remove+close+counter decrement) is deferred by the SSE handler.

`handleOperationEvents` (:230): parses `Last-Event-ID` header (decimal uint64, empty→0, else 400); subscribe failure → durable fallback `followDurableOperationEvents` (:447) which snapshots the run via `runs.Run`, rejects `after > LastSequence` with 409 `cursor_ahead` and `after+1 < OldestRetainedSequence` with 409 `replay_gap`, then polls `RunEvents(after, 512)` on a 250 ms–1 s adaptive timer, projecting durable events through `durableOperationEvent` (:516 name mapping accepted/claimed/lifecycle→snapshot, warning/finding/artifact/terminal identity, cancellation→cancel_requested, recovery/omission→recovery_required), until terminal+caught-up, 30 min lifetime, or client disconnect. Legacy `op_` ids → 410.
Live path (:249-290): `SetWriteDeadline(now + 30 min + 15 s)`; headers `text/event-stream; charset=utf-8`, `Connection: keep-alive`, `X-Accel-Buffering: no`; write replay frames then flush; select loop over subscriber channel (closed → flush+return), 15 s heartbeat comments (`: heartbeat\n\n`), 30 min lifetime timer, request-context cancellation. Frames formatted `id: %d\nevent: %s\ndata: %s\n\n` (:588).

### 2.7 Event buffer mechanics — `appendEventLocked` (:428)

Name normalized through `stableOperationEventName` (:470: 8 stable names pass; anything else becomes `progress`). Sequence increments per record; envelope `{operation_id, time RFC3339Nano, sequence, payload}` marshaled; marshal failure → drop + `projectionDrops++`. Encoded size > `MaxEncodedEventBytes` (16 KiB) → replaced by a `warning` stub ("event projection exceeded the safe size limit"). Ring trim to ≤256 events and ≤256 KiB total. `doc.LastEventID` updated. Fan-out: non-blocking send to each subscriber; full queue → close channel, deregister, `streams--`, `slowSubscribers++`.

### 2.8 Drain — `drainAndWait` (:479)

Called from `web.Run`'s ctx.Done branch (server.go:131-153):
1. Under mu set `draining=true`; snapshot all non-terminal records.
2. For each, `cancelOperation("", id, "server_shutdown")` — bypasses session check; `shutdownCleanups += len(records)`.
3. Sequentially wait `<-record.done` for each record OR `<-ctx.Done()`.
4. On deadline expiry: `persistCleanupUncertain(records)` FIRST (:505 — for each still-non-terminal record, fresh 1 s-timeout `context.Background()` call to `recorder.RecordOperationCleanupUncertain`, joined errors), THEN `markCleanupUncertain(records)` (:533 — under mu: state `cleanup_uncertain`, Reason `server_shutdown`, FinishedAt, Result DTO with "Refresh durable status before retrying." message, `terminal` event appended, active-- , close(done), close/remove subscribers). Returns `errors.Join(ctx.Err(), persistErr)`. Ordering is pinned by TestOperationHubDeadlinePersistsCleanupUncertaintyBeforeTerminalProjection (operations_test.go:181).
5. Back in web.Run: `cancelOperations()` (root ctx), `server.Shutdown(shutdownCtx)` (same 10 s budget, possibly already partially consumed by the drain wait), `server.Close()` on Shutdown error; `cleanupErr` takes precedence as the Run return error; waits for the Serve goroutine; emits `event=server_stopped`.

`RecordOperationCleanupUncertain` (app/web_usecases.go:540) honors ctx.Err() and routes by request shape: project+sprint → `sprint.Service.RecordCleanupUncertain` writing `projects/<p>/sprints/<s>/.cleanup-uncertain.json` (atomic temp+rename write, no lease held, reason forced to `server_shutdown`, schema-validated); study → study service equivalent; neither scope → typed error. Startup reconciliation consumes these markers under the mutation lease (`ReconcileOperations` app/web_usecases.go:563 → sprint `ReconcileInterruptedMutation` / study `ReconcileInterruptedRun`; a marker whose state cannot be reconciled aborts startup fail-closed per docs/local-web.md).

### 2.9 Serve command wiring (internal/app/serve_commands.go:18)

`ultraplan serve [--listen] [--open-browser]`: loopback validation (`ValidateLoopbackListen` :119 numeric IPv4/bracketed IPv6 loopback, explicit port, no zones), workspace discovery, config load, QA settings, runtime factory, run repository, `NewWebUseCases` with `Runner: sharedOperationRunner(...)`, `RunControl: repositoryRunUseCases`, `DurableOperations: newDurableOperationManager(repository, deps.runControl.owner)`, then `web.Run(deps.ctx, ...)`. Process-level ctx cancellation (SIGINT/SIGTERM handling lives in cmd wiring outside this surface) drives the drain path above.

## 3. Inputs and outputs

Inputs:
- JSON bodies (`prepareRequest`/`startRequest` wrapping `operationSpecRequest` kind/scope/options — ~20 option fields incl. model, parallelism, review focus, sources, dimensions, override rationale).
- HTML form fields (`_csrf`, kind, project, sprint | sprint_number+sprint_name, stage, model, parallelism, confirmation_token).
- Headers: Host, Origin, Referer, Sec-Fetch-Site, X-CSRF-Token (API mutations incl. DELETE), Last-Event-ID (SSE resume cursor), Content-Type.
- Cookie `ultraplan_session` (32-hex id + "." + HMAC-SHA256 under per-process random secret; MaxAge 3600).
- Path parameter `{id}` validated as identifier (dispatch, handlers.go:427-435).

Outputs:
- JSON envelopes `{data, meta}` / `{error:{code,message,retryable,details}, meta}` (writeSuccess/writeErrorDetails).
- SSE stream: typed frames with decimal monotonic ids, heartbeat comments; headers per §2.6.
- HTML pages (operation-confirm, operation status, errors); redirects 303.
- Headers: Location, Link rel=canonical, Retry-After: 2 (capacity/draining), X-Request-ID, security headers, Set-Cookie.
- Diagnostics lines (`event=http_request`, `event=operation_prepared/started/cancel...`, redacted via safeWebText, lockedWriter-serialized).
- External effects listed in §7.

## 4. Authoritative state

Ephemeral (this surface owns, process-local, lost on restart):
- `operationHub.records map[id]*operationRecord` — doc (id/kind/state/reason/timestamps/LastEventID/durable-status/result), owning session, request, cancel func + cancelOnce, done channel, nextEventID, event ring, subscriber map, dedupKey. Counters: active, streams, draining flag; atomic metrics starts/active/terminal/capacityRejections/cancellations/activeStreams/slowSubscribers/replayGaps/projectionDrops/shutdownCleanups.
- `dedup map[sha256(session\x00token)]opID`.
- `preparationStore.records map[token]*preparationRecord` (≤128, 2 m TTL, consumed-once flags).
- Retention: terminal records kept 10 minutes (`TerminalRetention`), then reaped lazily on any hub call that locks.

External authorities this surface reads/writes but never owns:
- Run-control store (SQLite via internal/runcontrol): runs, attempts/fences, event journal, leases/heartbeats, cancellation requests, terminal arbitration (durable spine surface).
- Product state: flow-state.json, execute/QA/review/smoke state, `.cleanup-uncertain.json` markers (sprint-flow-state surface), study run state.
- Governed inputs hashed into fingerprints (workspace files).

## 5. Invariants (as implemented)

- Session scoping of the ephemeral hub: status/subscribe require exact session match; cancel matches unless session=="" (server-internal); active list filters by session. Durable fallback reads (status/events/active-list merge/cancel) deliberately ignore browser session (Sprint 35 read visibility rule).
- Confirmation tokens are single-use, ≤2 minutes, bound to (issuing session, canonical request bytes, current fingerprint); expired/mismatched/stale consumption burns the token; replay of the same token after successful start returns the original document via the dedup map without re-consuming (dedup checked before draining/capacity gates).
- Fingerprint authority is server-side only: `ExpectedFingerprint` is stripped from caller input and repopulated by PrepareOperation (app/operations.go:129-132, :215); RunOperation re-prepares and compares before executing (:376-385).
- Per-operation event sequences are strictly monotonic (single mutex-guarded counter); SSE ids decimal; stable event-name allowlist of 8 names mirrored by the frozen browser list (operations_contract_test.go:142).
- All resource classes bounded (constants operations.go:18-32; documented in docs/local-web.md bounds table): 8 active ops, 128 preparations/2 m, 256 events & 256 KiB per op, 16 KiB encoded event, 256 KiB terminal result, 8 subscribers/op, 32 streams, 32-event queues, 10 min retention, 15 s heartbeat, 30 min stream lifetime, 64 KiB bodies, 32 concurrent requests (semaphore includes SSE connections).
- A record reaches a terminal state exactly once: finish and markCleanupUncertain both guard on `terminalOperationState` under the same mutex before mutating/closing done/subscribers; publishAppEvent and appendEvent skip fan-out once terminal. Terminal states: succeeded, failed, cancelled, interrupted, cleanup_uncertain (:616).
- Each record's cancel function is invoked at most once (cancelOnce); drain always uses reason server_shutdown; user cancels canonicalize to user_request unless server_shutdown/timeout/recovery.
- Redaction precedes retention/projection/logging: `token=`, `secret=`, `authorization:`, `cookie:` markers truncate-and-tag `[redacted]`; `/home/` or `C:\Users\` occurrences replace the whole value with `[redacted path]`; 4096-byte field cap; oversized encoded events replaced by warning stubs; oversized results stripped (operations.go:634-658, :442-448, :593-598). Pinned by TestOperationProjectionRedactsBeforeRetention.
- Drain ordering: markers persisted before in-memory cleanup_uncertain projection; new prepares rejected while draining (handler-level isDraining check plus in-lock errServerDraining); queued work cannot exist (capacity rejects, never queues — docs/local-web.md).
- Startup reconciliation must succeed before the listener serves (`ReconcileOperations` failure closes the listener, server.go:76-81).

## 6. Trust boundaries

- Loopback-only binding enforced twice: config string validation and post-listen resolved-address recheck (server.go:43, :68-74); canonicalAuthority requires numeric loopback IP. Documented caveat: loopback is not an auth boundary against same-user processes (docs/local-web.md).
- Every mutation (POST/DELETE under /api/v1/operations, HTML operation forms, run/sprint creates) requires ALL of: valid signed session cookie, exact-Origin match (`validCommandOrigin` = strict equality; documented port-less-Origin fallback additionally requires Sec-Fetch-Site: same-origin AND Referer scheme+host equal to expected, security.go:280-298), CSRF proof (constant-time header compare for API mutations including DELETE; `_csrf` form field for HTML), Host == listener authority.
- Operation reads (GET/HEAD api_operation, api_operation_events) accept absent Origin or same-origin proofs; other GET routes fall back to `validReadRequestOrigin`.
- Request hygiene: single Content-Length or chunked (not both), request target ≤8 KiB, bodies only on designated operationBody routes and capped at 64 KiB via MaxBytesReader, unknown query parameters rejected on non-query routes, method allowlists per route.
- Browser-supplied operation specs never reach execution verbatim: transport re-mapping + charset validation (mapOperationRequest), then app-level normalization/validation, then canonicalization + fingerprint recomputation at start time; the consumed Confirmation's stored canonical/fingerprint must equal the freshly computed ones.
- SSE payloads are redacted projections (see §5); durable event recording happens before fan-out so persisted events carry the same sanitized fields (durable_operations.go:182-195); diagnostics redacted.
- Session secret is crypto/rand with a deterministic fallback derived from authority+timestamp if rand fails (security.go:96-98); HMAC verification uses constant-time compare.

## 7. External effects

- Executes app operations that mutate the workspace (planning stages, flow, execute, review/smoke artifacts, QA state, study run loops) and invoke external harness/provider processes — via shared use cases outside this surface.
- Persists durable run rows/lifecycle/events/cancellation to the run-control store before advertising observability (Accept→Claim→running event before goroutine start; RecordOperationEvent before fan-out; FinishOperation proposes the arbitrated terminal outcome).
- Writes `.cleanup-uncertain.json` markers during drain-deadline expiry (product-owned files under sprints/studies).
- Mutates interrupted-evidence state at startup via ReconcileOperations (under product mutation leases).
- Network: listens loopback; opens no outbound connections itself (runtime adapters do).

## 8. Cancellation / retry / restart / error semantics

Cancellation:
- User: DELETE or HTML cancel → `cancelling` doc returned (202 API / 303 redirect), ctx cancelled once, RunOperation observes ctx and produces result cancelled (finish maps context.Canceled → cancelled); repeated cancel is idempotent (200, requested=false). Durable runs also poll cancellation via controlOperation and AcknowledgeCancellation.
- Shutdown: drainAndWait cancels all non-terminal records once with reason server_shutdown; contract docs/plans/server-shutdown-run-cancellation-contract.md §§3.1-3.6 define the normative sequence (draining → cancel-all-once → bounded wait → truthful terminal/interrupted/cleanup_uncertain → SSE closure → exit).

Retry:
- Same-session same-token start retries return the original document + identical Location (dedup), including while draining or at capacity (gate order §2.3).
- Capacity responses are retryable 429 with Retry-After 2; draining 503 retryable; preparation expiry/mismatch/stale are 409 with re-prepare guidance.

Restart:
- In-memory hub state is lost; recovery reads durable/product state: status/events fall back to run control; legacy `op_` ids answer 410 legacy_operation_not_retained; startup reconciliation converts dead-owner attempts to interrupted evidence and refuses ambiguous cleanup-uncertain sprints (fail-closed).
- SSE reconnect within retention replays from the ring buffer; beyond retention → recovery_required + snapshot (ephemeral) or 409 replay_gap (durable fallback); ahead-of-journal cursors → 409 cursor_ahead.
- Streams self-terminate at 30 min lifetime; clients reconnect with Last-Event-ID.

Errors:
- `writeOperationError` (:788) maps sentinel errors (confirmation family, not-found, capacities, draining) and string-classified causes (validation/incomplete/prerequisite → 422; lock/in progress → 409; unavailable → 424; context cancellation/deadline → 503 server_draining) onto stable codes; unclassified errors collapse to 500 `internal_failure` with cause suppressed; safe causes include redacted reason + guidance for statuses <500. Compatibility table pinned at operations_contract_test.go:179.
- Durable-path failures: Accept/Claim/persist failures abort start (fail closed, no child started); mid-run event-persist failure cancels the record; FinishOperation failures join into runErr (terminal projection still occurs locally).

Timing relationships worth noting (descriptive): `FinishOperation` uses a detached 30 s budget on `context.Background()`, independent of the 10 s drain window; `drainAndWait` waits on `record.done`, which `finish` closes only after that detached finish attempt completes — so a slow repository can consume the whole drain window and trigger the cleanup-uncertain path even though a durable terminal may still be proposed afterwards; conversely markCleanupUncertain makes the late-finishing `h.finish` a no-op in memory. The 30-minute SSE lifetime can hold one of the 32 request-semaphore slots for the stream duration.

## 9. Files, symbols, tests, contracts

Production files:
- internal/web/operations.go — constants/errors/DTOs (:18-103); operationHub (:105); startConfirmed :150; run :221; publishAppEvent :245; finish :279; status :311; activeOperations :322; cancelOperation :347; subscribe :376; appendEventLocked :428; stableOperationEventName :470; drainAndWait :479; persistCleanupUncertain :505; markCleanupUncertain :533; isDraining :559; reapLocked :565; projectOperationResult :577; cloneOperationDocument :602; terminalOperationState :616; canonicalCancelReason :625; safeWebText/safeProjectedText/safeBoundedText :634-658; parseEventID :660.
- internal/web/operation_handlers.go — handleOperationPrepare :78; runtimeConfirmationDTO :124; handleOperationStart :132; handleOperationStatus :169; handleActiveOperations :185; handleOperationCancel :206; handleOperationEvents :230; HTML variants :293/:330/:362/:381; durableOperationStatus :411; legacyOperationID :422; durableOperationDocument :424; durableActiveOperation :437; followDurableOperationEvents :447; durableOperationEvent :516; operationSpecFromForm :536; newSprintRef :555; writeSSEEvent :588; decodeStrictJSON :593; mapOperationRequest :610; mapOperationSpec :744; validOptionalIdentifier :775; writeOperationError :788; safeOperationCause :830; operationFailureGuidance :841; isClientOperationError :862; eventIDHeader :872 (no callers found in package at this commit); logOperation :882.
- internal/web/security.go — trackedWriter :33; securityMiddleware.wrap :102 (session issuance, CSRF derivation, route-class gates :133-170); ambiguous framing :180; signSession/readSession/csrfFor :228-251; applySecurityHeaders :253; origin validators :262-314; preparationStore :368-451; confirmationDedupKey :448; sentinel confirmation errors :453.
- internal/web/server.go — Options/Run :25-154 (shutdown block :131-153; canonicalAuthority :167; timeouts :16-23).
- internal/web/routes.go — HandlerOptions/NewHandler :30-78; method allowlist :252; matchRoute :282; page allowlists :404-429; splitPath :431.
- internal/web/handlers.go — dispatch operation cases :761-786; param validation :427-435; validIdentifier :1035.
- internal/app/operations.go — WebOperations :32; DurableOperationManager :41; AcceptedOperation :47; OperationCleanupRecorder :57; OperationCleanupUncertain :61; OperationReconciler :68; kinds :72-102; OperationRequest (+ExpectedFingerprint authority comment) :115-133; Confirmation :134; OperationEvent :147; OperationResult/Error :196; PrepareOperation :211; validateQAOperationRequest :361; RunOperation :376; normalizeOperationRequest :523; validateOperationScope :541; canonicalOperationRequest :577; operationPrerequisites :586; operationRuntimeIdentity :603; governedOperationInputs :627; fingerprintOperationInputs :663.
- internal/app/web_usecases.go — AcceptOperation :444; FinishOperation :479; RecordOperationEvent :486; Runs/Run/RunEvents/CancelRun wrappers :493-526; PrepareOperation/RunOperation delegation :532-538; RecordOperationCleanupUncertain :540; ReconcileOperations :563.
- internal/app/durable_operations.go — manager :14; AcceptOperation :97; qaOwnershipFromContext :140; RecordOperationEvent (coalescing/omission) :158; controlOperation :223; FinishOperation :266.
- internal/app/serve_commands.go — runServe :18; ValidateLoopbackListen :119; help text documenting graceful-shutdown promise :141.
- internal/app/run_usecases.go — RunUseCases interface :29; repositoryRunUseCases :36.
- internal/app/operation_runner.go — sharedOperationRunner :18.
- internal/web/static/js/sse.js — frozen stable event list + abort-close helper; static/js/operations.js — progressive-enhancement client (not fully traced).

Tests (all deterministic fakes; test_fakes_test.go supplies sampleQueries etc.):
- operations_test.go — lifecycle/session ownership/idempotent cancel :124; drain cancels + rejects new starts :162; deadline persists cleanup uncertainty before terminal projection :181; ninth active op → 429 :209; replay bound + slow-subscriber eviction :234; preparation binding/expiry/replay/capacity :270; full HTTP prepare→start→active→SSE→cancel→status→dedup-replay :302; strict JSON + CSRF rejection :387; kind allowlist :401; redaction-before-retention :413; safe error cause/guidance :434; no-JavaScript HTML flow :448; sprint-ref builder :478; single-stage mapping :497; requested-model projection :578.
- operations_contract_test.go — browser kind↔app-kind table :22; QA map-owned-shard restriction :68; code-context generic stage contract :89; lifecycle states + terminal classification + DTO shape :108; SSE event names/frame shape ↔ frozen js list :142; error-code compatibility table :179.
- sse_test.go — frame format :9; parseEventID :22.
- server_test.go — serve lifecycle, canonical URL, launcher warning, shutdown diagnostics :38; listen policy rejections :87.
- Adjacent: security_test.go (session/CSRF/origin), routes_test.go (method/route shapes), api_compatibility_test.go (executable compatibility manifest), import_boundary_test.go (web must not import product modules).

Contracts:
- CURRENT-CONTRACT: docs/local-web.md (command lifecycle steps 1-7; stable states/event names/error codes; bounds table; performance expectations; Shutdown section describing 10 s drain, exactly-once cancellation, marker persistence, fail-closed startup reconcile); docs/web-compatibility-baseline.md (route/method/status matrix incl. operations endpoints; fixture-gated changes); docs/plans/server-shutdown-run-cancellation-contract.md (normative shutdown sequence §3, outcome taxonomy §3.5, forced termination §5, concurrency rules §6 incl. "shutdown cancellation must not overwrite an already committed authoritative completion", config §7 grace period, required tests §9).
- Planning workspace (CURRENT-CONTRACT): sprints/31-web-operations/requirements.md (guarded operations, SSE, shutdown acceptance criteria; hub ephemerality; no web-owned workflow state); sprints/35-durable-run-observability/requirements.md (durable run identity, cross-surface visibility, replay cursors, lease/heartbeat truthfulness, single-terminal arbitration) — partially realized via DurableOperationManager + durable fallback paths; TRD testing expectations (confirmation expiry/staleness, SSE ordering/reconnect/slow subscribers, cancellation, shutdown, redaction).
- HISTORY/FUTURE-INTENT: Sprint 31 predates durable runs (`op_` ids are the legacy scheme, now answered 410); Sprint 35 defines the durability direction this surface partially implements.

## 10. Immediate surface dependencies

- **durable-operation-spine**: runcontrol Repository/Accept/Claim/Heartbeat/AcknowledgeCancellation/Reconcile/Snapshot/Events; lifecycle IsActive/IsTerminal; LastSequence/OldestRetainedSequence cursor semantics consumed by followDurableOperationEvents and handleActiveOperations; fence tokens carried into operation contexts; ProgressCoalesceWindow/lease durations defined there.
- **shared-usecase-vocabulary**: WebOperations/RunUseCases/DurableOperationManager interfaces; Confirmation/OperationRequest/OperationEvent/OperationResult types; dashboard use cases (PrepareOperation, RunOperation, Validate, QAMap) and sharedOperationRunner; sprint mutation leases and `.cleanup-uncertain.json` markers (owned/consumed by the sprint-flow-state surface); study service equivalents.
- Same-package support: security middleware (session/CSRF/origin gates), templates/static JS (browser client), diagnostics writer.

## 11. Explicit unknowns

- `eventIDHeader` (operation_handlers.go:872) has no caller found in the package at this commit; whether it is vestigial or intended for future use is unresolved.
- Production embedders other than `serve`: the code supports WebOperations implementations without DurableOperationManager (purely ephemeral mode) and ErrWebUnavailable degradation, but whether anything besides tests exercises those modes in production wiring was not surveyed beyond serve_commands.go and tui_commands.go.
- Full client-side behavior of static/js/operations.js (reconnect cadence, cursor management) was not traced; only the frozen event-name contract in sse.js was verified.
- Exact values of runcontrol timing constants (ProgressCoalesceWindow, OwnerTickInterval, HeartbeatInterval, lease durations) belong to the durable spine surface; only their interaction points are described here.
- docs/web-compatibility-baseline.md was sampled (route matrix) but not exhaustively diffed against handler behavior.
- The interplay between the detached 30 s FinishOperation budget, the sequential per-record drain wait, and the shared 10 s ShutdownTimeout under slow repository conditions is described from code but not measured.
- Whether any non-browser local client relies on the absent-Origin read allowance (validOperationReadOrigin) is undocumented in inspected sources.
