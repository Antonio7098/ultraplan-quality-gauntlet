# Context Pack: `project-catalog` — Project discovery, catalog, roadmap

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: foundation. Risk: normal. Descriptive only — no defect judgments.

## 1. Purpose

Read-only discovery and inspection of project planning roots under `projects/<name>/`: directory discovery with safe-name admission, prefix-tolerant reference resolution, `project-index.md` catalog parsing (six recognized sections), governed roadmap parsing (`## Phase` / `### Sprint N` grammar with slug/status/dependency metadata), roadmap-vs-sprint-directory validation, and a three-tier reasoning-default resolution chain (project → workspace → built-in). The parsed catalog is a governing input: later sprint planning, review, smoke, and execute stages re-parse the same file and steer stage behavior from its entries. The package owns exactly one deliberate write path — `MarkRoadmapSprintDelivered` — which is not reachable from this surface's CLI verbs but is invoked by the sprint smoke stage after a passing non-diagnostic smoke run (delivery marking delegated to publication flow).

## 2. Entrypoints and control flow

### 2.1 CLI (`internal/app/project_commands.go`)
`runProject` :10-75 dispatches after `discoverWorkspace` (requires `ultraplan.yml` marker, app.go:287-319) and builds `project.NewService(root.Path)` per invocation (stateless; every call re-reads disk):
- `project list` (:38-52) → `DiscoverProjects`; prints workspace path plus sorted names, `(none)` when empty.
- `<ref> status` (:53-59) → `Service.Status` → `renderProjectStatus` (:85-111): docs state/count/listing, roadmap/index/sprints presence, sprint dirs, reasoning-default sources (content stripped), area-reasoning documents, catalog health, finding count.
- `<ref> validate` (:60-69) → `Service.Validate` → findings to stderr as `severity section= entry= path= problem= cause= suggestion=` lines (:113-130); returns `classedError{ExitValidation}` whenever `len(Findings) > 0` (:66-68), regardless of severity; stdout still reports the computed status string.
- Error mapping `mapProjectError` (:77-83): `project.RefError` → ExitValidation (5); everything else → ExitWorkspace (4). Usage errors → ExitUsage (2). No `--json` variant exists for these verbs.
- Help texts :132-175 match docs/cli-reference.md:119-143 wording.

### 2.2 Discovery and resolution (`discovery.go`)
- `DiscoverProjects` :29-47: `workspace.ResolveInside(root,"projects")`; `readOptionalDir` tolerates missing dir (:96-102); admits direct child dirs that are non-hidden and pass `IsSafeName`; sorts by name. Nested-looking dirs (e.g. `projects/nested/child`) are listed as projects (pinned by TestDiscoverProjectsFiltersAndSortsDirectSafeDirectories).
- `IsSafeName` :75-85: single segment of `[A-Za-z0-9_-]`, no leading `.`, rejects empty/`.`/`..`/separators.
- `ResolveProject` :49-73: trims ref, enforces IsSafeName, exact-name match first, else unique-prefix match; zero matches → `RefError` with sorted candidate names, ≥2 → ambiguous RefError listing matches. Same shape reused by sprint's `ResolveSprint` (internal/sprint/discovery.go:34-60).
- `FSStore.ReadProjectFiles` (store_fs.go:32-93): re-resolves `projects/<name>` and hard-errors on clean-path mismatch with `p.Path` (:38-40); lists top-level `docs/*.md` (non-hidden), reads full `roadmap.md` and `project-index.md` contents into memory (no size bound), lists non-hidden sprint dirs. All listings sorted.

### 2.3 Catalog parsing (`index.go`)
- Recognized sections :10-17: Source Documents, Active Contract Pool, Available Evidence Reports, Available Reasoning Templates, Review Protocols, Smoke Harnesses.
- `ParseProjectIndex` :19-58: line-based; `## <name>` switches section (unrecognized name → empty section, subsequent rows silently ignored until next recognized heading); rows must start with `|`; first row per table = headers; separator rows skipped; row→entry failure emits error finding with 1-based line number ("malformed catalog row").
- `entryFromRow` :60-96: header-keyed map (lowercased headers); name aliases {document, contract, report, template, protocol, decision, harness}; path aliases {path, output path}; description aliases {summary, covers, applies to, useful for, required when, why selected}; optional manifest/status columns; evidence column split on `" and "`; `trimInlineCode` :132-140 strips backticks and converts markdown link `[label](target)` to target; missing name or path (or literal `N/A`) → error; Smoke Harnesses rows additionally require a manifest column (:92-94).
- External detection :91,142-145: URL parse with scheme+host, or absolute path under Smoke Harnesses.

### 2.4 Validation (`validation.go`)
`ValidateProject(root, p, files)` :16-106 produces sorted findings; overall status `invalid` iff any SeverityError finding exists (:98-104):
- Required files: non-empty `docs/`, `roadmap.md`, `project-index.md`, `sprints/` (:28-43).
- Deprecated Project Scope field `- **Smoke Harness Directory:**` rejected by regex (:14,:44-47).
- Per-entry checks (:50-80): Smoke Harnesses entries validated by `validateSmokeHarnessEntry` :168-196 — path must be absolute, root and manifest `EvalSymlinks`-resolved, manifest contained in root (`filepath.Rel` prefix check), existing non-dir file. External entries skipped for existence. Reasoning-template entries rejected when normalized path is under `projects/` but outside `projects/<own>/` (:60-67). All other entries resolved via lexical `workspace.ResolveInside` then stat'ed for existence/readability.
- Reasoning overrides: each of the three governed paths, if present at `projects/<p>/<rel>`, must resolve via `ResolveReasoningDefault` (:82-95).
- Roadmap join `validateRoadmap` :108-166: all ParseRoadmap issues become error findings; slug↔directory reconciliation — roadmap sprint whose dir is absent: active → error ("active roadmap sprint directory missing"), delivered → warn ("roadmap sprint directory absent"), planned/dropped → silent; conversely every unclaimed `sprints/<dir>` → error ("sprint directory missing from roadmap").
- `sortFindings` :236-250 gives deterministic order (section, entry name, path, problem).
- `StatusFromValidation` :198-214 maps presence flags to present/missing, with `empty` special case for docs dir.

### 2.5 Roadmap parsing (`roadmap.go`)
`ParseRoadmap` :93-202 implements a closed-world grammar:
- Fenced blocks (``` or ~~~ toggles) ignored :113-116; blank-line rule closes the Goal paragraph capture :118-120.
- H2 → phase (phases without sprints dropped :189-196); H3 `Sprint N: Title` (regex :215, N≥1) requires a preceding phase else issue :148-151; H1 or unexpected heading levels close the current sprint; H4 subsections restricted to allowlist {goal, build, acceptance, release gate, exit gate, evidence, uncertainty, deferred, deliverables, commands, notes} :59-71; gate variants acceptance/release gate/exit gate satisfy the gate requirement :75-79.
- Metadata blockquote lines before the first H4: allowed keys {slug, status, depends on, uncertainty} :81-86; status restricted to {planned, active, delivered, dropped} :12-25; depends-on parsed as comma-separated positive ints :252-264; unknown keys/malformed lines produce issues :229-241.
- Post-checks `checkSprints` :291-330: duplicate numbers/slugs, monotonic ordering, required goal/build/gate subsections, dependencies must reference declared sprints.
- Content capture `collectSprintContent` :268-289: Goal paragraph text joined; gate checklist items with `[ ]/[x]/[X]` markers stripped into GateItems.

### 2.6 Roadmap delivery marking (`roadmap_status.go`) — sole write path
`MarkRoadmapSprintDelivered(path, slug)` :15-81: read whole file; re-parse ignoring issues (:21); require exactly one slug match (duplicate → error); zero parsed sprints → silent `(false,nil)` legacy tolerance (:33-37); already delivered → idempotent `(false,nil)`; locate the sprint block (up to next heading level ≤3), find the first `> Status:` line matching `^(\s*>\s*Status:\s*).*$` before any H4 (:11,:54-66); rewrite preserving the matched prefix, else insert `"> Status: delivered"` directly after the Slug line (error if Slug line absent) :67-76. Write is atomic: temp file in same dir `.roadmap.*.tmp`, chmod to original mode perm, fsync, rename :83-109. Only production caller: `sprint.Service.RunSmoke` after completed+pass+non-diagnostic smoke with passing review verdict, ordered after terminal flow-state save and before smoke publication; failures surface as `smokeError("roadmap_reconciliation",...)` (internal/sprint/smoke.go:41-49).

### 2.7 Reasoning defaults chain (`reasoning_defaults.go`)
Governed paths :12-22: `prompts/create-area-reasoning.md`, `prompts/create-sprint-reasoning.md`, `templates/sprint-reasoning.md`. `ResolveReasoningDefault(root, project, rel)` :52-86 precedence: `projects/<safe-name>/<rel>` ("project:") → `<rel>` at workspace root ("workspace:") → embedded `workspace.DefaultOverrideFile(rel)` ("builtin:"); error when none exist. Guards: IsSafeName(project), rel must exactly equal one of the three cleaned paths (:35-43), existing file must be non-dir, `.md` (case-insensitive), readable, non-empty whitespace (:88-114). Consumers: prompt renderers pre-flight resolve all three and fail rendering on error (internal/sprint/prompts.go:124,148,151); resolved content is injected verbatim into agent prompts (`renderPromptFromDefault`/`appendInjectedProjectReasoningFile` prompts.go:279-320); non-governed template rels fall back through `sprintPromptTemplate` (workspace override → builtin → explicit "# Missing Prompt Default" text embedded in the prompt) prompts.go:281-300. `normalizeCatalogPath` :45-50 strips leading `./` and `.ultra/` (prototype path convention surfaced in prompts as well, prompts.go:274).

### 2.8 Status assembly extras (`service.go`)
`Service.Status` :18-47 runs ValidateProject, resolves the three reasoning defaults (content blanked, source label kept; resolution failure recorded as `Source:"invalid"` row rather than aborting), and derives `AreaReasoningDocuments` from catalog entries whose normalized path lies strictly under `projects/<p>/reasoning/` (:37-45, sorted). Note these documents come from catalog text, not a filesystem walk.

### 2.9 Non-CLI doors into the same functions
- Operation vocabulary `validate` subject=project: `dashboardUseCases.Validate` calls `project.NewService(u.root).Validate(ref)` (operations.go:881-893), findings converted via `projectFinding` with `displaySafe` redaction (usecases.go:289-297).
- Dashboard/web/TUI summaries: `ProjectSummaries` iterates DiscoverProjects × Status with ctx checks and aborts on first per-project error (project_usecases.go:25-71); consumed by `Dashboard` (usecases.go:144) and web project pages.
- Web roadmap page: `webUseCases.projectRoadmap` re-reads `projects/<p>/roadmap.md`, parses with ParseRoadmap (issues discarded), joins live SprintSummaries by slug, then reverses phases and sprints for newest-first display (web_usecases.go:673-725); project dashboard brief scraped from index headings via `readDashboardMarkdown` (web_usecases.go:987-1000).

## 3. Inputs / outputs

Inputs: CLI argv; workspace filesystem — `projects/<p>/**` (docs/, roadmap.md, project-index.md, sprints/ dir names), workspace-root `prompts/`+`templates/` overrides, embedded builtin defaults, `ultraplan.yml` marker via discoverWorkspace; wall-clock-free pure parsing. Outputs: human-readable stdout/stderr reports and exit codes (0/2/4/5); DTO projections (ProjectStatus, ValidationResult, DisplayFinding, ProjectSummary, WebRoadmapPhase/Sprint); resolved prompt/template content strings embedded into agent prompts by the planning chain; one atomic rewrite of `projects/<p>/roadmap.md` issued by the smoke stage. No network, no subprocesses, no durable DB access.

## 4. Authoritative state

This surface owns no durable state. It reads user-authored `projects/<p>/**`; `roadmap.md` status edits via MarkRoadmapSprintDelivered modify a user-owned document in place (mode-preserving atomic rename; no directory fsync). `project-index.md` has no product writer anywhere in-tree (created by users/init flows outside this repo surface). The catalog content becomes de-facto governing input because downstream stages hard-fail on malformed rows and validate selections against it (see §9). Sprint directories are enumerated by name only.

## 5. Invariants encoded here

- Name admission at both discovery and resolution (IsSafeName) keeps refs to single safe segments.
- Containment: every cataloged relative path and reasoning default passes lexical `workspace.ResolveInside` (Abs+Clean, no EvalSymlinks — internal/workspace/discovery.go:69-78, paths.go:9-23); smoke-harness root/manifest add explicit EvalSymlinks containment.
- Determinism: sorted projects/docs/sprints/findings/area-docs; stable parse order.
- Closed-world roadmap grammar: unknown metadata keys, statuses, subsections, heading levels, duplicate/out-of-order numbers, dangling dependencies, missing goal/build/gate all emit issues.
- Findings are data: severity error ⇒ invalid overall; warn never flips status.
- Malformed catalog rows never panic: they become findings; downstream consumers may escalate them to hard errors.

## 6. Trust boundaries

Repo/user-authored markdown catalogs are parsed as governing input for agent stages (assignment framing). Concrete transitions:
- Catalog `Path` cells decide which filesystem files are stat'ed, validated, injected into prompts (reasoning templates via appendInjectedReasoningTemplate), and cited by review/handbook joins — admitted only through ResolveInside containment (lexical) or, for smoke harnesses, EvalSymlinks-backed absolute roots.
- Markdown link syntax in cells is rewritten to plain paths by trimInlineCode before containment checks.
- Reasoning default overrides (project/workspace tier) become literal prompt instruction content for agent stages; validity gate = readable non-empty .md only.
- Roadmap Goal/GateItems text flows into web DTOs (WebRoadmapSprint.Goal/GateItems, web_usecases.go:691-701); sanitization responsibility sits with the web projection layer, not here.
- RefError candidate lists echo directory names back to stdout/stderr.

## 7. External effects & retry/restart/error/cancellation semantics

Effects: exactly one — atomic replace of roadmap.md from the smoke success path (§2.6); temp residue removed via defer on failure. Retry: all operations are stateless re-reads; re-running list/status/validate is idempotent; MarkRoadmapSprintDelivered is idempotent by delivered-status check. Restart: nothing cached; crash during roadmap rewrite leaves either old or new file (temp+rename), possibly a stray `.roadmap.*.tmp`. Errors: classified exits (usage/workspace/validation); parse issues are reported per-line with suggestions rather than aborting; Service.Status/Validate abort on store read errors (e.g. unreadable roadmap) with ExitWorkspace. Cancellation: ctx honored only at ProjectSummaries/Dashboard loop boundaries; CLI paths carry no ctx. Concurrency: no locks taken by this package; the smoke caller holds the sprint mutation lease while marking delivery, but concurrent user edits to roadmap.md are serialized only by the atomic rename (last-writer-wins on whole-file granularity).

## 8. Files / symbols / tests

Primary files: doc.go (package charter: catalogs only, deliberately excludes study/runtime consumption); domain.go (Project, CatalogSection constants, CatalogEntry, StatusState, ProjectStatus, ValidationFinding/Result); discovery.go (DiscoverProjects/ResolveProject/IsSafeName/RefError); index.go (ParseProjectIndex/entryFromRow/recognizedSections); roadmap.go (Roadmap/RoadmapSprint/RoadmapPhase grammar, ParseRoadmap, checkSprints); roadmap_status.go (MarkRoadmapSprintDelivered/atomicWriteRoadmap); validation.go (ValidateProject/validateRoadmap/validateSmokeHarnessEntry/StatusFromValidation); reasoning_defaults.go (three governed paths, ResolveReasoningDefault); service.go (Service.List/Status/Validate); store_fs.go (FSStore.ReadProjectFiles); app/project_commands.go (runProject/renderers/help); app/project_usecases.go (ProjectSummaries).

Test evidence map (all green at frozen baseline):
- project_test.go: discovery filters/sorts incl. hidden/file/nested cases (:10-26); invalid/missing/ambiguous refs with exact messages (:28-39); valid fixture status+validate ok (:41-64); missing paths, escape rejection, malformed rows (:66-91); empty docs (:93-106); deprecated Smoke Harness Directory rejection (:108-124); cross-project reasoning template rejection (:126-143); reasoning-default sources + area documents (:145-174); six-section parse with external URL and absolute smoke harness contract (:176-230).
- roadmap_test.go: golden grammar accept (:70-101); MarkRoadmapSprintDelivered incl. mode preservation and idempotence (:103-132); fenced-content immunity (:134-145); missing slug/goal/gate issues (:147-164); gate variants + Notes (:166-199); duplicate number/slug/order (:201-261); unknown status/metadata/subsection (:263-294); unknown dependency (:296-321); sprint-outside-phase (:323-348); roadmap↔directory join incl. orphan-dir error (:350-384); status-aware absence severities (:386-466).
- reasoning_defaults_test.go: three-tier precedence incl. no cross-project inheritance (:10-46); empty override rejected (:48-62).
- app/project_commands_test.go: help registration (:9-28); list/status/validate happy paths with builtin reasoning source visible (:30-62); ExitValidation + stderr field format + ANSI-free output (:64-84); missing/ambiguous refs as validation exits (:86-102).

## 9. Immediate surface dependencies

Upstream: **workspace-scaffold-defaults** (ResolveInside containment helper shared verbatim; DefaultOverrideFile builtin tier; ultraplan.yml discovery consumed by app layer). Downstream consumers of this package's symbols:
- **sprint-planning-chain**: resolveSprintInputs/resolveSprintForRequirements hard-fail on malformed catalog rows ("project-index.md has malformed catalog rows") and pass ProjectIndex into every stage manifest (service.go:957-1050); prompts consume ResolveReasoningDefault and inject catalog-derived template content (prompts.go:110-330); requirements docs listing via FSStore.
- **sprint-conformance-review**: selection joins against Active Contract Pool/Evidence Reports/Reasoning Templates sections by EqualFold name (+exact path where required) (validation.go subset checks; review.go:1312-1325; handbook.go:124-131; reasoning.go:213-220).
- **sprint-smoke-gate**: exactly-one Smoke Harnesses entry requirement, target-directory admission, harness root/manifest resolution (smoke_protocol.go:107-135); MarkRoadmapSprintDelivered call site (smoke.go:41-49).
- **sprint-execute-resume**: re-parses catalog for plan manifests/deferral reasons (execute.go:432-436).
- **sprint-flow-state / study-runloop adjacent**: ReconcileInterruptedMutation enumerates all projects via DiscoverProjects (locks.go:129); runtime metrics likewise (runtime_metrics.go:90).
- **shared-usecase-vocabulary**: validate operation (project subject) and ProjectSummaries wiring (operations.go:881-893).
- **web-routing-projection**: project pages, roadmap preview join, dashboard brief scraping (web_usecases.go:650-725, 987-1000, handlers.go:118,342,383-392).
- **product-state-mirror**: storage migrate enumerates projects (storage_commands.go:108).
- **cli-dispatch-exit-contract**: exit classes 2/4/5 and stderr finding format consumed by scripts.

## 10. Contracts (CURRENT-CONTRACT evidence)

- docs/cli-reference.md:119-143: verb semantics; validate "also validates project reasoning overrides and rejects reasoning templates owned by a different project"; smoke harness manifest validation named.
- docs/user-guide.md:66-72: reasoning precedence project → workspace → built-in; area-specific reasoning docs belong under `projects/<p>/reasoning/` and "must be listed in that project's project-index.md". :269-279: projects layout; "Project validation checks that the project catalog resolves selected contracts, evidence reports, reasoning templates, review protocols, and source documents."
- ultraplan-workspace system/protocols/plan-sprint-protocol.md:104-105,139,154,160,364: inputs anchored at `projects/<project>/{project-index.md,roadmap.md}`; "Every selected contract, evidence report, protocol, and reasoning template must come from `project-index.md`"; completeness fails if selections reference items absent from the catalog.
- ultraplan-workspace system/protocols/deep-smoke-sprint-protocol.md:5,21: target, harness root, versioned manifest, runs/, issues/ resolved from project-index.md.
- In-tree comments: doc.go:3-10 (catalog-only role); roadmap.go:88-92 (grammar summary incl. fenced-block ignoring); roadmap_status.go:13-14 (preserve-all-other-content promise).

## 11. Explicit unknowns / open questions (for later reviewers)

1. Warn-only validate runs print `Validation: ok` yet exit 5 (project_commands.go:66-68 vs validation.go:98-104); whether scripts can distinguish warn-only from invalid without parsing stderr is unpinned in cli-reference.md.
2. Rows under unrecognized `##` headings are silently ignored with no finding (index.go:30-35); tolerance intent undocumented.
3. Header alias families are global per row (e.g. `decision` accepted as a name column in any section); no schema pinning per section beyond smoke-harness manifest requirement.
4. MarkRoadmapSprintDelivered discards parse issues (:21) and scans lines independently of the parser; behavior on roadmaps with issues (still rewritable) vs consumer expectations elsewhere (hard-fail on malformed rows) differs; concurrency story is last-writer-wins whole-file rename under the caller's sprint lock only.
5. Zero-sprint roadmaps get silent success from MarkRoadmapSprintDelivered (:33-37) — legacy tolerance breadth unpinned.
6. No size caps on roadmap.md/project-index.md reads or catalog entry counts; resource behavior on large inputs unverified.
7. Containment is lexical (workspace.normalize does Abs+Clean only); symlinked dirs inside the workspace pointing outside pass ResolveInside, while smoke-harness paths use EvalSymlinks — divergence also tracked as seam `containment-helper-divergence`.
8. Area-reasoning reverse direction uncataloged: files under `projects/<p>/reasoning/` absent from the catalog produce no finding and are invisible to Status; user-guide states they "must be listed" but enforcement direction is one-sided.
9. Web roadmap presentation reverses document order (newest-first) while CLI/status preserve order; canonical ordering across doors unstated.
10. `nested` directory inclusion in discovery (fixture-pinned) means partially scaffolded dirs list as projects; shape enforcement happens only at status/validate time.
11. Case-insensitive `.md` extension check vs case-sensitive path equality elsewhere (review/handbook joins use exact Path equality; EqualFold only on names) — cross-platform path-matching behavior on case-insensitive filesystems unverified.

— End of context pack. Descriptive only; no defect claims made or implied.
