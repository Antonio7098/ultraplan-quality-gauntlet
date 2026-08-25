# Surface Map — map-05-interface-surfaces (CLI / TUI / web interfaces)

Job: independent product-surface discovery from the interface lens. No findings reported in this phase; risk notes are prioritisation rationale only.

## Provenance

- Target: `/home/antonioborgerees/coding/ultraplan/ultraplan-go`, frozen commit `f0fcd0c2107a8e8d69e1283f9e8d5e2c6da94025`. All discovery ran against a dedicated pinned clone (`/tmp/opencode/upgo-frozen`, verified `git rev-parse` = `f0fcd0c`). The live working tree is at `ad0be98`; sole delta vs frozen is `internal/sprint/prompt_context.go` (256 KiB → 512 KiB shared prompt budget) — not interface code. Everything below holds at both commits.
- Planning workspace: `/home/antonioborgerees/coding/ultraplan/ultraplan-workspace` @ `ab12dc38059c…` (== HEAD). CURRENT-CONTRACT anchors consulted: `system/contracts/surfaces/{cli,api-contracts,accessibility,frontend}.md`.
- Method: 5 parallel bounded `review-worker` discoveries (CLI grammar, TUI, web routes/lifecycle, web security model, interface convergence/divergence) + mapper verification of load-bearing claims (TUI cancel path `internal/tui/app.go:232-269`; `validCommandOrigin` body vs docstring `internal/web/security.go:269-274`; definition-only helpers `security.go:262-267,300-302`; 27-entry operation-kind enum; zero TUI constructions of `sprint-stage`/`sprint-stage-dry-run`/`validate`; no template/JS issuer of web `study-cancel`).
- Baseline: go test / -race / vet / cover all passed at freeze (state.json).

## Interface topology in one paragraph

Three frontends over one app core. `cmd/ultraplan/main.go` injects `TUIRunner`/`WebRunner` function types into `app.Run` (`internal/app/app.go:88-178`, inversion at `surfaces.go:18-20`); nothing in `internal/app` imports the frontends. CLI handlers mostly call product services directly; TUI and web share the `OperationalUseCases` boundary (`dashboardUseCases`) plus `DurableOperationManager` + `repositoryRunUseCases`, with runtime-backed work funnelling through `sharedOperationRunner` (`operation_runner.go:18-169`). One composition root resolves the workspace identically for all three (`discoverWorkspace`, `app.go:287-298`: `--workspace` flag → `ULTRAPLAN_WORKSPACE` → cwd-ancestry marker walk), and one SQLite run-control repository per workspace root backs observation/cancellation everywhere. Divergence lives in grammar (three arg-parsing styles), durability mechanics (CLI accept has empty digest/no dedup alias vs web/TUI fingerprint digests), and capability coverage (web-only sprint create/model list/timeline; CLI-only `code`, diagnostics export, study reset; TUI-only interactive parallelism form).

---

## Candidate surfaces

Grouping proposal: **A CLI shell** (IA1–IA2), **B Web console** (IB1–IB3), **C TUI** (IC1–IC2), **D Cross-interface** (ID1). Overlaps map-01's coarse S10–S12 but splits them into reviewable units.

### IA1 — `cli-dispatch-exit-contract` (domain A)
- Behaviour: argv dispatch of 15 top-level commands (`--help/-h`, `version`, `init-workspace`, `defaults`, `skills`, `config`, `health`, `storage`, `run`, `project`, `sprint`, `study`, `tui`, `serve`, `code`); whole-argv global `--workspace[=]` scan; exit classes 0–8 with stable code strings; three output styles (jsonEnvelope `{schema_version,command,workspace,status,result}`, ad-hoc sprint-family maps, raw structs); help/version text.
- Files: `cmd/ultraplan/main.go`, `internal/app/{app,json_output,status_json,qa_errors}.go`, per-command `*_commands.go` parsers/dispatchers, contract docs `docs/cli-reference.md`.
- Failure semantics: unknown command → exit 2 stderr (`app.go:175-177`); classified causes preserved (`classedError`, `errorCode` `app.go:41-86`).
- Inventory facts for reviewers: error-classification default differs per family (sprint/study/project → 4; run-control → 6); QA cancel/deadline → 8 while sprint cancellation → 7; some sprint CLI paths classify by error-text sniffing (`strings.Contains(err.Error(), "runtime")` → 6, `"failed tasks"` → 8, `sprint_commands.go:269,360-364,507`) whereas the operation-runner path pins no text classification (`failedOperation`, `operations.go:751-796`, pinned by `sprint_error_test.go:29-41`); three arg-parser styles coexist (hand loops, stdlib flag, `orderRunArgs` reorder where an unrecognised dash token becomes positional and a valued-flag swallows a following `--flag` as value, `run_commands.go:463-481`); undocumented surface: `tui`, `storage migrate`, `sprint metrics`, `study runs summary`, flow model/variant override flags, prompt `review`/`--explain`, cancel reason whitelist, `--workspace=` form; embedded `skillsMaterialiseHelp` lists 9 stages while workspace layer defines 11 incl. `reconcile` (`skills_commands.go:119-128` vs `workspace/skills.go:41,231-233`).
- Key tests: `app_test.go` (help/version/unknown/classified cause), `run_commands_test.go`, `sprint_commands_test.go` (exit classes, single JSON failure document, stable QA codes), `study_*_commands_test.go`, `serve_commands_test.go`, `code_commands_test.go` (exit 4/5/8 matrix).
- Seams: every runtime-backed command crosses the durable acceptance seam (`beginDurableCLICommand`, counted by source inventory `run_control_inventory_test.go:11-55`); JSON shapes are the automation contract.
- Risk: normal-high — silent breakage class (exit codes/JSON shapes) consumed by scripts and agents.

### IA2 — `run-cli-observation-control` (domain A)
- Behaviour: `run list|show|follow|cancel|diagnostics` against the shared repository: filtered listing (limit default 50, bounds enforced 1..200 downstream `runcontrol/lifecycle.go:197-202`), snapshot render, follow loop (512-event batches, 250 ms backfill / 1 s idle, exits on terminal && cursor ≥ LastSequence, ctx-cancel leaves durable state untouched), cancel with reason whitelist {user/operator/shutdown/quota}→`RequestCancellation`, diagnostics health block + bounded support export (≤1 MiB, O_EXCL 0600).
- Files: `internal/app/run_commands.go`, `internal/app/run_usecases.go` (`repositoryRunUseCases`), `internal/runcontrol` observation API.
- Key tests: `TestRunCommandsListShowFollowCancelDiagnosticsAndSupport`, `TestRunFollowReplaysTerminalJournalAndStopsWithoutCancelling`, `TestRunHelpDoesNotOpenRepository` (`run_commands_test.go`).
- Seams: same queries as web SSE and TUI polling; same `RequestCancellation` as all interfaces.
- Risk: normal; it is the operator's window into the S9 durable spine.

### IB1 — `web-routing-projection-compat` (domain B)
- Behaviour: hand-rolled route matcher (~47 page/API/static GETs + mutations): HTML pages (dashboard/projects/project/sprint/studies/study/runs/run/artifact/operation/operation-confirm/QA pages), `/api/v1/*` JSON reads with strict param regexes and query-string allowlist, artifact preview via HMAC opaque refs + symlink-evaluated containment + 256 KiB bound, go:embed templates (primitive→component→layout→page hierarchy validated at startup) and 14-name static allowlist; compatibility frozen by executable fixtures.
- Files: `internal/web/{routes,handlers,artifacts}.go`, `templates/**`, `static/**` (server-side parts), `web_usecases.go` projection layer (`bounded[T]`, honest `meta.returned_count/total_count/truncated`), `api_compatibility_test.go`, `routes_test.go`, `packaging_test.go`.
- Failure semantics: unknown query → 400; `/api/v2/*` → 404; wrong method → 405+Allow; internal causes collapsed to generic codes (`writeRouteError`, tested `routes_test.go:185`).
- Key facts: QA API returns raw app structs verbatim (no DTO layer, `qa_handlers.go`); markdown reaches browser only through goldmark-based `RenderSafeMarkdown` (raw HTML omitted, unsafe links filtered, `markdown.go:11-22`); JSON encoding uses `SetEscapeHTML(true)`; `Cache-Control: no-store` globally except static revalidated assets.
- Seams: consumes dashboardUseCases/webUseCases queries; artifact refs minted in-memory per process (restart invalidates).
- Risk: high — largest read/projection surface; escaping posture and envelope stability are review targets.

### IB2 — `web-operation-hub-sse` (domain B)
- Behaviour: two-phase operations (prepare → confirmation_token TTL 2 min, cap 128 → start consuming token against freshly prepared canonical request+fingerprint, atomic consume, sha256(session‖token) dedup for double-click idempotency); hub caps (MaxActiveOperations=8 → 429+Retry-After, streams 32/subscribers-per-op 8, ring 256 events/256 KiB/op, encoded event cap 16 KiB → warning stub); SSE with closed event-name set {snapshot, progress, warning, finding, artifact, cancel_requested, recovery_required, terminal}, Last-Event-ID replay, `recovery_required` on replay gap, slow-subscriber drop, 30 min lifetime/15 s heartbeat; graceful drain (reject new starts, cancel non-terminal ops reason `server_shutdown`, deadline → persist cleanup-uncertain markers via `RecordOperationCleanupUncertain`, project `cleanup_uncertain` terminal state).
- Files: `internal/web/{operations,operation_handlers,server}.go`, `security.go` preparation store, app glue `durable_operations.go`, `web_usecases.go` (`ReconcileOperations` at startup walks sprints+studies, `web_usecases.go:563-591`).
- Inventory facts: mutations outside the token flow exist — `POST /projects/{p}/sprints/create` (session+CSRF only, direct `CreateSprintWorkspace`, `handlers.go:1314-1334`), `DELETE /api/v1/runs/{id}` and HTML cancels (direct cancel, CSRF-checked); browser JS closes terminal SSE then `window.location.reload()` (`static/app.js:887-894`) while the durable-run timeline path stops without reload; pre-durable `op_*` ids get 410 `legacy_operation_not_retained`.
- Key tests: `operations_test.go` (hub lifecycle/drain/deadline-cleanup-uncertain/ninth-op rejection/redaction-before-retention), `sse_test.go`, `operations_contract_test.go` (browser kind table, SSE frame shape, error-code table), `integration_test.go` (real end-to-end prepare→start→poll).
- Seams: AcceptOperation durable seam; cleanup-uncertainty seam shared with study owner reconciliation.
- Risk: high — this is where user intent becomes durable child work; drain/token/replay semantics deserve deep review.

### IB3 — `web-security-session-origin` (domain B)
- Behaviour: loopback-trust model (no authn): signed anonymous session cookie (`ultraplan_session`, HMAC-SHA256 per-process secret, HttpOnly+SameSite=Strict, MaxAge 3600, no Secure flag — plain HTTP loopback), session-bound deterministic CSRF (constant-time compare on API headers; plain `!=` on `_csrf` form fields — asymmetry fact), Host pinning to listener-resolved numeric loopback authority, Origin tiering (mutations exact-origin or port-stripped-with-fetch-metadata+exact-Referer proofs; reads/SSE lenient-empty-Origin variants; static exempt), bind refusal for anything non-loopback (double validation pre-listen and post-resolve, `serve_commands.go:117-139`, `server.go:43-74`), body/framing discipline (64 KiB bodies on allowlisted routes only, duplicate CL / CL+TE rejection, DisallowUnknownFields JSON), security headers (CSP, nosniff, DENY, Referrer-Policy, Permissions-Policy, no CORS anywhere), abuse caps (32 in-flight requests, no per-session op cap), redaction before projection (`safeProjectedText` markers + `[redacted path]` + 4096 bound; tool payloads sanitised upstream in app `captureToolObservation`/`redactObservableValue`; normalized access-log buckets; `safeOperationCause` whitelist).
- Files: `internal/web/{security,server_policy}.go`, middleware attach points in `server.go`/`handlers.go`, `cmd/ultraplan/main.go:45-62` (argv-array browser launch, target always `http://<loopback>/`).
- Inventory facts: `validCommandOrigin` docstring describes port-stripping tolerance that its equality body does not implement — tolerance actually lives one layer up in `validCommandRequestOrigin` (`security.go:269-298`); `validOrigin`/`validOperationReadOrigin` have no production callers; session validity = HMAC validity until process restart (no embedded expiry); no HSTS/COOP/COEP/CORP headers; open-browser failures never fail the server.
- Key tests: `security_test.go` (host/origin matrices, port-strip proof requirements, framing, limits, log normalization, no-CORS), `server_policy_test.go` (fail-closed coherence), `TestPreparationStoreBindingExpiryReplayAndCapacity`, templates_test.go (JS CSRF retry-once).
- Risk: high — everything browser-reachable crosses here; comment/body drift and the form-vs-API comparison asymmetry are exactly where silent control weakening would hide.

### IC1 — `tui-dashboard-navigation-projection` (domain C)
- Behaviour: bubbletea alt-screen dashboard; explicit `Routes` stack with exactly 14 route kinds across Projects/Studies/Runs tabs; typed key map (`keys.go:24-55`); read-only projections from Dashboard/Runs/RunEvents/QA-bounded summaries/artifact previews (allowlist + symlink rejection + 32 KiB cap, `usecases.go:214-242`); glamour markdown rendering with raw fallback; wholesale data replacement on refresh (manual `r`, post-terminal, 1 Hz tick only while a run view is open — no background poller otherwise, so external CLI/web changes appear only via these refreshes).
- Files: `internal/tui/{app,model,views,viewport,theme,markdown}.go`, glue `internal/app/tui_commands.go:21-61`.
- Inventory facts: byte-slicing truncation (`truncateForDisplay` at 300 bytes; `boundContent` at `PreviewByteLimit` bytes) is rune-unaware; wrap-induced extra rows not modeled by line-count viewport; stale-response guards exist only for route-scoped messages (Preview/Validation/Confirmation), not Load/Refresh; `displaySafe` applied to findings/errors but not preview Content.
- Key tests: `views_test.go` (path-leak negatives, markdown engaged, ordering), `viewport_test.go`, `qa_view_test.go` (verdict-neutral rendering forced fail), `model_test.go` navigation/focus.
- Risk: normal — misprojection/truncation class rather than state corruption.

### IC2 — `tui-operation-lifecycle-cancel` (domain C)
- Behaviour: mandatory CONFIRM dialog pipeline (PrepareOperation → rendered Confirmation → Enter → beginOperation computes sha256(CanonicalRequest+NUL+fingerprint) digest → DurableOperationManager.AcceptOperation accept-before-goroutine, Existing dedup message); foreground event stream (128-buffer channel, emit-side non-blocking sends are silently dropped when full; RecordOperationEvent errors other than ErrWebUnavailable also stop forwarding; model keeps last 100 events); quit refused while Running (hides pane, demands `c`); Esc detaches display without cancelling; `c` semantics are overloaded in priority order: active durable op → `runs.CancelRun("user_requested")`; running w/o durable id → local ctx cancel; RouteRun → CancelRun + refresh; else selected study → `beginOperation({OperationStudyCancel})` directly, bypassing PrepareOperation/AcceptOperation because `Confirmation == nil` gates the accept block (`app.go:235,138-139`) — verified by mapper.
- Files: `internal/tui/app.go`, `model.go`, `views.go`, `qa_view.go`; manager `internal/app/durable_operations.go:97-156`.
- Inventory facts: enum kinds `sprint-stage`/`sprint-stage-dry-run` and `validate` are never constructed by any TUI nav item (validate goes straight to `UseCases.Validate` via `validationCmd`, skipping confirm/durability entirely); `OperationStudyStart` appears only in tests/render branches — nav always sends `OperationStudyResume` even for first run (parallelism form default 3, validated 1–64); success message "durable cancellation requested" stored in the Error field (`app.go:127`); no TUI string-vocabulary parity test exists (typed constants only); teaModel-level tests use fakes without DurableOperationManager, so the real accept/stream plumbing is exercised only indirectly via `app/tui_commands_test.go:52`.
- Key tests: `TestOperationConfirmationProgressBoundAndTerminal`, `TestEscapeHidesForegroundRunWithoutCancelling`, `TestCancelKeyWorksInsideRunView`, `TestRunLoopParallelParameterEntry`, `TestDurableRunListDetailUsesCanonicalVocabulary` (+tick re-arm), `TestQAViewUsesVerdictNeutralTextAtNarrowWidth`.
- Seams: shares S9 acceptance/cancellation seams with web; non-TTY chain derives from bubbletea v1.3.10 module cache (`/dev/tty` fallback then exit 1; dependency-source-derived, not executed).
- Risk: normal-high — the confirm/durability bypass paths and silent event drops define what the user believes happened.

### ID1 — `cross-interface-usecase-vocabulary` (domain D)
- Behaviour: the convergence fabric itself: `dashboardUseCases` implements ReadOnlyUseCases+WebOperations(+RunUseCases/DurableOperationManager optional capabilities returning ErrWebUnavailable when nil); `webUseCases` embeds it by value and adds additive capability interfaces (prompt/resources/models/sprint-create/dimensions); `sharedOperationRunner` maps OperationKind → product service calls with OperationEvent emission; closed 27-kind vocabulary pinned to browser strings by `TestBrowserOperationKindContract` and to shipped JS by regex-extracting `static/js/sse.js` stableEvents; cancellation converges on `runcontrol.Repository.RequestCancellation` from all three frontends (reason hard-coded `user_requested` in web/TUI, whitelisted in CLI); observation converges on `repositoryRunUseCases` (identical 512/250ms-1s algorithm in CLI follow and web SSE; TUI polls ≤200 events at 1 Hz instead).
- Files: `internal/app/{operations,usecases,run_usecases,web_usecases,durable_operations,operation_runner,surfaces}.go`; parity pins `internal/web/operations_contract_test.go`, `api_compatibility_test.go`.
- Divergence register (facts): CLI-only — `ultraplan code`, diagnostics/support export, sprint metrics, execute defer, study reset interactive confirm, storage migrate, defaults/skills install; web-only — sprint create, models endpoint, PromptBundle API, timeline API, study resources, roadmap join view, HMAC artifact refs; TUI-only — parallelism form, overloaded `c`; kind asymmetries — CLI never emits `execute-resume` (`--resume` still accepted as `execute-start`, `sprint_commands.go:338`), web form defaults `study_run_loop` → `OperationStudyStart` while CLI/TUI always send resume-kind, `study-cancel` mapped+contract-tested for web but no shipped template/JS issues it; single-stage `FlowStage` reachable only behind the operation layer (no CLI verb); durable acceptance mechanisms differ — CLI `beginDurableCLICommand` synchronous with empty digest (no dedup alias) vs web confirmation-token-derived dedup key vs TUI canonical+fingerprint digest; QA-cancel ownership precheck exists only on the CLI/ad-hoc path (`sprint_usecases.go:716-737`) — generic web/TUI run cancel performs no such target validation; validate/prompt wiring differs per frontend (CLI local switches vs `validateSprintStage`/`promptSprintStage` helpers vs web PromptBundle duplicating the switch again).
- Workspace-contract cross-reference (CURRENT-CONTRACT): `system/contracts/surfaces/cli.md` (stable exit codes, stdout/stderr discipline, destructive-action explicitness, non-interactive determinism) and `api-contracts.md` (versioned schemas, stable error codes, pagination, backpressure) are the applicable anchors reviewers should diff implementation against; `frontend.md` targets a `frontend/src/...` ownership tree that does not exist in this repo (Go-embedded templates/JS instead) — treat as generic template context, HISTORY-like for this target.
- Risk: critical-as-lens — any drift between the three frontends' semantics (kinds, durability, cancellation reasons, classification) produces "same action, different outcome depending on door", which is the costliest interface defect class in this product.

---

## Seam register (interface-specific)

1. **Durable acceptance ×3 mechanisms** — `beginDurableCLICommand` (empty digest, sync) / web `startConfirmed` (token dedup key, transient fallback on ErrWebUnavailable) / TUI `beginOperation` (canonical+fingerprint digest, gated on Confirmation != nil). Same repository.Accept underneath.
2. **Operation-kind vocabulary** — Go enum ↔ browser strings (contract-tested) ↔ TUI typed constants (compile-time only, no exhaustive parity test).
3. **Cancellation** — three doors, one `RequestCancellation`; study-cancel is a separate lock-signal mechanism, not a durable cancellation record.
4. **Observation** — one repository; three formats (text/envelope NDJSON, {data,meta} JSON+SSE, rendered TUI); omissions surfaced in all three.
5. **Workspace resolution** — single `discoverWorkspace` for all frontends; serve adds no overrides; `defaults`/`skills` bypass discovery using the raw flag.
6. **Redaction layers before human eyes** — config.RedactValue (CLI/TUI display), safeProjectedText (web hub), redactObservableValue (tool payloads upstream), safeOperationCause (error gating) — four layers, different marker sets.
7. **Confirmation authority** — fingerprint server-issued, caller-supplied values rejected (zeroed in Prepare, DisallowUnknownFields at transport, re-checked at start); TUI/web honour it, CLI has no fingerprint concept.
8. **Cleanup uncertainty** — web drain persists markers; study owner reconciles; CLI interrupt paths write equivalent markers.

## Reviewer prioritisation (risk rationale only)

1. **ID1** cross-interface divergence — same-action-different-semantics class (acceptance digests, kind asymmetries, QA-cancel precheck gap).
2. **IB2** operation hub/SSE — user intent → durable child work; token consume, drain, replay-gap honesty.
3. **IB3** security controls — origin/comment drift, form-vs-API CSRF asymmetry, loopback trust model boundaries.
4. **IC2** TUI lifecycle — confirm bypass paths, silent emit drops, overloaded cancel key.
5. **IA1** CLI contract — exit-code/text-sniffing inconsistencies, parser quirks, undocumented automation surface.
6. **IB1/IC1/IA2** — normal-high: projection honesty (truncation, staleness, retention gaps).

## Neutral inventory facts carried forward

- `validCommandOrigin` docstring vs strict-equality body (`security.go:269-274`); dead helpers `validOrigin`/`validOperationReadOrigin` (definition-only).
- Cookie lacks `Secure`; session expiry is browser-side MaxAge only; HMAC valid until restart rotates secret.
- HTML `_csrf` compares with `!=`; API header compare is constant-time.
- No per-session operation cap (global 8 ops / 32 streams / 32 in-flight).
- TUI: silent emit-side drops; success-cancel text lands in Error field; rune-unaware truncation; no background poller when no run view open.
- Enum entries `sprint-stage(-dry-run)`/`validate` unreachable from TUI; web `study-cancel` has no UI issuer.
- CLI: empty acceptance digest (no dedup alias); error-text sniffing in sprint paths; QA cancel→8 vs sprint cancel→7; undocumented commands/flags list above; `skillsMaterialiseHelp` omits `reconcile`.
- Web: sprint-create outside token flow; SSE terminal reload vs durable-run no-reload inconsistency; QA API unwrapped structs; artifact refs die on restart.
- `docs/ui-audit.md` is UX-only (no security content). `frontend.md` contract describes a nonexistent tree.
- Non-TTY TUI behaviour sourced from bubbletea v1.3.10 module cache, not executed.
