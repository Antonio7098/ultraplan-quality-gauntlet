The events log confirms this session IS attempt-01 of job `context-process-execution`. Let me check how the orchestrator captures results before deciding where to write artifacts.The runner persists my final message as `result.md`, which downstream reviewers read. Now let me study the surface. First the primary paths:Now let me map callers, helpers, and tests around this surface.All evidence gathered. Producing the context pack now.

# Context pack: `process-execution` — Subprocess control and spawn policies

Target: ultraplan-go @ `8eef70f4903b25580719960009a170945bdad9ad`. This pack describes what the code does; it reports no findings and renders no judgments.

## 1. Purpose

The surface grants execution capability to child processes outside the AgentWrap agent runtime:

- `internal/platform/process` — a product-agnostic direct-process boundary (`DirectRunner`) that spawns an executable with explicit argv/dir/env only, owns a Unix process group, enforces a timeout with a SIGTERM→SIGKILL group ladder, bounds captured output, and reports cleanup completeness.
- `internal/sprint/smoke_protocol.go` — translation of a validated protocol-v1 smoke-harness manifest (from the project catalog) into `DirectRunner` invocations (discovery + run), with containment resolution, env intersection, timeout resolution, strict single-JSON decoding, discovery/scope/evidence validation, and typed process-error classification.
- `internal/sprint/qa_prompt.go` — the approved-check policy for read-only QA: a map-owned check catalog (currently one deterministic `gofmt -d` descriptor), interpreter/metachar/write-mode/path/env restrictions, fingerprint ownership, parent-side env lookup with PATH-less child env, post-run target-identity drift detection, and digest-only command summaries.

Canonical seams touching this surface: `smoke-manifest-runner` (sprint-smoke-gate → process-execution) and `qa-approved-check-policy` (sprint-qa-investigation → process-execution).

## 2. Entrypoints and control flow

### 2.1 `DirectRunner.Run(ctx, Request)` — internal/platform/process/process.go:60

Validation before spawn (process.go:62-82): non-nil ctx; non-empty `Executable`; non-empty `Dir`; `Timeout > 0`; limits default to 4 MiB stdout / 1 MiB stderr (`DefaultStdoutLimit`/`DefaultStderrLimit`, process.go:15-19); `CleanupGrace` defaults to 5 s.

Spawn (process.go:84-103): `exec.Command(Executable, Args...)` — no shell; `cmd.Dir = req.Dir`; `cmd.Env = append([]string(nil), req.Env...)` (explicit-env-only; empty `Env` yields an empty child environment, no parent inheritance); `configureOwnedProcess(cmd)` sets `SysProcAttr.Setpgid = true` on linux/darwin (process_unix.go:12) and is a no-op elsewhere (process_other.go:11). Stdout/stderr are pipes drained by two goroutines into `limitedCapture`s (process.go:88-107, 154-183): each capture keeps the first `limit` bytes, sets `truncated` on overflow, and always accepts writes so capture never backpressures a pipe. `copyStream` reads 32 KiB chunks, writes them to the capture, and emits a progress `Event{Stream, Data, At}` per chunk (process.go:185-197). Events go through a `dispatcher`: buffered channel of 128 plus a dedicated sink goroutine; when full, events are counted as dropped, never blocking (process.go:199-242).

Wait/timeout/cancel select (process.go:108-137): `cmd.Wait()` runs in a goroutine feeding a 1-buffered channel. First of {wait done, ctx.Done, timer} wins:
- wait done → normal path;
- ctx.Done → `Cancelled=true`, `CleanupAttempted=true`, then `stopAndWait`;
- timer → `TimedOut=true`, `CleanupAttempted=true`, then `stopAndWait`.

`stopAndWait` (unix, process_unix.go:14-35): `Getpgid(pid)`; on failure returns `<-waited, false` (cleanup incomplete). Otherwise `kill(-pgid, SIGTERM)`; waits up to grace for Wait; a final `kill(-pgid, SIGKILL)` fires in both branches (the comment at process_unix.go:27-28 states it closes the leader-exits-before-descendants race); `CleanupComplete=true` iff Wait returned. Non-unix variant (process_other.go:13-24): `Process.Kill()`, bounded select; may return `(nil, false)` — cleanup incomplete with nil wait error — if the child survives past grace.

Result assembly (process.go:124-151): `DroppedEvents` from dispatcher close; `ExitCode` from `ProcessState.ExitCode()` with `-1` sentinel when `ProcessState == nil`; `Signal` from `WaitStatus.Signaled()` (unix only, process_unix.go:37-42; empty elsewhere); `!CleanupAttempted ⇒ CleanupComplete=true`. Error mapping: timeout → `"process timed out after %s"`; cancel → `context.Canceled`; `*exec.ExitError` → `"process exited with code %d"` (Result still fully populated); other wait errors wrapped. The `Runner` interface (process.go:54-56) is the injection seam: production `sprint.Service` defaults to `DirectRunner{}` (service.go:76); `WithProcessRunner` exists for tests (service.go:55-56; used in qa_prompt_test.go:144, smoke_test.go:362; no production caller found).

### 2.2 Smoke manifest → runner translation

Preparation — `prepareSmokeStatic` (smoke_protocol.go:111-211):
1. Resolve sprint inputs; require exactly one `SectionSmokeHarnesses` catalog row (smoke_protocol.go:116-125).
2. Resolve target implementation dir via `resolveSprintTarget` (must be valid or smoke aborts).
3. Containment chain, all resolved through `filepath.EvalSymlinks` and checked with `inside(root, …)` (artifacts.go:77-83): harness root = `canonicalDirectory(entry.Path)` (:130); manifest path = `canonicalFileInside(root, …)` (:138); executable = manifest `executable`, absolutized against root then `canonicalFileInside` (:153-160); CWD = `canonicalDirectoryInside(root, …)` (:161-168); evidence roots Runs/Issues = `canonicalDirectoryInside(root, join(root, manifest.Evidence.*))` (:169-176).
4. Manifest bytes decoded by `decodeOneJSON` — exactly one JSON object; trailing values rejected (smoke.go:625-637).
5. `validateSmokeManifest` (smoke_protocol.go:213-258): `schemaVersion==1` and protocol major == `SmokeProtocolMajor`=1 (smoke_types.go:15); required harness ID/version, executable, cwd, Discover/Run commands, both evidence roots; optional `defaults.timeout` parsed as positive Go duration ≤ 24 h; capabilities must include `discovery`, `run`, `evidence-v1`, `scope-mapping`, `authoring-v1`; authoring paths non-empty, slash-cleaned, unique, `safeRelPath` (execute_state.go:275-281), pairwise non-overlapping (`smokePathsOverlap`, :581-585), disjoint from both evidence roots; environment names match `validProtocolEnvName` (uppercase/underscore/digits-after-first, :563-573), unique.
6. Review gate (same function, :177-209): fresh review required; staleness computed from `PrepareReview` findings, optional compile-time freshness switch (`strictCompletedReviewSnapshotFreshness=false`, freshness_policy.go:12), content validation and `ArtifactDigest` equality; verdicts `pass`/`pass_with_findings` proceed; `fail`/`blocked` require `ForceReview` + `OverrideConfirmed` + non-empty rationale; other verdicts rejected.

Environment construction — `smokeEnvironment` (smoke_protocol.go:623-642): name set = configured allowlist (`settings.Environment`) intersected with manifest-declared names (manifest names are added only if already allowed); values fetched parent-side via `settings.Getenv` (default `os.Getenv`); names with empty values omitted; result sorted `NAME=value` pairs. Defaults: sprint `DefaultSmokeSettings()` = `[PATH HOME TMPDIR LANG LC_ALL]`, discovery 30 s, run 30 m, grace 5 s, caps 4 MiB/1 MiB (smoke_types.go:54-56); config default identical (config/config.go:189); overridable via `ULTRAPLAN_SMOKE_{DISCOVERY_TIMEOUT,RUN_TIMEOUT,STDOUT_LIMIT,STDERR_LIMIT,CLEANUP_GRACE,ENVIRONMENT}` (config.go:132-136) and assembled per-door by `app.smokeSettings` (app/sprint_commands.go:814-823).

Discovery invocation (smoke.go:89-110): argv = `manifest.Args + manifest.Commands.Discover + ["--target", prepared.Target]`; Dir = manifest CWD; Env as above; Timeout = `settings.DiscoveryTimeout`; stdout truncation fails closed (`smoke_discovery_truncated`). Stdout must decode to one JSON object and pass `validateSmokeDiscovery` (smoke_protocol.go:260-373): identity equality against the manifest (schema, protocol string, harness ID, evidenceSchema=1); unique IDs across prerequisites/levels/suites/tests; prerequisite status ∈ {available, blocked}; referential integrity (level→suite, suite→test with ownership consistency, suite→prerequisite, test→suite); sprint-mapping rules including coverage-ID completeness for `complete` mappings (legacy incomplete mappings tolerated, comment :338-340).

Selection — `selectSmoke` (smoke_protocol.go:441-540): explicit `--level/--suite/--test` overrides (each checks prerequisite availability; level/suite may be "complete" only if backed by a complete mapping covering them; test override is always diagnostic-only); otherwise the unique complete sprint mapping's suites form the scope; missing/incomplete mapping ⇒ verdict `blocked`. Verdicts `blocked`/`not_applicable` short-circuit to commit without running tests (smoke.go:129-135).

Run invocation (smoke.go:140-167): argv = `Args + Commands.Run + [--project, --sprint, --workspace s.root, --target, --scope-kind, --scope <ids joined>]`; `SafeArgv` redaction recorded before launch (:142, safeArgv :638-657 keeps option names, replaces values with `[ARG]`/`[REDACTED]`); Timeout = effective timeout from `smokeTimeout` precedence (smoke_protocol.go:644-657): request > settings whose `Sources["smoke.run_timeout"]` is workspace/env > manifest default (validated ≤ 24 h) > settings default; `Progress` callback emits phase-only messages ("harness progress") — child stream data itself does not leave this package except via captures. Post-run gates: dropped events recorded as diagnostics (:151-153); run-stdout truncation fails closed (:154-156); decode failure falls back to process-error classification when a run error exists (:157-164); `TimedOut || Cancelled || !CleanupComplete` classified before response use (:165-167).

Evidence validation — `validateSmokeRun` (smoke.go:304-412): identity equality (schema/protocol/harnessID/non-empty RunID); reported scope equals requested scope; counts non-negative and sum to total; ≥1 evidence item and non-negative duration; executed test identities must equal the discovered expected set exactly (sorted `\x00`-joined comparison, :342) with statuses ∈ {passed, failed, skipped, error}; evidence paths resolved inside declared roots (`canonicalFileInside(RunsRoot|IssuesRoot)`), optional SHA-256 equality, non-issue evidence file names must contain the RunID; issue records need valid ID/status, issue-ID-addressed paths under the issues root, and full metadata for open issues; every failed/errored test requires an open detailed issue; non-zero exit with zero failed/error counts rejected (`smoke_process_unexplained`).

Error classification — `classifyProcessSmokeError` (smoke.go:603-614): `result.TimedOut` → category `timeout`; `Cancelled || context.Canceled` → `cancellation`; `!CleanupComplete` → `cleanup` ("Terminate owned descendants before retrying"); else `process`.

Orchestration wrapper — `RunSmoke` (smoke.go:21-61): dry-run bypasses locking/persistence entirely; otherwise acquires the sprint mutation lease, persists a `running` attempt record before any harness work (`saveSmokeAttempt` :31-33, :191-232), runs, persists terminal state, marks roadmap delivered and publishes git artifacts downstream on non-diagnostic pass (adjacent surfaces). In `runSmoke`, authoring is skipped on dry-run but the discovery subprocess executes regardless; the run command is never spawned on dry-run (:82-87, :143-147).

### 2.3 QA approved-check policy

Catalog construction — `ApprovedQAChecks(target, changedPaths, budgets)` (qa_prompt.go:59-87): target must be absolute; changed paths normalized (`normalizeQAStrings`, qa_types.go:754) and validated (`validateQAPath`, qa_map.go:287-292: relative, not `.`/`..`, no `../` prefix, no NUL/CR/LF); `.go` paths filtered; none ⇒ nil catalog (no approved checks). Single descriptor: `{ID: "go-format-diff", Executable: "gofmt", Args: ["-d", <goPaths…>], WorkingDirectory: cleaned target, Timeout: budgets.CommandTimeout, OutputLimit: budgets.CommandOutputBytes}`; `Fingerprint` = SHA-256 over canonical JSON (`canonicalQAJSON`, qa_types.go:495-503 — std encoder, HTML escaping off, struct field order) of the descriptor with its Fingerprint field blanked (`fingerprintQAValue`, qa_map.go:380-387).

Policy — `validateQACheckDescriptor` (qa_prompt.go:89-125): non-blank ID/executable; lowercase-basename denylist `sh bash zsh fish cmd powershell pwsh python python3 perl ruby node git`; working directory must equal the cleaned target; `0 < Timeout ≤ budgets.CommandTimeout`; `0 < OutputLimit ≤ budgets.CommandOutputBytes`; per argument: reject NUL CR/LF `| ; & > < \` $ ( )`; reject `-w` exact and any `--write` prefix (write modes); non-flag arguments must be relative and satisfy `inside(target, filepath.Join(target, arg))`; environment names restricted to exactly `LANG LC_ALL TZ` (`validQAEnvironmentName` :127-134).

Map integration: `BuildQAMap` builds the catalog from execute-reported changed paths (qa_map.go:106-112) and copies `QAApprovedCheckRef{ID,Fingerprint}` refs into every primary/boundary shard (:220, :244); the investigator prompt packet carries those refs and instructs check requests to contain "only the exact ID and fingerprint from approved_checks" (prompt text qa_prompt.go:145-152; packet struct :27-38); investigator output `Checks` are capped by `CommandsPerAttempt` (qa.go:604) and each must resolve to the map-owned catalog (qa.go:630-640). Shard investigation itself is fenced by before/after `targetIdentity` comparisons and permission-enforcement verification (`restricted`/`deny`/`UnsupportedCount==0`, qa.go:550-577) — that loop belongs to `sprint-qa-investigation` but lives around this surface's executor.

Execution — `Service.RunApprovedQACheck(ctx, qaMap, descriptor, requestedRef)` (qa_prompt.go:243-285), ordered:
1. `descriptor.ID/Fingerprint` must equal the requested ref (else `QAErrorPermissionDenied`);
2. ref must appear in some shard's `ApprovedChecks` (else denied);
3. descriptor revalidated under current budgets/policy;
4. `targetIdentity(descriptor.WorkingDirectory)` captured before (`targetIdentity`, verify.go:349-443: absolute path, Lstat symlink-reject on root; git repos hashed via `git rev-parse --show-toplevel`/`HEAD` + `git ls-files -co --exclude-standard` per-path content hashing with 64 MiB-per-file bound, symlink containment inside root; non-git roots fully WalkDir-hashed);
5. child env built only from `descriptor.Environment` names present in the parent environment via `os.LookupEnv` (:266-271) — catalog descriptors have empty `Environment` by construction, so the `gofmt` child receives an empty env while bare-name lookup resolves parent-side via `exec.LookPath`;
6. `processRunner.Run` with Dir = working directory (= target), Timeout = descriptor.Timeout, both stream caps = OutputLimit, CleanupGrace = `budgets.CleanupTimeout` (:272);
7. identity re-captured after; any mismatch ⇒ `QAErrorPermissionDenied` "target identity changed during the approved check" even on exit 0 (:273-276);
8. summary built digest-only: `QACommandSummary{CheckID, DescriptorFingerprint, ExitCode, Duration, StdoutDigest, StderrDigest, OutputBytes, Truncated}` — raw streams discarded after hashing (:277);
9. run error (non-zero exit, timeout, cancel, cleanup) ⇒ `QAErrorRuntimeUnavailable` (:278-280); `OutputBytes > 2×OutputLimit` or any truncation ⇒ `QAErrorBudgetExhausted` (:281-283).

Budget defaults (qa_types.go:114-127): `CommandTimeout` 5 m, `ShardTimeout` 20 m, `CleanupTimeout` 30 s, `CommandOutputBytes` 256 KiB; maximum tier raises several ceilings (qa_types.go:129-142). Settings freeze via `WithQASettings`/`effectiveQASettings` (service.go:159-190).

Same-file adjacency: `RenderQAInvestigatorPrompt` (:136-157) and `QAInvestigatorRequest`/`QAChallengerRequest` (:159-241) build `pruntime.Request`s for the AgentWrap runtime (read-only sandbox, default-deny tool policy, path rules for shard paths; challenger denies every tool). Those requests travel the opencode-agent-runtime stack, not DirectRunner; they are listed here because they share the file and define what investigators may request.

## 3. Inputs and outputs

Inputs:
- project-index.md catalog entry (harness root path + relative manifest path);
- external manifest JSON (identity, executable, args prefix, cwd, discover/run commands, evidence roots, authoring paths, capabilities claims, env names, default timeout);
- external harness stdout (discovery JSON; run-response JSON) and stderr (captured but unused for parsing);
- config/env-derived `SmokeSettings` (timeouts, caps, env allowlist, getenv func);
- QA side: review/execute-derived changed-path list, frozen budgets, investigator-requested check refs, live process environment (for LANG/LC_ALL/TZ lookup), target tree state (via git/filesystem).

Outputs:
- spawned processes (harness binary twice per smoke run; `gofmt -d` per requested check; `git` invocations from `targetIdentity`);
- `pprocess.Result` (exit code, signal name, capped streams + truncation flags, dropped-event count, timing, TimedOut/Cancelled/CleanupAttempted/CleanupComplete);
- smoke: validated evidence/issue/test structures, verdict, coverage mapping, SafeArgv string, typed `SmokeError`s → smoke.md + flow-state (downstream surfaces);
- QA: `QACommandSummary` digests recorded into shard attempts; typed `QAError`s.

## 4. Authoritative state

This surface owns no durable state of its own. It reads/derives from:
- cataloged harness root and its manifest (external, owned by the harness);
- flow-state review/smoke stage records and smoke.md (authority of sprint-flow-state);
- QA map/shard/attempts state (authority of sprint-qa-investigation, stored under `verification/state.json` + attempts per canonical map).

State it causes others to write: children write within the manifest CWD (harness root) and declared evidence roots; smoke results land in flow-state/smoke.md only via `commitSmoke` (sprint-smoke-gate); QA summaries land in QA attempt records (sprint-qa-investigation). Temporary files: `atomicWriteFile` temp files in the sprint directory during commit (smoke.go:714-746) belong to the smoke-gate surface.

## 5. Invariants (as coded)

DirectRunner:
- argv-only exec, no shell interpolation anywhere in this package.
- explicit-env-only: child env is exactly the caller's slice copy; nothing inherited implicitly.
- owned process group on unix (`Setpgid`); termination addresses `-pgid` (group-wide), TERM then KILL with a final group KILL after graceful exit.
- `CleanupComplete` is true iff Wait returned after a cleanup ladder (or no cleanup was needed).
- bounded capture: overflow is flagged, never blocked; pipes always drained concurrently.
- progress delivery is lossy by design: drops counted in `DroppedEvents`, never fatal.
- required parameters enforced pre-spawn; timeout must be positive.

Smoke translation:
- every filesystem-derived path (root, manifest, executable, cwd, evidence, evidence items) is symlink-evaluated and containment-checked against the harness root or declared sub-root.
- single-JSON-object protocol on both discovery and run channels; truncated discovery/run stdout fails closed.
- executed test identities must equal discovered expectations; counts must be arithmetically consistent; failed tests require filed issues; unexplained non-zero exits rejected.
- env forwarded is the sorted intersection of configured and manifest-declared names, values fetched at spawn time.
- timeout precedence is fixed (request > workspace/env setting > manifest default ≤ 24 h > settings default).
- cleanup incompleteness is a distinct failure category that blocks the run response from use.

QA checks:
- the check catalog is product-owned and deterministic (currently exactly `gofmt -d` over changed .go files); investigators can reference but never define checks.
- interpreter basenames, shell metacharacters, write-mode flags, absolute or escaping path arguments, and out-of-budget time/output are rejected.
- cwd is pinned to the target root; env names pinned to LANG/LC_ALL/TZ; child env omits everything else (including PATH) while executable lookup stays parent-side.
- fingerprint (canonical-JSON SHA-256 over descriptor sans fingerprint) plus ID must match a map-owned ref.
- target-tree identity must be unchanged across execution; drift denies the check regardless of exit status; raw output never persists, only digests.

## 6. Trust boundaries

- Catalog entry + manifest JSON (user/harness-authored markdown-referenced content) become executable path, argv prefix, cwd, env names, timeouts, and capability claims. Controls: EvalSymlinks containment, schema/field validation, capability list requirement, timeout cap, env-name charset rule, authoring/evidence disjointness.
- Harness stdout/stderr re-enter as verification truth (discovery structure, run counts/tests/evidence/issues). Controls: identity equality with manifest/discovery, exact expected-test-set equality, count arithmetic, evidence containment/hash/run-ID addressing, issue completeness, unexplained-exit rejection, truncation fail-closed.
- Investigator LLM output selects which approved checks run. Controls: ID+fingerprint must match map-owned refs; per-attempt check-count budget; revalidation at execution time.
- Child processes receive capability: harness executable gets the intersected env and runs unsandboxed within its own root/CWD; QA checks get near-empty env and diff-mode argv. There is no OS-level sandbox in this package; containment is by path resolution, argv shape, env selection, and post-hoc identity verification.
- Parent-side lookups: `gofmt` resolves via the parent's PATH; smoke executable path comes fully resolved from the manifest.

## 7. External effects

- Spawns processes with caller-controlled argv/env/dir (harness ×2, gofmt ×N, git ×3-4 per `targetIdentity` call).
- Reads manifest and evidence files; hashes evidence (full file reads via `hashFile`).
- Sends SIGTERM/SIGKILL to process groups it created.
- Writes nothing durably itself; all persistence happens in adjacent surfaces (flow-state/smoke.md, QA state, roadmap/publication).
- No network I/O originates here.

## 8. Cancellation, timeout, retry, restart, error semantics

- DirectRunner: one-shot per call. Caller cancellation (ctx) and internal timeout both trigger the same TERM→KILL ladder with configurable grace; outcomes distinguished by `Cancelled` vs `TimedOut` flags and returned error (`context.Canceled` vs timeout error). `CleanupComplete=false` signals uncertain teardown (reachable on unix only via Getpgid failure; reachable on other platforms via post-KILL grace expiry).
- Smoke: mutation lease held for the whole run; running-attempt record persisted before spawn enables interrupted-run reconciliation by sprint-flow-state. No automatic retry of discovery/run — rerun means invoking the command again. Error categories (`timeout`, `cancellation`, `cleanup`, `process`, plus protocol/catalog/review_gate/persistence families) drive attempt status classification (`AttemptTimedOut`, `AttemptCancelled`, `AttemptBlocked`, `AttemptFailed`, smoke.go:234-250) and user guidance strings. Dry-run performs preparation + discovery only.
- QA checks: no retry; any run error ends the shard investigation with `QAErrorRuntimeUnavailable`; drift and permission failures fail closed with `QAErrorPermissionDenied`; budget exhaustion is distinct. Investigator runtime retries (taxonomy in qa.go:587-597) wrap around this and are re-fenced by identity checks per retry.
- Restart: nothing cached in memory between calls; every smoke run re-reads manifest/flow-state and re-runs discovery; every QA attempt rebuilds the check catalog from changed paths and re-captures identity.

## 9. Files, symbols, tests, contracts

Primary files:
- internal/platform/process/process.go — `Request`, `Event`, `Result`, `Runner`, `DirectRunner.Run`, `limitedCapture`, `copyStream`, `dispatcher`.
- internal/platform/process/process_unix.go — `configureOwnedProcess`, `stopAndWait`, `processSignal` (linux/darwin).
- internal/platform/process/process_other.go — stubs (other platforms).
- internal/sprint/smoke_protocol.go — `smokeManifest`/`smokeDiscovery`/`smokeRunResponse` types, `prepareSmokeStatic`, `validateSmokeManifest`, `validateSmokeDiscovery`, `selectSmoke`, `smokeEnvironment`, `smokeTimeout`, `canonicalDirectory(Inside)`, `canonicalFileInside`, `validProtocolEnvName`, `smokePathsOverlap`.
- internal/sprint/qa_prompt.go — `QACheckDescriptor`, `ApprovedQAChecks`, `validateQACheckDescriptor`, `validQAEnvironmentName`, `RunApprovedQACheck`, plus investigator/challenger packet+request builders.
- internal/sprint/smoke.go (invocation sites, `decodeOneJSON`, `classifyProcessSmokeError`, `safeArgv`, `validateSmokeRun`, `hashBytes`/`hashFile`), smoke_types.go (`SmokeSettings`, `DefaultSmokeSettings`, error types), service.go (runner field/default/injection, settings setters).

Supporting helpers: `inside` (sprint/artifacts.go:77), `safeRelPath` (sprint/execute_state.go:275), `targetIdentity` (sprint/verify.go:349), `fingerprintQAValue` (sprint/qa_map.go:380), `validateQAPath` (sprint/qa_map.go:287), `canonicalQAJSON`/`normalizeQAStrings`/budget types (sprint/qa_types.go).

Wiring: app builders pass `smokeSettings(effective, envLookup(deps.env))` at sprint_commands.go:88,710, operation_runner.go:80, serve_commands.go:59,77, tui_commands.go:44, usecases.go:127-128; smoke invoked from CLI (sprint_commands.go:549), durable operation runner (operation_runner.go:85, timeout parsed from request string, parse errors ignored → 0 → precedence fallback), and web/TUI preview paths (operations.go:290,481). Config keys documented in docs/configuration.md:122-126,230.

Tests:
- internal/platform/process/process_test.go (4): exact-env/cwd/capture (`TestDirectRunnerExactEnvironmentCwdAndCapture`); cancellation kills an owned background descendant verified via pid liveness/zombie check (`TestDirectRunnerCancellationCleansOwnedDescendant`); slow Progress sink doesn't block drain/truncation correctness (`TestDirectRunnerSlowProgressDoesNotBlockDrain`); timeout+bounds+ladder completion (`TestDirectRunnerBoundsAndTimeout`). Windows skips; package coverage 88.9% (baseline/go-test-cover.txt).
- internal/sprint/smoke_test.go: manifest rejection matrix (:487), discovery relationship rejection (:213), identity reuse across kinds (:226), scope/mapping completeness (:192, :260), env preservation incl. non-allowlist exclusion (:108), authoring path allowlist (:287), artifact commit/preservation on malformed run (:301), protected-write attribution (:130), gated real-harness lane requiring `ULTRAPLAN_REAL_SMOKE=1` plus workspace/project/sprint env (:522-534). Uses `smokeRecordingRunner` fake (:38).
- internal/sprint/qa_prompt_test.go: investigator/challenger request policy (:13, :47), catalog argv determinism/order-independence (:70), policy rejections shell/git/write/escape/env (:85), unowned-ref rejection + target-drift detection using a fake runner (:122-163), `targetIdentity` symlink behavior (:165). Uses `qaProcessRunner` fake (:109).
- sprint package coverage 70.9%.

Contracts (CURRENT-CONTRACT):
- TRD (workspace projects/ultraplan-go/docs/TRD.md): platform role "safe external executable/argv execution for the smoke harness" (line ~79); detailed run/issue evidence belongs to the external harness; no Git mutation during smoke; read-only QA phase with bounded shards (lines ~2048, 2232).
- docs/configuration.md:230: "UltraPlan passes only named environment variables; values never appear in config output... The built-in platform set is limited to TMPDIR, LANG, and LC_ALL; PATH and HOME are not forwarded by default."
- Workspace skill ultraplan-smoke/SKILL.md: protocol-v1 harness discoverability, review gate, diagnostic override discipline.
- Canonical seams: `smoke-manifest-runner` — "exec-not-shell argv, env intersection of PATH/HOME/TMPDIR/LANG/LC_ALL with manifest requests admitted only if already allowlisted, timeouts capped 24h, truncation fails closed, group SIGTERM->SIGKILL cleanup must complete before verdicts"; `qa-approved-check-policy` — "interpreter denylist/metachar ban with cwd==target, LANG/LC_ALL/TZ-only env, and post-execution target-identity drift detection; child env omits PATH while lookup happens parent-side."

Neutral observation (REALITY vs CURRENT-CONTRACT, for reviewer awareness): docs/configuration.md:230 states PATH/HOME are not forwarded by default, while config default `smoke.environment` includes both (config.go:189), `DefaultSmokeSettings` includes both (smoke_types.go:55), and `TestDefaultSmokeEnvironmentPreservesInterpreterPath` pins forwarding of PATH and HOME (smoke_test.go:117-121).

## 10. Immediate surface dependencies

Upstream consumers: `sprint-smoke-gate` (sole smoke caller chain), `sprint-qa-investigation` (check catalog + execution inside shard loops), app wiring layer (`config-inspection-health` supplies effective config/sources/env lookup; `cli-dispatch-exit-contract`, `web-operation-hub-sse`, `tui-console`, `shared-usecase-vocabulary` deliver requests).
Shared helper owners: `sprint-flow-state` (flow-state load/save used in prepare/commit), `sprint-conformance-review` (PrepareReview/validators used by the smoke review gate), `repo-publication` (downstream publication), `project-catalog` (catalog entry source).
Downstream recipients: flow-state/QA-state writers consume this surface's results; nothing else imports `internal/platform/process` besides sprint.
Explicitly adjacent but different surfaces: AgentWrap/opencode binary spawning (opencode-agent-runtime has its own invocation path in internal/platform/runtime/opencode.go and does not use DirectRunner), gitpublish git execution (repo-publication), study clone execution (study-authoring), direct `git` calls inside `targetIdentity`/`qaGitIdentity` (verify/qa_map, consumed by smoke-gate and QA surfaces too).

## 11. Explicit unknowns

- Whether `WithProcessRunner` has any production caller (none found; appears test-only).
- Non-unix lane behavior (`process_other.go`): no automated tests exercise it (tests skip windows); the `(nil, false)` cleanup-incomplete return path is unpinned.
- Behavior when a spawned descendant escapes the owned process group (e.g., calls `setsid`): group-addressed signals would not reach it; no test or code handles this case.
- Upper bound of request-sourced smoke timeouts on the durable-operation path: CLI parses `--timeout` with a 24 h cap (sprint_commands.go:843-848) and manifest defaults are validated ≤ 24 h, but `smokeTimeout` itself applies no cap to request-sourced durations, and operation_runner.go:82-84 silently ignores `ParseDuration` errors (yielding 0 → precedence fallback). Whether any door enforces a ceiling there is determined upstream of this surface.
- `targetIdentity` cost profile on large non-git targets (full recursive content hash, 64 MiB per-file read bound); called before/after every investigator retry and every approved check.
- Consumers of `Result.Signal` beyond tests (none found in sprint/app code).
- Cross-run staleness of external evidence between discovery and run-validation (mitigated by run-ID naming and hash checks at validation time; nothing re-validates evidence after smoke.md commit — owned by the smoke-gate surface).
- Exact provenance intended by the documentation sentence about PATH/HOME (recorded above as a factual divergence; intent unknown from code alone).