# Context Pack: `web-security-boundary` — Web security, session, and origin controls

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: operator-interfaces. Risk: high. Descriptive only — no defect judgments.

## 1. Purpose

This surface is the product's sole network boundary. It owns everything that decides whether an HTTP request from the loopback listener reaches a dashboard handler at all:

1. **Loopback bind policy**: the `serve --listen` flag is validated as a numeric IPv4 or bracketed IPv6 loopback literal with explicit non-zero port before any workspace work; after `net.Listen` resolves, the resolved address is re-validated and canonicalized to `ip:port` (triple enforcement: pre-bind validation, resolved-address recheck, numeric parse in `canonicalAuthority`).
2. **Host pinning**: every request's `Host` must equal the canonical listener authority exactly (including port); anything else is `403 host_rejected`.
3. **Anonymous signed sessions**: stateless per-process HMAC-SHA256 session cookies (`ultraplan_session` = 32-hex id + "." + hex MAC under a crypto/rand secret generated once per process). Invalid or absent cookies are transparently re-issued; there is no server-side session table.
4. **Derived CSRF**: each session's CSRF token is `HMAC("csrf:"+session)` under the same per-process secret, exposed via the `X-CSRF-Token` response header and `<meta name="ultraplan-csrf">`; API mutations compare it constant-time in middleware; HTML form mutations compare the `_csrf` field per handler.
5. **Origin proof tiering**: mutations require the exact server origin (`http://<authority>`), with a documented fallback for browsers that strip non-default ports from Origin — accepted only when `Sec-Fetch-Site: same-origin` AND the Referer scheme+host equal the expected authority exactly. Reads/navigation allow absent Origin; static assets bypass origin gates.
6. **Request hygiene limits**: 8 KiB request target, ambiguous framing rejection (duplicate Content-Length or CL+TE), 64 KiB body cap on allowlisted POST routes only (`MaxBytesReader`), bodies rejected everywhere else, 128-byte identifiers.
7. **In-flight bound**: a 32-slot semaphore acquired before routing; waiting honors request-context cancellation (503 otherwise). SSE connections hold slots (interaction owned by the hub surface).
8. **Security headers**: CSP (`default-src 'self'`, no inline scripts, `frame-ancestors 'none'`, `form-action 'self'`), `X-Frame-Options: DENY`, nosniff, `Referrer-Policy: same-origin`, `Cache-Control: no-store`, Permissions-Policy lockdown, no CORS headers ever.
9. **Policy coherence gate**: `ServerPolicy` freezes every timeout/bound as immutable built-in values; `ValidateServerPolicy` enforces positivity plus coherence relations and runs fail-closed in `web.Run` before listening. Operators cannot weaken caps via config/env.
10. **Out-of-token-flow mutations**: two guarded workspace actions that deliberately bypass the prepare/start confirmation protocol and rely only on session+origin+CSRF gates: sprint-create (materializes a roadmap sprint directory) and HTML/API run cancellation.

## 2. Entrypoints and control flow

### 2.1 Process wiring

- `cmd/ultraplan/main.go:18` builds `signal.NotifyContext(os.Interrupt, SIGTERM)` and injects `WebRunner` → `web.Run` (:30-41) with `LaunchBrowser: openBrowser` (per-OS `xdg-open`/`open`/`rundll32 url.dll,FileProtocolHandler`, 5 s timeout :45-62).
- Dispatch: internal/app/app.go:171 `"serve"` → `runServe` (internal/app/serve_commands.go:18): flags `--listen` (default `127.0.0.1:8080`) and `--open-browser`; unexpected args → ExitUsage; `ValidateLoopbackListen(*listen)` (:33) runs BEFORE workspace discovery/config/runtime initialization (pinned by TestServeListenValidationRunsBeforeWorkspaceAndRunner, serve_commands_test.go:51); then workspace discovery, effective config load, QA settings, runtime factory, run repository, `NewWebUseCases` (with DurableOperations manager), finally `deps.webRunner(deps.ctx, ServeRunOptions{...})`.
- `ValidateLoopbackListen` (serve_commands.go:119): rejects leading/trailing space/empty, `net.SplitHostPort` failure, `%` zones, non-parse or non-loopback IPs, ports outside 1–65535.

### 2.2 web.Run startup gate sequence (internal/web/server.go:39)

1. `app.ValidateLoopbackListen(opts.Listen)` (:43).
2. `ValidateServerPolicy(DefaultServerPolicy())` (:46) — the coherence gate; failure aborts before any I/O.
3. Required `Queries` check (:49); stdout/diagnostics default to io.Discard; diagnostics wrapped in `lockedWriter`.
4. `opts.ListenFunc("tcp", opts.Listen)` (:63) — production `net.Listen`.
5. `canonicalAuthority(listener.Addr())` (:68): SplitHostPort + numeric loopback parse → `JoinHostPort(ip.String(), port)`; then `ValidateLoopbackListen(authority)` recheck (:72) fails closed with "listener resolved outside loopback policy".
6. `origin = "http://" + authority`.
7. If Operations implements `app.OperationReconciler`: `ReconcileOperations(ctx)` must succeed before serving; failure closes the listener and returns error (:76-81).
8. Hub + `NewHandler` (routes.go:53): parses embedded templates (duplicate-definition and layering validation), constructs `preparationStore`, QA capability extraction, then `security.wrap(h)` (:76-77) — the security middleware wraps the entire dispatch tree.
9. `http.Server` with fixed ReadHeaderTimeout 5 s / ReadTimeout 15 s / WriteTimeout 30 s / IdleTimeout 60 s and `BaseContext` returning the serve context (:100-107).
10. Prints `Dashboard: <origin>/` to stdout; starts `server.Serve(listener)` goroutine; optional browser launch (warnings only, never fatal).
11. Select: serve error (ErrServerClosed → nil) vs ctx.Done → shutdown path (drain belongs to hub surface; `ShutdownTimeout` 10 s budget shared).

### 2.3 Per-request pipeline — `securityMiddleware.wrap` (internal/web/security.go:102)

Order of operations:

1. `trackedWriter` created; `applySecurityHeaders` applied unconditionally before any outcome (:106).
2. Server-generated `X-Request-ID` (16-byte hex via `randomRequestID`); caller-supplied X-Request-ID ignored (:107-109).
3. `readSession(r)` (:110, :234): cookie `ultraplan_session`; split on "." must yield exactly 2 parts, first part length 32; recompute `signSession(parts[0])` and constant-time compare against full cookie value. On absence/failure: fresh random 32-hex id, `Set-Cookie ultraplan_session=<signed>; Path=/; HttpOnly; SameSite=Strict; MaxAge=3600` (no Secure attribute — plain HTTP loopback) (:111-114).
4. `csrfFor(session)` computed and set as `X-CSRF-Token` response header on every request; both session id and CSRF token placed into request context (:115-119).
5. Semaphore acquire (`MaxInFlight` = 32, chan-based) (:121-131); if `r.Context()` cancels while waiting → 503 "unavailable".
6. Route classification (:133-143): `matchRoute(r.URL.Path)`; mutation classes:
   - `apiOperationMutation` = POST or DELETE with path prefix `/api/v1/operations`
   - `apiRunMutation` = DELETE on route name `api_run`
   - `htmlOperationMutation` = POST with path prefix `/operations/`
   - `htmlSprintMutation` = POST on route name `sprint_create`
   - `htmlRunMutation` = POST on route name `run_cancel`
   - `operationRead` = GET/HEAD on `api_operation` / `api_operation_events`
   - `staticAsset` = GET/HEAD under `/static/`
   - `operationBody` = POST on {`api_operation_prepare`, `api_operations`, `operation_prepare`, `operation_start`, `operation_cancel`, `run_cancel`, `sprint_create`}
7. Gate switch, evaluated in order (:144-164):
   1. `len(RequestURI) > 8 KiB` → 400 invalid_request
   2. `ambiguousRequestFraming` (>1 Content-Length, or Content-Length + Transfer-Encoding) → 400
   3. `r.Host != m.authority` → 403 host_rejected (exact string equality including port)
   4. operationMutation without validSession → 403 session_required
   5. operationMutation without command-tier origin → 403 origin_rejected
   6. apiOperationMutation/apiRunMutation with `subtle.ConstantTimeCompare(X-CSRF-Token, csrf) != 1` → 403 csrf_failed
   7. operationRead without read-tier origin → 403 origin_rejected
   8. all other routes (pages, non-operation APIs, unknown paths) without read-tier origin → 403 origin_rejected (staticAsset excluded)
   9. `ContentLength > 64 KiB` → 413 request_too_large
   10. hasBody && !operationBody → 400 "Request bodies are not accepted."
   11. default: wrap `r.Body = http.MaxBytesReader(tracked, r.Body, 64 KiB)` when operationBody; call next.
8. Diagnostics line always written: `event=http_request request_id=… route=<normalizedRoute class> method status duration_ms response_bytes` (:174-176). Rejected requests additionally log `event=security_rejection … code=<code>` (:186).

Mechanics notes (descriptive): `matchRoute` runs twice per request (middleware + handler.ServeHTTP routes.go:227). A valid-cookie request never receives Set-Cookie; an invalid one receives a new cookie on every such request. The trackedWriter makes WriteHeader idempotent and forces an implicit 200 on first Write/Flush.

### 2.4 Origin validators (security.go:262-314)

- `validCommandOrigin(origin, expected)`: strict string equality with `http://<authority>` (:272-274).
- `validCommandRequestOrigin(r, expected)` (:280-298): exact match passes. Otherwise ALL of: `Sec-Fetch-Site == "same-origin"`; Origin, expected, and Referer each parse as URLs with no userinfo; Origin scheme == expected scheme ("http"); Origin hostname == expected hostname (numeric loopback); Origin port empty AND expected port non-empty; `Referer scheme+"://"+Referer host == expected`. Any deviation fails closed.
- `validOperationReadRequestOrigin` / `validReadRequestOrigin`: absent Origin allowed; present Origin must pass the command tier (:304-306, :312-314).
- Rejection messages render sanitized received values: `displayOrigin` parses and normalizes or labels "null"/invalid; `displayAuthority` strips control chars and caps at 256 bytes (:190-226).

### 2.5 Session/CSRF cryptography (security.go:96-98, :228-251)

- Secret `[32]byte` filled by `crypto/rand` at middleware construction; deterministic fallback `sha256(authority + now().UTC().RFC3339Nano)` only if rand errors.
- `signSession(id)` = `id + "." + hex(HMAC-SHA256("session:"+id))`; verification recomputes and constant-time compares the full value.
- `csrfFor(id)` = `hex(HMAC-SHA256("csrf:"+id))` — same key, distinct domain prefix.
- Consequence of design: all sessions/preparations become invalid whenever the process restarts (secret rotation); there is no revocation list, expiry beyond cookie MaxAge 3600, or persistence.

### 2.6 Out-of-token-flow mutations

- **Sprint-create** (`handleSprintCreate`, handlers.go:1314): POST `/projects/{project}/sprints/create`. Per-handler guard: `r.ParseForm()` error or `FormValue("_csrf") != csrfToken(ctx)` → 403 rendered error (:1315). Capability check `h.queries.(app.WebSprintWorkspaceMutation)` else 503 (:1319-1322). `slug := FormValue("sprint")` must pass `validIdentifier` (≤128 bytes, single safe path segment charset) and project via `validOptionalIdentifier` non-empty (:1325-1327) → 400 otherwise. Calls `mutation.CreateSprintWorkspace(ctx, project, slug)` (web_usecases.go:766 → sprint service `CreateWorkspace`) which materializes the sprint directory without running flow stages; failure → 422 rendered error; success → 303 redirect to `/projects/{p}/sprints/{slug}`. This writes workspace filesystem state with NO confirmation token, preparation record, fingerprint binding, or hub involvement — only session+origin+CSRF gates apply.
- **HTML run cancel** (`handleRunPageCancel`, run_handlers.go:889): POST `/runs/{id}/cancel`. Same per-handler `_csrf` pattern → 403; nil runs → 503; `runs.CancelRun(ctx, RunID(value), "user_requested")` durable cancellation → error renders 409; success 303 to `/runs/{id}`.
- **API run cancel** (`handleRunCancel`, run_handlers.go:918): DELETE `/api/v1/runs/{id}` — JSON success envelope `{changed, run}`; CSRF enforced centrally by the middleware gate (constant-time header compare).
- The middleware classifies all three as `operationMutation`/mutation-body routes, so they also carry session-required and command-origin gates centrally (:137-139, :143).

### 2.7 ServerPolicy gate (internal/web/server_policy.go)

- `ServerPolicy` aggregates every timeout (ReadHeader/Read/Write/Idle/Shutdown, PreparationTTL, TerminalRetention, SSEHeartbeat, MaxStreamLifetime) and limit (MaxInFlight 32, MaxBodyBytes 64 KiB, MaxRequestTarget 8 KiB, MaxIdentifierBytes 128, MaxActiveOperations 8, MaxPreparations 128, MaxEventsPerOperation 256, MaxEventBytesPerOperation 256 KiB, MaxEncodedEventBytes 16 KiB, MaxTerminalResultBytes 256 KiB, MaxSubscribersPerOperation 8, MaxConcurrentStreams 32, SubscriberQueueSize 32). Package comment states operators cannot weaken these through workspace configuration or environment variables (:8-11).
- `DefaultServerPolicy()` mirrors the package constants in server.go:16-23 and operations.go:18-31 (single definitions; policy struct references the constants).
- `ValidateServerPolicy` (:34-52): all durations positive; all limits positive; coherence relations — ReadHeaderTimeout ≤ ReadTimeout, SSEHeartbeat < MaxStreamLifetime, PreparationTTL < TerminalRetention, MaxEncodedEventBytes ≤ MaxEventBytesPerOperation, SubscriberQueueSize ≤ MaxEventsPerOperation, MaxSubscribersPerOperation ≤ MaxConcurrentStreams. Violation message: "local-web resource limits are incoherent". Called once in `web.Run:46` at startup; there are no other callers found at this commit (the struct is descriptive documentation of the fixed bounds rather than a runtime-configurable knob).

## 3. Inputs and outputs

Inputs consumed by this surface:
- Request line: method, `RequestURI` length, URL path (route classification).
- Headers: `Host` (pinning), `Origin`, `Referer`, `Sec-Fetch-Site` (command-tier fallback proofs), `Cookie: ultraplan_session`, `X-CSRF-Token` (API mutations), `Content-Length` / `Transfer-Encoding` (framing ambiguity), implicit `ContentLength`.
- Body bytes only on the seven allowlisted POST routes, capped 64 KiB (enforcement of parsing belongs to handler/hub surfaces).
- CLI: `--listen`, `--open-browser`, global `--workspace`.

Outputs:
- Response headers: security header set + `X-Request-ID` + `X-CSRF-Token` on every response; `Set-Cookie ultraplan_session` when (re)issued.
- Policy rejections: JSON error envelope `{error:{code,message},meta}` for `/api/*` paths; minimal inline HTML page (message HTML-escaped via `templateText`) otherwise (writePolicyError, handlers.go:1116-1124). Codes: `unavailable`, `invalid_request`, `host_rejected`, `session_required`, `origin_rejected`, `csrf_failed`, `request_too_large`. Messages embed sanitized echo of received Host/Origin plus remediation guidance ("Open the exact Dashboard URL printed by UltraPlan…").
- Sprint-create/run-cancel handler outputs: rendered error pages (403/400/422/409/503), 303 redirects, JSON envelopes for the API variant.
- Startup outputs: stdout `Dashboard: <origin>/\n`; diagnostics events `server_started`, `server_stopped`, `browser_launch warning=…`, `security_rejection`, `http_request`; process exit codes ExitUsage/ExitConfig/ExitRuntime/ExitError mapped in runServe/app.Run.
- External effects listed in §7.

## 4. Authoritative state

Owned here, process-local, never persisted:
- `securityMiddleware.secret [32]byte` — the sole root of session/CSRF trust; generated once per process; rotates on restart, invalidating every outstanding cookie and derived CSRF value.
- `securityMiddleware.sem` (chan, cap 32) and `active atomic.Int64` — in-flight accounting.
- `preparationStore.records map[token]*preparationRecord` (security.go:368-446): token `"confirm_"+randomRequestID`; bound Session/Canonical/Fingerprint/full Confirmation; `ExpiresAt = now+2m`; single-use `Consumed` flag; capacity ≤128 checked after lazy reap. `issue()` calls `reapLocked()` (drops entries past ExpiresAt) before the capacity check; `consume()` does not reap — expired-but-unreaped entries still fail consumption with `errConfirmationExpired` and burn the Consumed flag. Consumption outcomes: unknown/consumed → `errConfirmationReplayed`; expired → burn + expired; session-or-canonical mismatch → burn + mismatch; fingerprint drift → burn + stale. Sentinel errors at :453-458. (Token lifecycle semantics are exercised jointly with the hub surface, whose startConfirmed consumes.)
- `confirmationDedupKey(session, token)` = sha256(session+"\x00"+token) (:448) — input to the hub's dedup map.
- Anonymous session ids are random 32-hex strings verified purely by MAC; there is no server-side session registry, clock/expiry tracking of sessions themselves (cookie MaxAge is client-enforced), or logout mechanism.

External authorities touched but not owned: workspace filesystem (sprint-create creates directories via sprint service); run-control store (cancellation requests via `runs.CancelRun`); the hub surface owns records/SSE/drain; handlers own query projections.

## 5. Invariants (as implemented)

- Every response carries the security header set and a server-generated X-Request-ID regardless of acceptance or rejection; caller-supplied X-Request-ID is discarded.
- Host pinning precedes origin/session evaluation; authority equality is exact including port, so `localhost:8080` ≠ `127.0.0.1:8080` and IPv4/IPv6 forms are distinct.
- Every mutation-class request requires all three of: cryptographically valid session cookie, command-tier origin proof, and CSRF proof — API variants via central constant-time header compare, HTML variants via per-handler `_csrf` field comparison (plain `==`, non-constant-time) after `ParseForm`.
- An absent Origin can never satisfy a mutation gate: strict equality fails, and the port-stripped fallback requires `Sec-Fetch-Site: same-origin` plus an Origin that parses with matching scheme/hostname — an unparsable/empty Origin fails those checks.
- Reads and page navigation tolerate absent Origin (top-level navigation, curl-style local clients); any present Origin must prove exact or fully-proven port-stripped sameness. Static assets are exempted from origin gates entirely.
- Bodies are accepted only on seven named POST routes; elsewhere any body (declared or chunked) is rejected; declared bodies over 64 KiB are rejected up front and `MaxBytesReader` bounds actual reads.
- Ambiguous framing (duplicate Content-Length, CL+TE) and >8 KiB request targets are rejected before Host/origin evaluation.
- At most 32 requests are inside the handler tree at once (SSE streams included); admission waits are cancellable via request context.
- Policy bounds are compile-time constants; the startup coherence gate fails closed on impossible configurations; no operator-facing knob exists to relax them (configOverridesForServe is intentionally empty, serve_commands.go:113).
- Loopback binding is validated pre-bind and re-validated post-resolve; canonicalAuthority requires a numeric loopback IP, so hostname listeners cannot slip through.
- Session verification uses constant-time comparison; API CSRF verification likewise; both derive from one per-process random key with domain-separated MAC inputs ("session:"/"csrf:" prefixes).
- Sprint-create slugs/projects are constrained to single safe path segments (validIdentifier regex, ≤128 bytes) before reaching app-layer filesystem effects; unsafe segments are rejected with no mutation attempted.
- Diagnostics classify routes via normalizedRoute buckets and never include raw paths or query strings (TestRequestDiagnosticsAreNormalizedAndRedacted pins that `secret=hunter2`, refs, and authority do not appear).
- Rejections are safe-by-construction: JSON envelopes for API paths; HTML rejection pages escape the interpolated message including attacker-influenced Host/Origin echoes (control chars stripped, 256-byte cap).

## 6. Trust boundaries

- This is the product's only network listener. The boundary model is: loopback listener ↔ browser origin, with the OS user as the implicit super-trust. docs/local-web.md §Local Security And Trust Boundary states explicitly: loopback is not an authentication or isolation boundary against another process running as the same OS user; the cookie is "session policy", not an account/TLS/remote authentication model; proxying/port-forwarding is prohibited by documentation but not technically preventable.
- Three escalation tiers of proof are recognized for a request: (a) top-level navigation/local reads (Host-valid + absent Origin), (b) same-origin read proofs (any Origin presented must match command tier), (c) mutation authority (session + command-tier origin + CSRF, with confirmation tokens additionally required by the hub for operation starts).
- The port-stripped-Origin fallback treats `Sec-Fetch-Site` and `Referer` as unforgeable browser assertions; the code comments frame this as tolerance for "browser shells or a local reverse proxy" behavior while keeping session/CSRF independent mandatory checks.
- Fetch-metadata/session/CSRF defenses assume a standards-conformant browser on the same machine; cross-origin attackers are modeled as web content reaching `http://<loopback>:port`, which Host pinning + origin checks + CSRF are designed to stop.
- The deterministic secret fallback (only on crypto/rand failure) would make the secret derivable from authority+timestamp by anyone who knows both; it exists as a never-fail guarantee rather than a normal path.
- Sprint-create converts a browser form post into workspace filesystem creation — the widest trust transition in the surface — fenced only by identifier charset validation at transport plus whatever the sprint service enforces downstream.

## 7. External effects

- Binds a TCP listener on a loopback address (the only socket the product opens for serving).
- Creates sprint workspace directories (projects/<p>/sprints/<slug>) via `CreateSprintWorkspace` when sprint-create succeeds — durable filesystem mutation outside the confirmation-flow.
- Records durable cancellation requests in the run-control store via `runs.CancelRun` (HTML and API run-cancel paths).
- Optionally launches an external browser process (`xdg-open`/`open`/`rundll32`) with a 5 s budget; failures degrade to warnings.
- Writes to stdout (dashboard URL) and stderr (diagnostics event stream, serialized by lockedWriter).
- Terminates the process with classified exit codes on configuration/policy failures.

## 8. Cancellation / retry / restart / error semantics

Cancellation:
- Process-level: SIGINT/SIGTERM → NotifyContext cancellation → `web.Run` select takes ctx.Done branch → hub drain (other surface) → shared 10 s ShutdownTimeout → `server.Shutdown` → exit nil on clean path; runner returning ctx.Canceled maps to exit OK (TestServeCancellationIsClean).
- Request-level: semaphore waits honor `r.Context().Done()` (BaseContext derives request contexts from the serve context, so process cancellation also releases waiters) → 503 unavailable.
- No async work is started by this surface itself except the transient browser-launch command (context-bounded 5 s).

Retry:
- All policy rejections are terminal single-request responses; retryability is implied only by their messages (refresh/re-open guidance). No Retry-After headers are emitted by this surface (capacity Retry-After lives in the hub surface).
- Session bootstrap retries naturally: any request lacking a valid cookie gets a fresh one, so a retry after rejection-with-new-cookie can proceed to session-gated flows.

Restart:
- Full loss of session/CSRF/preparation state by construction (in-memory secret). Browsers holding old cookies receive new cookies transparently on next request; preparations held by the hub die with the process (hub surface covers reconciliation).
- Port occupancy is not retried or worked around ("does not silently choose a different port" — docs/local-web.md:33-34); listen failure aborts with classified error.

Errors:
- Gate failures use uniform codes/messages (§3) and never leak stack traces; cause text is limited to the crafted remediation sentences with sanitized echoes.
- Startup failures are ordered and classified: listen-flag validation (ExitUsage, before workspace I/O) → workspace/config/runtime/repository errors (their own exits) → policy coherence (wrapped error from web.Run) → listen syscall failure → post-listen loopback recheck failure → reconcile failure (listener already closed).
- `flag.ErrHelp` exits 0 without starting anything; unexpected positional args exit Usage.

## 9. Files, symbols, tests, contracts

Production files:
- internal/web/security.go — constants MaxBodyBytes/MaxRequestTarget/MaxIdentifierBytes :23-27; context keys :29-31; trackedWriter :33-65; securityMiddleware :67-76; newSecurityMiddleware (secret init incl. rand-fallback) :78-100; wrap (full pipeline) :102-178; ambiguousRequestFraming :180; reject + rejection messages + display sanitizers :185-226; signSession/readSession/csrfFor :228-251; applySecurityHeaders :253-260; origin validators validOrigin/validCommandOrigin/validCommandRequestOrigin/validOperationReadOrigin/validOperationReadRequestOrigin/validReadRequestOrigin :262-314; context accessors :316-329; randomRequestID :331; normalizedRoute :339; preparationRecord/preparationStore issue/consume/reapLocked :368-446; confirmationDedupKey :448; sentinel errors :453-458.
- internal/web/server_policy.go — ServerPolicy :12-20; DefaultServerPolicy :22-32; ValidateServerPolicy :34-52.
- internal/web/server.go — timeout/MaxInFlight constants :16-23; Options :25-37; Run (validation order, listen, canonical authority recheck, reconcile gate, handler/server construction, serve/shutdown select) :39-154; lockedWriter :156; canonicalAuthority :167-177.
- internal/web/routes.go — NewHandler (template validation, preparation store, security.wrap) :53-78; ServeHTTP :226; allowedMethods :252-271; matchRoute :282-402 (route names referenced by the middleware classes: api_operation_prepare/api_operations/api_operation/api_operation_events/api_run/operation_prepare/operation_start/operation_cancel/run_cancel/sprint_create/static); serveStatic (allowlisted asset names :23-28) :442-457.
- internal/web/handlers.go — dispatch cases :536-539 (run_cancel), :779-786 (sprint_create et al.); writePolicyError/templateText :1116-1128; handleSprintCreate :1312-1334.
- internal/web/run_handlers.go — handleRunPageCancel (HTML) :889-903; handleRunCancel (API DELETE) :918-929.
- internal/web/operations.go — bound constants consumed by ServerPolicy :18-31 (TTLs, capacities, stream limits).
- internal/app/serve_commands.go — DefaultServeListen :16; runServe :18-99; configOverridesForServe :113; ValidateLoopbackListen :117-139; serveHelp :141.
- internal/app/app.go — serve dispatch :171-172.
- cmd/ultraplan/main.go — main :18-43 (signal context, WebRunner→web.Run wiring); openBrowser :45-62.
- internal/app/web_usecases.go — WebSprintWorkspaceMutation interface :70-75; CreateSprintWorkspace :764-775.
- Browser side: templates/shell.html:7 (`<meta name="ultraplan-csrf">`); static/js (consumption of meta/header tokens not traced in depth).

Tests:
- internal/web/security_test.go — host/origin matrix incl. rewritten-port, different-loopback, null, malformed origins; header presence and no-CORS assertion :11-50; static assets accept `Origin: null` while APIs reject it :52-73; bracketed IPv6 authority :75-88; explanatory mismatch messages + diagnostics classification :90-116; exact command-origin requirement incl. https/loopback-swap/null rejections :118-148; port-stripped mutation allowance with full proofs :150-166; port-stripped read allowance :168-182; read rejection with wrong referer :184-196; mutation fallback requiring each individual proof (missing fetch metadata, cross-site, wrong referer port, missing referer) :198-225; operation-stream origin tiering :227-281; body/target/X-Request-ID limits :283-311; framing ambiguity :313-331; diagnostics normalization/redaction :333-351.
- internal/web/server_policy_test.go — defaults validate; six incoherence mutations each fail :8-27.
- internal/web/server_test.go — lifecycle/canonical URL/launcher warning/shutdown diagnostics :38-85; non-loopback and listen-failure rejections :87-101.
- internal/web/sprint_create_test.go — happy path redirect + fake capture :13-43; `../escape` slug rejected before mutation; forged `_csrf` → 403; GET → 405 :26-42; creation failure surfaces 422 :45-55.
- internal/app/serve_commands_test.go — help contract :12-25; preflight/runner options with `[::1]:9090` :27-49; listen validation ordering + accepted literals (127.42.0.8 is loopback) :51-71; workspace discovery/startup failure :73-95; clean cancellation :97-109.
- internal/web/operations_test.go — establishOperationSession helper (GET / → cookie + X-CSRF-Token) :524; form/mutation request helpers :534-559; preparation binding/expiry/replay/capacity coverage :270+ (jointly with hub surface).
- internal/web/import_boundary_test.go — internal/web may import only stdlib + internal/app :12-41.
- internal/web/routes_test.go — testHandler/request helpers :241-258; method/route shape tests.

Contracts:
- CURRENT-CONTRACT: docs/local-web.md — "--listen accepts only a numeric loopback IP literal and explicit port"; Local Security And Trust Boundary section (bullet list of controls; absent-Origin rule for navigation/GET/HEAD; mutation triple requirement; null/malformed/cross-origin/non-loopback rejection; IPv4/IPv6 distinctness; loopback-not-authentication caveat; proxy prohibition); stable error-code vocabulary incl. `host_rejected` family; server-settings immutability ("restart to change them").
- Planning workspace (CURRENT-CONTRACT): projects/ultraplan-go/docs/TRD.md §7.5 "Local server security requirements" — same-origin/no CORS, strict Host/Origin, CSRF for mutations, body limits, security headers, bounded concurrent streams, safe cookies/per-process session mechanism, path containment/redaction/safe-error parity with CLI/TUI, no hosted auth in Phase 4; §7.5 required-test list names CSRF/origin rejection, path escape, redaction; Phase 4 restatement at TRD.md:2395. Sprint 30 requirements.md maps security.go/security_test.go to those acceptance criteria (non-loopback bind rejection before serving; Host/Origin/limits/headers/redaction enforced by HTTP tests).
- HISTORY/FUTURE-INTENT: Sprint 30 established the read-only foundation this surface secures; Sprint 31 added operations (confirmation flow lives in the hub surface but its preparation store is defined in security.go); TRD Phase 6 (Sprint 35) adds durable-run visibility rules that later surfaces implement around these gates.

## 10. Immediate surface dependencies

- **web-operation-hub-sse**: shares this package; hub.startConfirmed consumes `preparations.consume(token, session, canonical, fingerprint)` and `confirmationDedupKey` defined here; hub capacity/draining errors (`errOperationCapacity`, sentinel family) originate in security.go; SSE connections consume semaphore slots owned by this middleware.
- **web-routing-projection**: `matchRoute` names/classes feed every middleware gate decision; method allowlists decide 405s after security gates pass; staticAssetNames allowlist bounds the origin-exempt static surface.
- **durable-operation-spine**: `runs.CancelRun` invoked by both run-cancel handlers; run repository constructed during serve preflight.
- **foundation/config-inspection-health**: workspace discovery, effective config load, QA settings consumed by runServe before web.Run; `ReconcileOperations` startup gate bridges into hub/product recovery.
- **workspace-scaffold / sprint service**: CreateWorkspace performs the actual sprint-directory materialization behind sprint-create.
- **platform/runtime adapter**: models lister capability extracted during serve preflight (not security-relevant but part of the entrypoint chain).
- cmd wiring (main.go) supplies the signal context and browser launcher consumed by this surface's lifecycle.

## 11. Explicit unknowns

- Real-world browser populations that strip ports from loopback Origins (motivating `validCommandRequestOrigin`'s fallback) are asserted in code comments/tests but not evidenced against specific browser versions in inspected sources.
- The crypto/rand failure fallback for the session secret has no test coverage and its practical reachability was not assessed.
- `htmlRunMutation` is gated twice (middleware session/origin class + per-handler `_csrf` compare); whether the per-handler check is redundant belt-and-braces or load-bearing for some path was not traced further (all three HTML mutation handlers repeat the pattern identically).
- HTML-form CSRF comparison uses plain `==` while API CSRF uses `subtle.ConstantTimeCompare`; whether this asymmetry is deliberate (form parsing timing dominance) is undocumented.
- Whether any non-browser local tooling depends on the absent-Origin read allowance beyond docs' mention of "local GET/HEAD clients" is undocumented.
- `readSession` validates the session-id component by length only (32 chars), not charset; whether non-hex ids occur anywhere (they are only ever produced by randomRequestID) was not surveyed beyond this package.
- The interaction between the 32-slot semaphore and long-lived SSE streams (slot held for stream lifetime up to 30 min) is described from code but its operational impact belongs to measurement in the hub surface.
- `consume()` does not lazily reap expired preparations (only `issue()` does); capacity pressure therefore relies on issue-path reaping — mechanics described, intent unstated.
- docs/web-compatibility-baseline.md was sampled for sibling surfaces; exhaustive diff of policy-relevant fixtures against this surface was not performed.
