# Context Pack: `opencode-agent-runtime` — OpenCode agent runtime adapter and session stores

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: agent-execution-platform. Risk: high. Descriptive only — no defect judgments.
SDK dependency: `github.com/Antonio7098/agentwrap` v0.0.0-20260825130518-dccd575bd101 (go.mod:8), read from the module cache; cited below as `agentwrap@…:file:line`.

## 1. Purpose

Everything below the consumer fakes: `internal/platform/runtime` maps UltraPlan's generic `Request` into
the agentwrap SDK, invokes the `opencode` CLI as a subprocess with a per-run SQLite database, streams and
bounds canonical events, applies retry/backoff/backup-model policy, synthesizes a cancelled result after a
≤5s grace wait that abandons further teardown observation, deletes sessions through SQL executed by the
binary plus WAL checkpoint/VACUUM, and maintains hashed per-owner runtime stores with dead-owner retention,
GC, and XDG log pruning. The package is product-agnostic (no study/sprint types import it back).

## 2. Entrypoints and control flow

- `Adapter.StartRun` (runtime.go:310-419): prepare store (:314-318) → `toAgentwrapRequest` (:319,:562-614) → SDK `StartRun` (:324) → event-collector goroutine (:331-344) + waiter goroutine (:350-354) → select wait vs ctx.Done (:356-381) → map result/events (:383-401) → error/status mapping (:402-418). Every terminal path calls `retainRuntimeStore` (:321,:327,:375,:403,:414,:417).
- `Adapter.Health` (health.go:43-103): delegates to the concrete runtime's `CheckHealth` (`a.health` is the un-wrapped primary opencode runtime, opencode.go:82), overlays capability support locally, downgrades Ready→Unsupported for unsupported required caps (:89-91), errors via `RequiredHealthFailure` (:94) and unsupported-capability check (:97-101). Statuses map ok/warn/fail (:113-122).
- `Adapter.Capabilities` / `ListModels` (runtime.go:526-535; models.go:23-40): pass-through with mapping/type-assert on `agentwrap.ModelLister`; `MaxModelListing=500` (models.go:43).
- `DeleteSession`/`DeleteSessions`/`DeleteRuntimeStore` (runtime.go:269-300): no-op guards for empty ids/paths; batch falls back to per-session loop when no batch hook exists. Hooks are wired only by `NewOpenCode`.
- Factory: `NewOpenCode(c config.Config)` (opencode.go:31-127) builds everything: rate-table store under `os.UserCacheDir()/ultraplan` (:23-29,:32), runtime closure adding config ExtraArgs/Env/StderrLimit and `WithSnapshots(false)` (:33-44), stage-variant router (:46-51), policy stack (:52-81), deletion hooks (:83-125).

## 3. Request/policy mapping into agentwrap (`toAgentwrapRequest`, runtime.go:562-614)

- Health/capability name whitelists → `agentwrap.HealthCheckID`/`Capability`; unknown names error (agentwrap.go:9-36,:38-73; tested runtime_test.go:466-479).
- `PermissionPolicy` → `agentwrap.PermissionPolicy` with tool/path translation and `ValidatePermissionPolicy` before launch (agentwrap.go:75-101); empty policy maps to nil (:76-78).
- Runtime-store plumbing: when `RuntimeStorePath != ""`, metadata gains `opencode.database_path` (`agentwrapopencode.MetadataDatabasePath`) and `runtime_store_owner` (runtime.go:576-582).
- `CacheDirective` becomes metadata only — `prompt_cache_foundation_key`, derived cohort key `"ultraplan-cohort-v1-"+sha256(cohort)[:16]` over {foundation key, provider, model, workdir, sandbox, permissions, policy}, breakpoint bytes, prefix digest, mode (runtime.go:583-597); comment at :46-48 states adapters may apply provider-native support later. Pinned by cache_test.go:5-26.
- `RequestFromConfig` (runtime.go:537-560) builds the base request from config: timeout from `Execution.DefaultTimeout`, provider/model split on first slash from `Models.Primary` else `Models.Default` (:800-807), RequireHealth/RequireCaps/Sandbox/Permissions/policy defaults from `Agentwrap`.
- Result mapping `mapResult`/`mapEvent` (runtime.go:616-668): attempts, usage knownness (:719-750), cost source (:164-171), policy decisions, validation/repair/cleanup summaries (events.go:29-149), context promotion of provider/model/harness (:647-651) and nested observable fields (:675-717).

## 4. Binary invocation and DB isolation (SDK internals)

All in agentwrap@v0.0.0-20260825130518-dccd575bd101 unless noted:

- Validation before launch: session actions fork/replace/release rejected (opencode/runtime.go:31,:1276-1289); provider must not contain `/`, model ≤1 slash (:34,:1291-1301); required preflight runs health checks when `RequireHealth` set (:37,:97-125).
- argv: `opencode run --format json [--dir W] [--model P/M] [--session S] [extraArgs...]` (:128-144); prompt delivered via **stdin** (`Stdin: req.Prompt`, :166; process.go:25). Timeout wraps the run ctx (:42-46).
- Env: subprocess gets `append(os.Environ(), configured env...)` (process.go:22-24); snapshots disabled writes `OPENCODE_CONFIG_CONTENT={"snapshot":false}` (opencode/runtime.go:149-154,:170-189); DB isolation: metadata `opencode.database_path` must be absolute ending in `opencode.db`, then set as `OPENCODE_DB` (:155-160; constant :23-26).
- Process group: linux sets `Setpgid` + `Pdeathsig: SIGTERM` (process_linux.go:15-18); cancellation signals the group TERM, waits 2s, then KILL (process.go:70-97). Stderr is captured into a bounded buffer (process.go:99-131; limit from `WithStderrLimit`, options.go:52-58).
- Event decoding: goroutine scans stdout NDJSON, projects native records into `agentwrap.Event`s into a 32-buffer channel (opencode/runtime.go:308-369,:59), emits lifecycle/session/permission-audit/rate-limit events (:1047-1151); stderr tail, exit code, counts land in `RunMetadata.NativeMetadata` (:493-500).
- Terminal-state ladder `finalResult` (:390-554): final-result seen / idle / clean-exit output heuristic / durable-DB reconcile proof (:458-480) — the reconcile queries `session`/`messages`/`parts`/`assistant_text` via `<exe> db --format json <sql>` with quote-doubling escaping (:648-692) and its assistant text overrides streamed terminal output (:475-480). Usage merges DB rows (:473-474); cost prefers OpenCode-reported values, else prices tokens against the shared `RateTableStore` (:502-509,:614-631; pricing.go:283,:300).
- Wait returns classified results (`classifyContextError` → timeout/cancellation categories, :1262-1267); `Cancel` emits lifecycle cancelled, runs process cleanup once (2s budget), cancels ctx, waits ≤100ms (:262-272,:1022-1032).

## 5. Event pipeline, ring buffer, sanitize/redaction bounds (UltraPlan side)

- Collector keeps the LAST 200 events (`retainedRuntimeEventLimit`, runtime.go:144; shift-ring add :450-470), tracks totals/dropped, dedups ordered SessionIDs (:452-455, appended to result :393-396), samples memory on first/64th event (:456-458), and captures terminal output from payloads to depth 4 (:472-501).
- Payload sanitization bounds (agentwrap.go:121-210): ≤64 fields/map (`_omitted_fields` counter), strings truncated at 8192 bytes with `... [truncated N bytes]` marker (:220-225), slices capped at 16 items with omission marker, depth 3 for nested maps/slices, `[]byte` replaced by `[bytes omitted: N bytes]`, unknown types by `%T omitted`. Terminal output is separately bounded at 96KiB (:124) and preserved un-truncated relative to payload text (pinned runtime_test.go:344-367).
- Raw native payloads are never copied: events record only presence/source/encoding plus omission reason ("raw payload bytes omitted by UltraPlan runtime mapping" / "unsafe raw payload bytes omitted by default", events.go:5-13; `RawOmitted = rawPresent`, runtime.go:663). PersistencePolicy `PersistUnsafeRawPayloads:false` is additionally set at the SDK boundary (opencode.go:80).
- Diagnostics (error details, warnings, attempt details, policy reasons) truncate at 4096 bytes and pass through `config.RedactValue`, which replaces values matching secret markers with `[REDACTED]` (runtime.go:796-798; agentwrap.go:216-218; config/redaction.go:28-48); pinned by runtime_test.go:72-137.

## 6. Retry/backoff/backup-model policy machinery

- Composition inside `NewOpenCode` (opencode.go:73-81): `ObservingRuntime{ ValidatingRuntime{ PolicyRunner{ stageVariantRouter, missingSessionPolicy{ BasicPolicy } } } }` with `PersistencePolicy{PersistUnsafeRawPayloads:false}`. TRD contract order Observing→Validating→PolicyRunner→opencode.Runtime (TRD.md:1149-1162) matches.
- Knobs (opencode.go:52-56): `MaxAttemptsPerTarget = Execution.DefaultRetries + 1`; `ExponentialBackoff{Initial:1s, Factor:2, Max:30s}`; `RetryRateLimits:true`. Backup model (`Models.Backup` ≠ primary) becomes one fallback target named "backup" carrying provider/model override and opencode RuntimeContext (:57-72).
- SDK decision logic (policy.go): stop on ctx/completed/max-elapsed (:143-151); runtime-exit prefers fallback before retries (:156-158); retries require category-retryable (timeout/runtime_exit/runtime_unavailable/provider_unavailable/model_unavailable/malformed_event; rate_limit needs signal) (:248-261); delay prefers provider RetryAfter/reset-at over backoff (:186-201); fallback never for authentication/permission/configuration/cancellation (:263-276); fallback requests start from the ORIGINAL request so primary-run metadata (incl. `OPENCODE_DB`) survives target overlay (:776-831).
- The local `missingSessionPolicy` short-circuits "session not found" (substring match over UserDetail/DebugDetail/ResponseBody/cause, opencode.go:129-149) to `PolicyDecisionStop` without consulting next policy.
- `PolicyRunner` execution (policy.go:398-497): logical run id `policy-N`, per-attempt summaries, decision records, retry/fallback canonical events with injected `policy_run_id`/`attempt`/`target_index` payload keys (:544-562), buffered-64 event channel where overflow records dropped events in metadata (:564-594), cancellable sleep between attempts (:913-925).
- Validation/repair wrapper (validation.go:142-176,:253-313): spec from request override; validate→repair loop bounded by `Repair.MaxAttempts` with same-session continue, optional fresh-session fallback (excluded for cancellation/timeout/permission failures, :419-429), `repair_exhausted` error category (:293-297).

## 7. Cancellation semantics: ≤5s grace waiter and synthesized result

- On ctx.Done while waiting (runtime.go:362-377): calls `run.Cancel(context.Background())` (:363) — background ctx so cancel itself is not deadline-bound; SDK-side this triggers process-group TERM/KILL cleanup (see §4).
- Then selects: if the waiter delivers within 5s, the real result/error is used and mapped (waitErr forced to ctx.Err() when nil, :378-380). If not, StartRun returns a synthesized `Result{Status:"cancelled", FinishedAt:now, Error{Category:"cancellation"}}` (:369-374) plus ctx.Err() — this path carries no Events/EventStats/Memory because the collector goroutine is left running and its channel never drained (teardown observation abandoned; the drain at :385-401 has its own separate 1s cap).
- Post-wait classification (:402-411): `context.Canceled` ⇒ status cancelled/category cancellation; `DeadlineExceeded` ⇒ failed/timeout; other wait errors and `result.Err` re-map via `mapError` (:752-775), which preserves `*agentwrap.SDKError` identity (errors.As) with redacted/truncated details and cause unwrapping.
- Consumer view: controlledRuntime polls durable cancellation every tick and cancels its derived ctx (run_control.go:271-278); `terminalOutcome` maps cancelled/timeout/failed statuses to run-control outcomes (run_control.go:765-777). Pre-cancelled contexts take the fast path (pinned runtime_test.go:481-497).

## 8. Error taxonomy / category mapping

- SDK categories (agentwrap@…/errors.go:13-28): configuration, health, runtime_unavailable, provider_unavailable, model_unavailable, authentication, permission, rate_limit, timeout, cancellation, malformed_event, runtime_exit, validation, repair_exhausted, cleanup, unknown. Opencode adapter classifies start failures as runtime_unavailable (:1243-1245), decode failures as malformed_event (:1247-1253), exit-after-work as runtime_exit with exit_code/stderr debug detail (:1255-1260), DB-unavailability during reconcile as runtime_unavailable (opencode/runtime.go:694-700).
- UltraPlan preserves classification verbatim: returned errors stay `*agentwrap.SDKError` with redacted UserDetail/DebugDetail/ResponseBody (runtime.go:752-775); result-level `Error` mirrors category/operation/provider/model/runtime kind/exit code/signal/RetryAfter/metadata (:777-794). Consumers rely on these strings (e.g., study/run.go:183-196 category switch; sprint/qa.go:809 classifyQARuntimeFailure).

## 9. Session deletion path (SQL-through-binary, checkpoint/VACUUM)

- `deleteSessions` hook (opencode.go:93-125) serializes on package-global `openCodeSessionCleanupMu` (:19). Per session: (1) `DELETE FROM event_sequence WHERE aggregate_id = '<id>'` executed as `<executable> db <query>` (comment :101-103 notes event_sequence has no FK back to session); (2) `<executable> session delete <id>` (:107-111). Both inherit os.Environ() plus configured Agentwrap env (:108,:182).
- SQL string built by interpolation behind `sqliteString`, which doubles single quotes only (opencode.go:186-188; pinned opencode_test.go:79-83).
- After the batch: `PRAGMA wal_checkpoint(TRUNCATE)` retried up to 20× at 250ms until the third-from-last output field is `0` (opencode.go:151-167), then `VACUUM` (:116-118), then a second checkpoint because WAL-mode VACUUM writes the compacted image to the WAL (:119-123). Any step failure aborts the batch with combined command output in the error.
- `DeleteRuntimeStore` hook (:83-92): removes the whole scoped dir via `removeRuntimeStore` (store.go:109-118); on failure marks the record `cleanup_pending` (:85); on success prunes OpenCode logs under the same mutex (:88-91).
- Callers: study service prefers whole-store delete when `result.RuntimeStorePath != ""`, else deletes observed∪result session IDs (study/service.go:43-84); controlledRuntime forwards all three verbs by interface upgrade (run_control.go:105-138).

## 10. Scoped per-owner runtime stores

- Path derivation: `ScopedRuntimeStorePath(scopeRoot, owner)` = `<scopeRoot>/.ultraplan/runtime/opencode/<hex(sha256(trim(owner))[:16])>/opencode.db` (store.go:17,:48-51). Owners: `"study:<name>:<kind>:<dimRef>[:<sourceKind>:<sourceName>]"` (study/run.go:122-127); `"sprint:<project>:<sprint>:<stage>:<task>:<coverage>:<area>"` rooted at `projects/<p>/sprints/<s>` (sprint/service.go:1137-1139).
- Containment guard: paths must be absolute, basename `opencode.db`, and contain the `.ultraplan/runtime/opencode/` marker segment, else mutation/deletion is refused (store.go:120-131; pinned store_test.go:72-76).
- `store.json` lifecycle (schema v1, atomic temp+chmod600+rename writes, store.go:148-170): `prepareRuntimeStore` → state active with PID=os.Getpid(), CreatedAt preserved across reuse (:53-67); every StartRun terminal path → retained, PID 0, LastError recorded (:69-89); interrupted removal → cleanup_pending (:91-107). Inspection lists stores sorted by UpdatedAt with recursive byte sizes (:172-195,:246-258).

## 11. GC / maintenance

- `CleanupRuntimeStores(scopeRoot, maxAge, maxBytes, aggressive)` (store.go:199-237): dead-owner retention first — active stores whose PID fails `kill(pid,0)` and are >30min stale flip to retained, keeping the DB resumable (:211-220; pinned store_test.go:78-101); removal criteria: cleanup_pending ∨ (retained ∧ older than maxAge) ∨ quota breach (total>maxBytes, non-active) ∨ aggressive∧non-active; live-PID active stores survive even aggressive sweeps (:223; pinned store_test.go:103-121). PID liveness is `syscall.Kill(pid,0)` (:239-244).
- Sprint wiring: `startSprintRuntime` runs `CleanupRuntimeStores(sp.Path, 72h, 2GiB, false)` before admitting each run; return value discarded (sprint/runtime_metrics.go:116-125).
- Log pruning (opencode_maintenance.go): data root from last `XDG_DATA_HOME=` in configured env else `$HOME/.local/share` (:25-35,:79-87); targets `<root>/opencode/log`; removes regular non-symlink files older than 48h or, under the 128MiB cap, oldest-first but only if untouched for >10 minutes (:14-17,:37-77; pinned opencode_maintenance_test.go:10-44).

## 12. Inputs / outputs

Inputs: effective config (Agentwrap executable/env/extra args/stderr limit/permissions, Models primary/backup, Execution timeout/retries), composed prompts (delivered on stdin), optional SessionID/SessionAction ("continue"/"fresh"), Validation specs from study/sprint callers, Metadata (trace/task/store-owner/variant/cache keys), wall clock, os.Environ().
Outputs: `Result` (status, terminal output, sanitized events + stats, memory stats, artifacts, warnings, attempt/policy/validation/repair/permission/cleanup summaries, usage, cost estimate with source provenance, mapped Error), `OnEvent` callback stream, scoped `.ultraplan/runtime/opencode/<hash>/` dirs containing `opencode.db`(+-wal) + `store.json`, XDG-deleted log files, run-control events via consumer wrapping.

## 13. Authoritative state and ownership boundaries

- Runtime-owned durable state: the hashed store dirs and their `opencode.db` (OpenCode's own schema, written through the binary), plus product-written `store.json` records. Product code touches the DB exclusively through `<executable> db <sql>` / `<executable> session delete` — no direct sqlite driver exists in the package.
- Product-owned state lives elsewhere: reports/artifacts (consumers), run-control DB (run_control.go), run-state sessions (run-loop surface). This surface only holds transient request/result copies.
- SDK-owned in-process state: rate-table cache under `os.UserCacheDir()/ultraplan` (opencode.go:23-29) and per-run buffers; the `ObservingRuntime` here carries no Store/Sinks (opencode.go:73-81), so SDK-level persistence is limited to raw-payload policy enforcement.
- Package-global mutable state: `openCodeSessionCleanupMu` (opencode.go:19) serializes deletions/pruning process-wide; `studyRuntimeFactory` is a swappable var for tests (study_commands.go:22-24).

## 14. Invariants (as implemented)

1. Every StartRun terminal path leaves a store.json record: prepared→active, finished→retained (with LastError when applicable); removal failure downgrades to cleanup_pending (runtime.go:314-418; store.go:53-107).
2. Store mutation/deletion refuses any path outside managed `.ultraplan/runtime/opencode/**/opencode.db` shape (store.go:120-131).
3. Mapped events/results never include raw payload bytes; presence is recorded instead (events.go:5-27; runtime.go:643-667).
4. All mapped diagnostics are ≤4096-byte truncated and secret-marker redacted; payload strings ≤8192 bytes; terminal output ≤96KiB; ring retains exactly the newest 200 events (agentwrap.go:121-225; runtime.go:144).
5. "session not found" stops retry processing immediately; it never reaches BasicPolicy (opencode.go:133-138).
6. Backup-model fallback preserves the original request's metadata, hence the same OPENCODE_DB-scoped database (policy.go:776-831; opencode.go:59-71 overrides only Provider/Model/context).
7. Cancellation always attempts `run.Cancel` regardless of waiter outcome; after 5s the caller receives a complete-but-eventless cancelled result rather than blocking (runtime.go:362-381).
8. Deletion batches checkpoint→VACUUM→checkpoint so subsequent waves find a compacted main DB file (opencode.go:113-123).
9. Dead owners' stores are retained, not deleted, by GC; only explicit retention expiry/quota/aggressive flags remove them (store.go:211-226).
10. Unknown health-check or capability names fail request construction before any launch (agentwrap.go:31-33,:67-70).

## 15. Trust boundaries

- Real LLM/subprocess output enters as NDJSON stdout events, stderr tails, exit codes, and `opencode db` query results; all are projected, size-bounded, and secret-redacted before leaving the adapter (§4-§5, §8). The DB-reconcile path treats binary JSON output as data and re-parses it defensively (opencode/runtime.go:694-727).
- Session IDs observed from events flow back verbatim into `--session` args and `aggregate_id = '<escaped-id>'` SQL; the only transformation is single-quote doubling (opencode.go:103,:107; sqliteString :186-188). Other SQLite literal contexts receive no additional escaping helper.
- GC `RemoveAll` deletes the entire hash-named directory adjacent to live agent databases; scope relies on the hash derivation and containment marker, and on `kill(pid,0)` PID liveness checks (store.go:109-118,:211-244).
- Config-provided env entries and extra args pass through to the subprocess unchanged except snapshot/DB overrides; secrets inherited from the environment are not parsed by this package (redaction happens only on outbound diagnostics).
- Log pruning deletes files under an XDG-derived directory chosen from config-supplied env text; symlinked entries are skipped (opencode_maintenance.go:48-56).

## 16. Immediate surface dependencies (one–two hops)

- Upstream consumers: internal/study (run.go:108-181 builds store-scoped validated requests; service.go:43-84 deletion; run_loop/run_all/synthesize call sites), internal/sprint (service.go:1122-1195 request construction incl. variant/cache directives; runtime_metrics.go:116-125 pre-run GC; review.go:906-913 deny-by-default review policy; execute.go:253, qa.go:809, smoke_author.go:61-69 event/policy hooks), internal/app composition (study_commands.go:22-24,:330-351; sprint_commands.go:25,:690-696; serve_commands.go:51; health_commands.go:112-149; web_usecases.go:115,:399-400 ListModels clamp).
- Wrapping layer: controlledRuntime (run_control.go:97-343) adds durable acceptance/claim/fencing/event coalescing/terminal proposals around every StartRun and forwards the three delete verbs.
- Downstream: agentwrap SDK only (single dependency, go.mod:8); the `opencode` binary itself is an external executable resolved by name/path from config.
- Config foundation: `config.Config.Agentwrap/Models/Execution` fields and `config.RedactValue`.

## 17. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace `projects/ultraplan-go/docs/TRD.md`:
- L14/L48/L1118-1143: must use agentwrap + `agentwrap/opencode`; must not reimplement runtime contract, process handling, canonical events, policy, validation, repair, permission translation, health, observability.
- L100/L114-115: platform runtime stays generic; study/sprint behavior stays out of adapters.
- L1145-1162 (§11.2): required composition ObservingRuntime→ValidatingRuntime→PolicyRunner→opencode.Runtime; matches opencode.go:73-81.
- L1177-1180 (§11.3): RunRequest field mapping incl. metadata keys and session fields only for intentional continuation.
- L1181-1202 (§11.4): must use documented `opencode.NewRuntime` options and rely on the adapter for `opencode run --format json` launch; "must not parse OpenCode stdout/stderr directly" (L1202) — the deletion SQL path uses `<exe> db <sql>` subcommands rather than stdout parsing, consistent with the letter of L154 (TRD.md: subprocess execution acceptable for behavior owned by platform/runtime and agentwrap).
- L1248-1252 (§12.3): unsafe raw payload bytes must not be persisted; omission reasons preserved — implemented per §5.
From `docs/ARCHITECTURE.md`: L35 "internal/platform/runtime owns generic execution only"; L99 expected files runtime.go/agentwrap.go/opencode.go; L388-400 platform/runtime placement rationale; L425-427 dependency arrows (study,sprint → platform/runtime → no product modules).
From sprints:
- `09-runtime-integration/requirements.md`:L40-41 composition + no direct launch/parse; :49 retry delegated to PolicyRunner; :53 canonical kinds; :55 raw-payload non-persistence; :60 fake-runtime coverage list.
- `12-durable-run-loop/requirements.md`:L50 cancellation propagates through the platform boundary; :53,:81,:115 retry/fallback/usage/cost metadata preserved from runtime results, not parsed from process text.
- `26-review-stage/requirements.md`:L73 no retry-policy reimplementation outside agentwrap.
No CURRENT-CONTRACT doc found governing `store.json` states, store hashing, or the SQL/checkpoint deletion sequence (see Unknowns 1).

## 18. Tests (evidence map)

internal/platform/runtime tests (package-internal fakes fakeRuntime/fakeRun, runtime_test.go:499-544):
- runtime_test.go — TestAdapterStartRunMapsEventsUsageAndError (:15-53) pins event kind mapping, context promotion (provider/model/harness), RawOmitted, usage knownness. TestAdapterPreservesSDKErrorClassification (:55-70). TestAdapterBoundsSDKErrorAndPolicyDiagnostics (:72-106) pins 4096-byte diagnostic bound + truncation marker across error/result/policy-detail surfaces. TestAdapterRedactsSDKErrorDetails (:108-137) pins `[REDACTED]` for user/debug/response-body. TestAdapterMapsSanitizedAttemptErrorDetail (:139-154). TestAdapterMapsAllCanonicalEventKinds (:156-194) covers all 17 kinds through mapEvent. TestAdapterMapsCancellationAndTimeoutFailures (:196-228). TestAdapterMapsMalformedEventFailureSafely (:230-256). TestAdapterRetainsOnlyRecentRuntimeEvents (:258-297) pins ring semantics (last 200 kept, 5 dropped, warning emitted, deduped session order, memory sampling). TestAdapterBoundsMappedEventPayloads (:299-342) pins string truncation, byte omission, OnEvent-path bounding. TestAdapterPreservesBoundedTerminalOutputApartFromMappedEvents (:344-367). TestAdapterMapsPolicyRetryFallbackAndValidationMetadata (:369-451) pins attempt/policy/permission/cleanup/repair/usage/cost mapping. TestPermissionPathRulesMapToAdapterPolicy (:453-464). TestHealthAndCapabilityNameValidation (:466-479). TestStartRunCancelsUnderlyingRunWhenContextCancelled (:481-497) pins Cancel invocation + cancelled status on the fast path.
- opencode_test.go — TestMissingSessionPolicyStopsWithoutRetrying (:20-28); TestRequestVariantRuntimeRoutesStageVariant (:43-68) pins trimmed-variant routing and base fallback; TestRequestVariantRuntimeUsesBaseCapabilities (:70-77); TestSQLiteStringEscapesSessionID (:79-83) pins quote-doubling only.
- store_test.go — TestRuntimeStoreLifecycle (:10-40) pins hash-scoped path, byte counting incl. -wal, retain transition, RemoveAll of the dir; TestCleanupRuntimeStoresRetriesPendingAndRemovesExpiredStores (:42-70); TestRemoveRuntimeStoreRejectsPathsOutsideManagedRoot (:72-76); TestCleanupRuntimeStoresPreservesAnInterruptedStoreForResume (:78-101) pins dead-PID→retained; TestAggressiveCleanupSacrificesRetainedButNotLiveStores (:103-121).
- opencode_maintenance_test.go — TestPruneLogDirectoryExpiresAndCapsInactiveLogs (:10-39) pins age-expiry and quota eviction sparing recently-active files; TestEnvValueUsesLastOverride (:41-44).
- cache_test.go — TestAgentwrapRequestReceivesCohortScopedCacheMetadata (:5-26) pins cohort-key derivation/divergence; the code under test lives in runtime.go's `toAgentwrapRequest` (:583-597) — there is no separate cache source file in the package despite the test filename.
Coverage-gap notes (descriptive): no test drives Adapter.DeleteSessions/DeleteRuntimeStore end-to-end (SQL construction beyond sqliteString, checkpoint loop, VACUUM sequencing, prune-on-success wiring are unpinned); the 5s-grace synthesized-result branch (:368-377) has no test (only the pre-cancelled fast path); NewOpenCode factory wiring (stack order, backup-model fallback, env/args propagation) has no direct unit test; SDK-internal behavior is exercised only via fakes at this seam.

## 19. Explicit unknowns / open questions

1. No workspace CURRENT-CONTRACT document was found describing the scoped-store hashing scheme, `store.json` state machine, dead-owner retention windows, or the SQL-through-binary deletion/checkpoint/VACUUM sequence; they exist only in code (TRD §12.3 governs a different persistence concern — raw payload omission).
2. The external `opencode` binary's `session delete`, `db <sql>`, and `--variant` flag semantics are outside both repositories; the adapter treats them as stable CLI contracts (agentwrap options/opencode.go wiring), and no vendored specification exists in the module cache.
3. Whether any provider consumes the `prompt_cache_*` metadata keys is undetermined in-tree; CacheDirective is metadata-only by design comment (runtime.go:46-48).
4. Mid-batch deletion failure leaves earlier sessions deleted and later ones intact with no rollback record beyond the returned error (opencode.go:97-112 ordering).
5. The 5s-grace branch abandons the event collector goroutine and channel; whether late SDK events are simply dropped (GC'd with the handle) is unobserved by any test or counter.
6. `CleanupRuntimeStores` return values are discarded at the only production call site (sprint/runtime_metrics.go:119); removal outcomes are visible only on the store filesystem.
7. PID-based liveness (`kill(pid,0)`) cannot distinguish a reused PID from a live owner (store.go:239-244); the 30-minute staleness window is the compensating delay.
8. `envValue` reads the pruning root from configured env text while the subprocess sees `os.Environ()+env`; divergence between the two resolutions is possible if the ambient environment differs (opencode_maintenance.go:25-35 vs process.go:22-24).
9. Health checks bypass the policy/validation wrappers (`adapter.health = primary`, opencode.go:82); capability downgrade logic is local (health.go:89-91) and independent of SDK RequiredHealthFailure semantics.
10. `sqliteString` handles single quotes only; identifiers/other literal contexts in the two deletion statements rely on fixed SQL shapes around the escaped value (opencode.go:103,:186-188).
11. Two same-shaped SQL escapers exist independently: internal/platform/runtime/opencode.go:186 and agentwrap opencode/runtime.go:690 — duplication across repo boundaries with no shared helper.
12. Sprint stage `variant` metadata keys drive `--variant` argv injection (sprint/service.go:1177-1179 → opencode.go:49,:199-204); the accepted value space is defined by the external binary, not by either repo.

— End of context pack. Descriptive only; no defect claims made or implied.
