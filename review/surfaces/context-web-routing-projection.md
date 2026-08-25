# Context Pack: `web-routing-projection` — Web routing, pages, and API projection

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen; working tree clean except untracked `.ultraplan/` tool state).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: operator-interfaces. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

`internal/web` is the loopback HTTP transport adapter of the `ultraplan serve` dashboard. It hand-routes ~48 named endpoints (no framework router): server-rendered HTML pages for workspace/project/sprint/study/run/QA/artifact/operation state, a versioned `/api/v1` JSON API projecting the same app-layer results through `{data,meta}` envelopes, an allowlisted embedded static asset set, and SSE progress endpoints owned by the sibling surface (`web-operation-hub-sse`). It owns: request matching, method allowlists, query-parameter allowlists, identifier/opaque-ref validation, transport security middleware, HTML/template rendering with layered template validation, JSON DTO mapping, bounded artifact preview rendering, and the frozen route/method/DTO compatibility baseline (`internal/web/api_compatibility_test.go` + `docs/web-compatibility-baseline.md`). It owns no product state machines, no filesystem discovery, no runtime/process construction, and no durable recovery rules (TRD §18A, ultraplan-workspace TRD.md:2093-2106).

Read-only projections dominate: every GET route maps one app query call into HTML or JSON. The only non-GET projections on this surface are the guarded mutation form posts (`sprint_create`, `run_cancel`) whose operation hub mechanics belong to the sibling surface; here they appear as route entries, CSRF/session gates, and redirect/error rendering.

## 2. Entrypoints and control flow

### 2.1 Composition and startup

- `cmd/ultraplan/main.go:30-37` injects `WebRunner`; `ultraplan serve` (`internal/app/serve_commands.go:18-99`) validates loopback listen (`ValidateLoopbackListen` :119), builds `NewWebUseCases(root, WebUseCaseOptions{...})` with run-control repository + durable-operation manager, then calls it.
- `web.Run` (`internal/web/server.go:39-154`): re-validates loopback + `DefaultServerPolicy` (:46), listens, derives canonical numeric-loopback `authority` (:68-74, `canonicalAuthority` :167 rejects non-numeric/non-loopback), reconciles interrupted operations via optional `app.OperationReconciler` (:76-81), creates the operation root context (:83), builds handler via `NewHandler` (`routes.go:53-78`), prints `Dashboard: <origin>/` to stdout (:109), serves in a goroutine, and on serve-context cancellation runs `hub.drainAndWait` → `cancelOperations()` → `server.Shutdown` in that order (:131-153).
- `NewHandler` parses the embedded template tree at construction (`parseTemplateTree` routes.go:80-92) — duplicate-definition rejection (:94-125) plus hierarchy validation (:127-188: namespace layers primitive(0)<component(1)<layout(2)<page(3), downward-only dependencies, cycle rejection, 16 required templates). Template/static assets are `//go:embed` (routes.go:20-21).
- Server timeouts: ReadHeaderTimeout 5s, ReadTimeout 15s, WriteTimeout 30s, IdleTimeout 60s, ShutdownTimeout 10s, MaxInFlight 32 (server.go:16-23). In-flight gate: semaphore acquire or `503 unavailable` on client-context cancellation (security.go:121-131).

### 2.2 Per-request chain

Every request passes `securityMiddleware.wrap` (security.go:102-178) before `handler.ServeHTTP` (routes.go:226-250):

1. Security headers set first (`applySecurityHeaders` :253-260): `Cache-Control: no-store`, CSP `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'`, `Referrer-Policy: same-origin`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Permissions-Policy`. `X-Request-ID` (server-generated hex) added; caller-supplied IDs are never trusted.
2. Session bootstrap: HMAC-signed cookie `ultraplan_session` = `<32-hex>.<HMAC-SHA256(secret,"session:"+id)>`, HttpOnly, SameSite=Strict, Path=/, MaxAge 3600 (:113). Invalid/absent cookie ⇒ fresh session minted and Set-Cookie issued on this response. CSRF token = `HMAC(secret,"csrf:"+session)` exposed to pages via `X-CSRF-Token` response header on *every* response (:116) and injected into page models at render time (`render`, handlers.go:1130-1131).
3. Rejection switch, evaluated in order (security.go:144-164):
   - RequestURI > 8 KiB → 400 `invalid_request`.
   - Ambiguous framing: duplicate `Content-Length`, or `Content-Length` + `Transfer-Encoding` → 400 (:180-183).
   - `r.Host != m.authority` (exact string compare against the canonical numeric loopback authority) → 403 `host_rejected` (:149).
   - Mutation classification (:134-143): API op mutations (POST/DELETE under `/api/v1/operations`), `DELETE api_run`, HTML POSTs under `/operations/`, `sprint_create`, `run_cancel`. Mutations require a *valid signed session* (fresh-minted sessions do not qualify) then exact-origin check; API op/run mutations additionally require header `X-CSRF-Token == csrf(session)` constant-time compare (:155); HTML mutations instead verify `_csrf` form field inside each handler (:handlers.go:1315, run_handlers.go:890, operation_handlers.go:294/331/382).
   - Operation reads (`api_operation`, `api_operation_events` GET/HEAD) and all other non-static requests enforce Origin checks (:157-160); static assets are exempt from Origin validation (:141,159). Origin rule: absent Origin always allowed on reads; present Origin must exactly equal `http://<authority>` OR satisfy the port-less fallback — `Sec-Fetch-Site: same-origin` AND Origin parses to same scheme+hostname with empty port while expected has a port AND `Referer` scheme://host equals expected origin (:280-298).
   - Body policy: `Content-Length > 64 KiB` → 413; any body on a non-operation-body route → 400; operation-body routes get `http.MaxBytesReader` at 64 KiB (:161-168). Operation-body routes: the seven prepare/start/cancel/sprint_create/run_cancel names (:143).
4. `handler.ServeHTTP`: `matchRoute(path)` again independently (:227); unknown path → 404 (JSON envelope if `/api/` prefix, HTML error page otherwise, :228-235); method not in per-route allowlist (`allowedMethods` :252-271) → 405 with `Allow` header; HEAD served through body-discarding `headResponseWriter` (:242-244, 438-440); static → allowlisted embed read (:245-247, 442-457); else `dispatch`.

### 2.3 Route table (matchRoute, routes.go:282-402)

Exact matches: `/` dashboard, `/projects`, `/studies`, `/runs`, `/api/v1/{dashboard,projects,studies,validations,models,health,operations/prepare,operations,runs,timeline}`, `/operations/prepare`, `/operations/start`.
Pattern segments (splitPath requires no trailing slash; trailing-slash paths fall to unknown→404):

| Pattern | Name | Notes |
| --- | --- | --- |
| /projects/{p}/(documentation\|artifacts)/{ref} | project_artifact | all params opaque-ref validated |
| /projects/{p}/sprints/{s}/artifacts/{ref} | sprint_artifact | all params opaque-ref validated |
| /projects/{p}/sprints/{s}/qa[/shards/{x}\|/theories/{x}] | sprint_qa[_shard/_theory] | HTML QA pages |
| /projects/{p}/{page} | project_page | page ∈ roadmap,sprints,documentation,artifacts,operations,validation |
| /projects/{p} ; /projects/{p}/sprints/create | project ; sprint_create | sprint_create POST-only |
| /projects/{p}/sprints/{s}/{page} ; …/{s} | sprint_page ; sprint | page ∈ workflow,run,artifacts,plan,delivery,operations,validation |
| /studies/{st}/{page} ; /studies/{st} | study_page ; study | page ∈ inputs,progress,results,operations,validation,dimensions,reports,repos |
| /artifacts/{ref} ; /operations/{id} ; /runs/{id} | artifact ; operation ; run | |
| /runs/{id}/cancel ; /operations/{id}/cancel | run_cancel ; operation_cancel | POST-only |
| /api/v1/projects/{p}[/sprints/{s}[/qa[/map\|synthesis\|shards/{x}\|theories/{x}]\|/prompts/{stage}]] | api_project…api_prompt_bundle | segment-count matched |
| /api/v1/studies/{st}[/resources] ; /api/v1/artifacts/{ref} | api_study(_resources) ; api_artifact | |
| /api/v1/operations/{id}[/events] ; /api/v1/runs/{id}[/events] | api_operation(_events) ; api_run(_events) | events = SSE |
| /static/{name} | static | name must key `staticAssetNames` map (routes.go:23-28) |

Method allowlists (routes.go:252-271): default GET+HEAD everywhere; POST-only prepare/start/cancel/create; `api_operations` GET/HEAD/POST; `api_operation` GET/DELETE; `api_run` GET/HEAD/DELETE; `api_run_events` GET/HEAD.

### 2.4 Dispatch (handlers.go:421-790)

1. Query allowlist: only `api_validations`, `api_runs`, `api_run_events`, `runs`, `api_timeline` may carry a RawQuery at all (:422-426); everything else with any query → 400 `invalid_request`. Allowed routes re-validate per-key inside their handlers (`onlyQueryKeys`, run_handlers.go:1064-1075: allowed keys only, exactly one value each).
2. Identifier validation for every path param (:427-436): `validIdentifier` (handlers.go:1035-1038: 1..128 bytes, not `.`/`..`, no `/` or `\`, regex `^[A-Za-z0-9][A-Za-z0-9._-]*$`); if the route name contains "artifact", ALL its params use `validOpaqueRef` (`^[A-Za-z0-9_-]+$`, ≤128 B) instead — including project/sprint segments of project_artifact/sprint_artifact.
3. One case per route name; HTML cases call one `app.WebQueries` method then `render`; API cases call the same query then `writeSuccess`. Capability-gated routes type-assert additive interfaces on `h.queries`: `WebModelQueries` (api_models), `WebPromptQueries` (api_prompt_bundle), `WebResourceQueries` (api_study_resources), `WebStudyReportQueries`/`WebDimensionQueries` (study reports/dimensions cards), `WebSprintUsageQueries` (sprint flow usage); absence maps to `app.ErrWebUnavailable` → 503 rather than compile-time coupling.
4. `h.runs` (RunUseCases) nil-safe: run routes render/write 503 `run_control_unavailable` when absent. `h.qa` (`app.QAQueries`, asserted from Operations then Queries at NewHandler routes.go:71-74) likewise guards QA handlers (qa_handlers.go).

### 2.5 Page assembly details worth knowing

- Page aliases collapse legacy paths: project artifacts/documentation→documentation, operations/validation→overview, sprints→roadmap (with reversed sprint order, handlers.go:460-477); sprint run/plan/delivery/operations/validation→workflow (:498-508); study dimensions→inputs, operations→progress, validation→overview, reports/repos→results (:552-600).
- Study "inputs" renders dimension cards via `safeMarkdown(item.Content)` (dimensionCard.Body as `template.HTML`); "results" builds report link groups and the repo leaderboard/matrix/champions from `StudyRepos` (:838-924).
- Artifact pages call `validateArtifact` (artifacts.go:9-20: media ∈ {text/markdown, application/json}; ReturnedBytes within [0, WebPreviewByteLimit] and equal to len(Content); SizeBytes ≥ ReturnedBytes; Truncated ⇔ SizeBytes > ReturnedBytes) before rendering `renderMarkdown` → `app.RenderSafeMarkdown` wrapped as trusted `template.HTML` (handlers.go:801-814); markdown render error degrades to empty HTML silently.
- Run detail page projects journal facts only: lifecycle/liveness/cancellation cue tables (run_handlers.go:777-825), retained-sequence history note, ≤200 most-recent-retained events rendered via `newRunEventView` (payload string keys extracted; free text truncated at 160 chars; tool_arguments/result/error pretty-printed when valid JSON, :727-759); study insights joined from `queries.Study`, sprint usage from `SprintRuntimeUsage`, QA insights from `h.qa.QAStatus/QASynthesis` when target kind is qa-start/resume (:353-371, 383-438).
- Usage aggregation (`runUsageView` :138-220): known-flag discipline per token class; cache-hit percentage only when input/cacheRead/cacheWrite known for every contributing task; cost sums provider-reported and model-priced rows with `*` suffix and per-stage provenance notes ("mixed"/"rate-table estimate"/"provider reported").
- Sprint create (HTML mutation): `_csrf` form check → `WebSprintWorkspaceMutation` capability → identifier validation of project + slug → `CreateSprintWorkspace` → 303 redirect to `/projects/{p}/sprints/{slug}` (handlers.go:1314-1334). Run cancel HTML: `_csrf` check → `runs.CancelRun(id,"user_requested")` → 303 to `/runs/{id}`; failure renders 409 error page (run_handlers.go:889-903).

## 3. Inputs and outputs

Inputs (all untrusted): URL path (segment-matched), query parameters (allowlisted per route; timeline: exactly one of sprint/study, window ∈ {6h,24h,7d,30d} default 24h, limit 1..50 default 20 — timeline_handlers.go:49-97; api_runs: project/sprint/study/lifecycle CSV of valid `RunLifecycle`s/limit 1..200/after — run_handlers.go:249-282; validations: exactly scope∈{workspace,project,sprint,study}+opaque ref — handlers.go:1016-1033; api_run_events: `after` uint64 XOR `Last-Event-ID` header, both → 400 `cursor_conflict`, run_handlers.go:931-991), headers (Host, Origin, Referer, Sec-Fetch-Site, X-CSRF-Token, Last-Event-ID, Accept, Content-Type, Content-Length, Transfer-Encoding), cookies (`ultraplan_session`), and bodies (JSON prepare/start with strict decode: Content-Type must be application/json, DisallowUnknownFields, exactly one value — operation_handlers.go:593-608; urlencoded forms for HTML mutations parsed via ParseForm).

Operation spec mapping (`mapOperationRequest` :610-742): scope identifiers via `validOptionalIdentifier`; ~27 kind aliases normalized (underscore→dash); family scope rules (validate needs exactly one of project/study; `study-*` needs only study; others need project+sprint; QA kinds reject every option except shard, shard valid only for start/resume).

Outputs: `{data, meta}` success envelope and `{error:{code,message,retryable?,details?}, meta}` error envelope (handlers.go:25-49,1084-1114) with `meta.api_version="v1"`, server request id, RFC3339Nano generated_at, and collection triad returned_count/total_count/truncated when a `CollectionInfo` exists. JSON written through buffered encoder with SetEscapeHTML(true) and explicit Content-Length. HTML through buffered template execution; execution failure → bare `http.Error` 500 text/plain (handlers.go:1130-1141). Policy rejections before dispatch use `writePolicyError` (security.go:1116-1124): JSON envelope for `/api/` prefixes, minimal inline escaped HTML otherwise. DTO field sets are frozen by reflection-based schema assertions (api_compatibility_test.go:51-81).

Static assets: exact-name allowlist, Content-Type css/js by suffix, `Cache-Control: public, max-age=0, must-revalidate`, explicit Content-Length; names not content-addressed (routes.go:442-457; baseline doc).

## 4. Authoritative state vs transport state

Authoritative state lives entirely outside the web package: workspace files via `dashboardUseCases`/sprint/study/project services, durable run journal via `runcontrol` repository (`repositoryRunUseCases`), QA persistence via `dashboardUseCases` QA methods, config via effective workspace config. The web package holds only process-lifetime transport state:

- `securityMiddleware.secret [32]byte` — crypto-rand at construction; deterministic fallback `sha256(authority+now)` only if rand fails (security.go:96-98). Sessions, CSRF tokens derive from it; restart invalidates all sessions.
- `preparationStore.records` — TTL'd (2 min) single-use confirmation records, cap 128 post-reap (security.go:396-446).
- `operationHub.records/dedup` — ephemeral operation docs/events/subscribers, terminal retention 10 min (operations.go).
- `webUseCases.secret` + `refs map[string]webRefTarget` — artifact/project/sprint/study ref mint map: `issue()` computes `base64url(HMAC-SHA256(processRandomSecret, kind+"\0"+value...))` AND stores the mapping; `resolve()` requires map membership plus kind equality (web_usecases.go:1503-1523). Refs are minted per response during listing/detail projections (webProject :1372, webSprint :1391, webStudy :1459, webArtifacts :1480-1501 capped at 200 links per collection) and never reaped; artifact preview resolves ref→relative path→containment-checked absolute file (`containedArtifactPath` :1525-1543: `workspace.ResolveInside` + EvalSymlinks on root and file + Rel-prefix escape check), regular-file stat, ≤256 KiB bounded read (`Artifact` :1300-1347, `WebPreviewByteLimit` :30). Both secret and map die on process restart ⇒ previously issued refs resolve to 404 after restart.

## 5. Invariants observable in code

- Unknown `/api/*` always yields the JSON envelope; unknown browser paths yield the HTML error page (routes.go:228-235, 401; writePolicyError branches on path prefix).
- Every success/error JSON carries meta.api_version/request_id; collections carry the truncation triad; empty collections serialize `[]` via `nonNilSlice`.
- Method/query/body/identifier/origin/host limits precede any app-layer call; app calls receive `r.Context()`.
- Artifact previews can only be reached through refs that were minted by this process AND pass media-type/size/truncation contract checks (`validateArtifact` re-checks the usecase result at both HTML and API entry points, handlers.go:608,724).
- Markdown reaching templates is exclusively the output of `app.RenderSafeMarkdown` (goldmark GFM default renderer: raw HTML omitted, unsafe destinations filtered — internal/app/markdown.go:15-22), marked trusted once at two call sites (handlers.go:813, 973).
- Timeline carries only sanitized lifecycle/bounds/timestamps; raw payloads never leave the repository (timeline_handlers.go:12-15 comment; only committed-at timestamps of tool-bearing events projected).
- Event/result projections pass `safeWebText`/`safeProjectedText`: trim, truncate (4096 chars / half-of-256KiB content), redact `token=`/`secret=`/`authorization:`/`cookie:` markers and `/home/` or `C:\Users\` substrings (operations.go:634-658); oversized encoded events replaced by a warning stub; per-op event ring capped at 256 events/256 KiB.
- Compatibility freeze: route/method matrix and DTO tag schemas fail tests on change (api_compatibility_test.go:14-99); changes require rationale in docs/web-compatibility-baseline.md.

## 6. Trust boundaries

- Browser/network side: Host must byte-equal the canonical numeric loopback authority; Origin policy as §2.2; session cookie integrity rests on HMAC of the process-secret; CSRF proof required for API mutations (header) and HTML mutations (form field); static assets bypass Origin checks; reads tolerate absent Origin (curl/top-level navigation model) but validate present ones.
- Workspace-content side: agent-/human-authored markdown reaches HTML only via RenderSafeMarkdown + html/template auto-escaping elsewhere; artifact JSON is served as data with nosniff; display paths are workspace-relative slash paths; artifact content is size-bounded; containment double-checked after symlink resolution.
- App-boundary side: web trusts typed app results (e.g., DisplayPath, findings text) and applies only generic redaction on operation event/result projections, not on query projections; capability interfaces gate optional features.

## 7. External effects

Within this surface: stdout line `Dashboard: <origin>/` and diagnostics lines (`event=http_request …` with normalized route name, status, duration, bytes; `event=security_rejection …`; `event=server_started/server_stopped`; `logOperation` lines event=request_id=operation_id=kind=state=reason) to the diagnostics writer (stderr in production). Cookie issuance. Filesystem reads (templates embedded; artifact/stat/workspace-marker reads via app). The actual mutations (sprint workspace creation, run cancellation, operation execution) are delegated to app use cases and detailed in the sibling packs.

## 8. Cancellation, retry, restart, error semantics

- Request context flows into every app call; MaxInFull semaphore rejection returns 503 `unavailable` when the client is already gone (security.go:128-130).
- Error mapping: `handleQueryError` (handlers.go:1044-1068) — QA typed errors first (AsQAUseCaseError: 409 conflict / 503 persistence·runtime / else 422, with category/operation/guidance/component/correlation_id details); `ErrWebNotFound`→404; `ErrWebUnavailable`, `context.Canceled`, `context.DeadlineExceeded`→503 `unavailable`; else 500 `internal_error` (cause stays out of responses). Run-control errors map via sentinel identity (run_handlers.go:1049-1062: 400/404/409/503-schema/503-default). Operation errors classify through sentinel errors plus substring sniffing ("validation/incomplete/prerequisite"→422; "lock/in progress"→409 retryable; "unavailable"→424 prerequisite_unavailable; keyword-classified client errors stay 400; anything else 500) with redacted cause text attached only below 500 (operation_handlers.go:788-839). Legacy `op_` ids get 410 Gone on status/cancel/events fallbacks.
- Cursor semantics: run events reject cursors ahead of LastSequence (409 cursor_ahead) and replay below oldest retention (409 replay_gap with recovery hints); operation SSE replays retained events, emits recovery_required+snapshot on gap detection (operations.go:388-396).
- Retry: capacity rejections set `Retry-After: 2`; start dedup by sha256(session+token) makes retried POSTs return the original document without double-start (operations.go:154-159).
- Restart: sessions, CSRF tokens, preparation tokens, operation hub records, and artifact refs are all process-lifetime; durable run state survives and status/event routes fall back to the durable journal; startup runs `ReconcileOperations` over sprint summaries + studies (web_usecases.go:563-591).
- Shutdown ordering and drain semantics are described in `context-web-operation-hub-sse.md` §drain; from this surface's perspective `web.Run` sequences drainAndWait → cancelOperations → Shutdown and joins serve errors (server.go:131-153).

## 9. Files, symbols, tests, contracts

Primary files (internal/web): routes.go (embed, template tree validation, matchRoute, allowedMethods, splitPath, static serving, HandlerOptions/handler construction); security.go (middleware, session/CSRF HMACs, origin validators, preparationStore, trackedWriter/headResponseWriter helpers live here and routes.go); server.go (Run lifecycle, timeouts, canonicalAuthority); handlers.go (dispatch, envelopes, DTO mappers, pageModel, markdown bridge, sprint create, validations); run_handlers.go (runs list/page/detail/cancel/show/events+SSE, usage views, lifecycle cue tables); timeline_handlers.go; qa_handlers.go; operation_handlers.go (prepare/start/status/list/cancel/events + HTML variants + spec mapping + strict JSON decode + error taxonomy); operations.go (hub: documents, events ring, subscribers, drain, safe-text projection); artifacts.go (preview contract validator); server_policy.go (immutable bounds + coherence validation); templates/ (shell/dashboard/projects/project/sprint(s)/stud(y|ies)/runs/run/run_qa/artifact/operation(-confirm)/error + primitives/components/layouts/pages); static/ (css/tokens|base|primitives|components|layouts|utilities.css, css/*.css top-level duplicates app.css/resource-monitor.css/run-timeline.css, js/app|operations|sse|study.js + top-level app.js/resource-monitor.js/run-timeline.js).
App-side support: internal/app/web_usecases.go (interfaces WebQueries + additive capabilities, webUseCases, ref issue/resolve, Artifact preview, Health, Dashboard/Projects/Sprint/Study/Validations projections, ReconcileOperations), web_dimensions.go, web_repos.go, markdown.go, serve_commands.go, surfaces.go (WebRunner type), cmd/ultraplan/main.go:30-37.

Tests (internal/web, 109 test funcs / 21 files): api_compatibility_test.go (route/method freeze, DTO schema freeze, unknown-API/method/query errors + no-store), routes_test.go, security_test.go, server_test.go, server_policy_test.go, templates_test.go, operations_test.go + operations_contract_test.go, run_handlers_test.go, run_tool_observability_test.go, qa_handlers_test.go, timeline_handlers_test.go, sprint_create_test.go, sse_test.go, integration_test.go, packaging_test.go, dimensions_test.go, import_boundary_test.go (production files import stdlib + internal/app only), test_fakes_test.go (fake queries/ops). App-side: web_usecases_test.go (preview bounds, traversal/symlink escape), markdown_test.go (hostile markdown), usecases_test.go.

Contracts: CURRENT — docs/web-compatibility-baseline.md and docs/local-web.md in the target repo; TRD §18A (workspace TRD.md:2093-2167: required initial routes, envelope compatibility control, unknown-API JSON rule, ephemerally-bounded hub, graceful shutdown ownership, template/static layout); PRD Phase-4 requirements (PRD.md:205, 402-420, 1293-1307); Sprint 30 reasoning/api-design.md (Sprint-30 read-only contract: envelope shapes, error table incl. deliberate 404-collapse for artifact rejections, limits 200 entries/256 KiB/64 KiB/8 KiB/128 B, no-store, health truthfulness); Sprint 30/31 dirs; ARCHITECTURE.md dependency direction (internal/web → internal/app only; :552 anti-pattern clause). FUTURE-INTENT markers: baseline doc notes former operation/validation/dimension/report/repo paths remain GET-compatible aliases; TRD notes exact detail routes may evolve but envelopes are controlled.

## 10. Immediate surface dependencies (shared-usecase-vocabulary)

Consumes: `app.WebQueries` (Dashboard/Projects/Project/Sprint/Studies/Study/Validations/Artifact/Health), additive capability interfaces (WebPromptQueries, WebResourceQueries, WebSprintUsageQueries, WebModelQueries, WebDimensionQueries, WebStudyReportQueries, WebSprintWorkspaceMutation), `app.WebOperations` (PrepareOperation/RunOperation + DurableOperationManager + OperationReconciler + OperationCleanupRecorder), `app.RunUseCases` (Runs/Run/RunEvents/CancelRun/RunHealth), `app.QAQueries` (QAStatus/QAMap/QAShard/QATheory/QASynthesis), `app.RenderSafeMarkdown`, `workspace.ResolveInside`, sentinel errors (ErrWebNotFound, ErrWebUnavailable, ErrStaleOperation, QAUseCaseError, Run* sentinels), CollectionInfo/DisplayFinding/WebArtifactLink/WebArtifactPreview/RunSnapshot/RunEvent/RunTarget/RunLifecycle/SprintMetricsSummary types. Consumed-by: cmd composition only; browser JS consumes the frozen API. Sibling surfaces: `context-web-security-boundary` (middleware internals), `context-web-operation-hub-sse` (hub/drain/SSE mechanics), `shared-usecase-vocabulary` (app contracts).

## 11. Explicit unknowns (for later reviewers, not findings)

1. **SSE vs WriteTimeout**: `handleOperationEvents` explicitly extends the write deadline via `http.NewResponseController(w).SetWriteDeadline(now + MaxStreamLifetime + SSEHeartbeat)` (operation_handlers.go:254), but `followRunSSE` (run_handlers.go:993-1046) and `followDurableOperationEvents` (operation_handlers.go:447-514) set no extended deadline while the server sets WriteTimeout=30s (server.go:19,104). Whether long run-event streams survive beyond 30s depends on net/http deadline mechanics and was not executed in this exploration.
2. **Deterministic-secret fallbacks**: both process secrets degrade to values derived from authority/timestamp/root-path if `crypto/rand` fails (security.go:96-98, web_usecases.go:438-440). Reachability under Linux runtime not established here.
3. **refs map growth**: minted artifact/project/sprint/study refs are never evicted during process lifetime; growth profile depends on traffic and is unmeasured.
4. **Timeline tool filter contract**: an event counts as a tool sample iff `event.Payload["tool"] != ""` (timeline_handlers.go:113-118); the producer-side guarantee for that key lives in the run-control/tool-observability surface, not here.
5. **Trailing-slash and Host edge behavior**: `splitPath` returns nil for trailing-slash paths (→404) and Host comparison is exact-case string equality (security.go:149); intent documented only implicitly (baseline doc says "exact").
6. **HEAD depth**: HEAD is honored generically via the discarding writer after full handler execution (routes.go:242-244); whether any handler behaves differently under HEAD was not probed.
7. **Session cookie flags**: HttpOnly + SameSite=Strict, no Secure attribute (scheme is plain http on loopback by design); operator exposure when tunneled is described nowhere in the current contract.
8. **Form-body routes under `hasBody && !operationBody`**: ParseForm-consuming mutations are all classified operationBody (:143), so generic GET routes with bodies are rejected pre-dispatch; interaction with chunked form uploads (TransferEncoding without ContentLength) follows the middleware's `hasBody` definition (:142) and was not exercised here.
