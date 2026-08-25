# Context Pack: `workspace-scaffold-defaults` — Workspace bootstrap, defaults, skills

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: foundation. Risk: normal. Descriptive only — no defect judgments.

## 1. Purpose

Three CLI surfaces plus two shared primitives:

1. `ultraplan init-workspace` scaffolds a new workspace: marker/config file `ultraplan.yml`, `README.md`, and the `studies/` directory, creating only missing entries.
2. `ultraplan defaults install` materialises embedded prompt/template defaults into workspace-relative `prompts/` and `templates/` files as *editable optional overrides*, with byte-compare, skip/overwrite-confirm (`--force`) semantics.
3. `ultraplan skills materialise [all|stage]` renders 11 embedded "stage skill" definitions into `.agents/skills/<name>/SKILL.md` + `.agents/skills/<name>/agents/openai.yaml`, with the same preserve/confirm/--force semantics.
4. `workspace.Discover` resolves the workspace root: explicit path > `ULTRAPLAN_WORKSPACE` env > upward search from start dir for a `ultraplan.yml` marker (regular file), stopping at filesystem root.
5. `workspace.ResolveInside` is the containment primitive used across study/sprint/project code: it lexically normalizes a root-relative (or absolute) path and rejects anything escaping the root.

The override-resolution rule implemented on top of these primitives (consumed at prompt-render time everywhere): read the workspace file at the relative path; if absent, fall back to the embedded default (`workspace.DefaultOverrideFile`); otherwise error or emit placeholder text. Project-level overrides sit above workspace-level for reasoning defaults only.

## 2. Entrypoints and control flow

### CLI dispatch
- `cmd/ultraplan/main.go` builds `app.Config{Args, Stdout, Stderr, Context, Version, TUIRunner, WebRunner}`; it does not set `WorkDir`.
- `internal/app/app.go:88 Run`: parses global flags (`--workspace <path>`, app.go:198-220) into `deps.workspaceFlag`; if `WorkDir` empty, uses `os.Getwd()` (app.go:128-132). Dispatches `init-workspace` → `runInitWorkspace` (app.go:149-150), `defaults` → `runDefaults` (151-152), `skills` → `runSkills` (153-154).

### init-workspace (`internal/app/workspace_commands.go:10`)
- Flags: `--path <dir>` (default `deps.workDir`, then `"."`), `--dry-run`, `-h`. Unknown args → ExitUsage(2). The global `--workspace` flag is NOT consumed by this command (only its own `--path`).
- Dry-run: `workspace.PlanInit(path)`; otherwise `workspace.Init(path)` (`internal/workspace/init.go:243,270`).
- Plan: for each of `RequiredDirs()` = `["studies"]` and `RequiredFiles()` = `["ultraplan.yml", "README.md"]` (init.go:299-305), resolve via `ResolveInside`, stat, and emit `{Action:"create", Type:"dir"|"file"}` ops only when `os.IsNotExist`.
- Execute: `MkdirAll(dir, 0o755)`; for files, MkdirAll parent then `os.WriteFile(full, content, 0o644)` guarded by an IsNotExist re-check inside Init (init.go:282-294). Contents come from inline consts `defaultConfig` (init.go:137-175) and `defaultWorkspaceReadme` (177-241). Existing files are never rewritten. Idempotent: second run yields zero operations.
- Output: `Workspace: <root>` then one line per op (`created|would create <type> <path>`); `No changes needed.` when empty. Errors classified ExitWorkspace(4).

### defaults install (`internal/app/defaults_commands.go:25`)
- Path precedence: `--path` arg > `deps.workspaceFlag` (global `--workspace`) > `deps.workDir` > `"."`. Flags: `--dry-run`, `--force`, `--path`.
- Non-dry-run without `--force`: computes an initial plan (`PlanDefaults`), collects `skip` file ops (`skippedDefaultFiles`, line 115), and if any, prints the list and prompts `confirmOverwriteDefaults` (125-144) reading one line from stdin; answer `yes|y` sets `opts.Force = true`. Any other answer/read error keeps customizations. Then `workspace.InstallDefaults(path, opts)` recomputes and executes.
- `internal/workspace/defaults.go`: `PlanDefaults` plans `prompts/`, `templates/` dirs (create if IsNotExist; other stat errors are ignored, i.e., treated as existing) and all `DefaultOverrideFiles()` in sorted order. Per file: IsNotExist → create; read error (non-not-exist) → abort with error; byte-equal to embedded → no op; differs + Force → overwrite; differs w/o force → skip. `InstallDefaults` executes non-skip ops: dirs `0o755`, files `os.WriteFile(..., 0o644)` (truncate-in-place overwrite).
- Embedded inventory (`internal/workspace/init.go:107-135`): 14 `prompts/*.md` (base, synthesize, create-area-reasoning, create-requirements, create-code-context, create-sprint-index, create-sprint-reasoning, create-technical-handbook, execute-sprint, meta-plan, meta-synthesize, plan-sprint, review, smoke) and 13 `templates/*.md` (README, meta-report, project-index, repo-analysis, report, requirements, code-context, review, smoke, sprint-index, sprint-plan, sprint-reasoning, technical-handbook). Each is a `//go:embed` from `internal/workspace/scaffold/{prompts,templates}/`.

### skills materialise / materialize (`internal/app/skills_commands.go`)
- `runSkills` accepts subcommand `materialise` or alias `materialize` (line 17-19).
- `runSkillsMaterialise` (25): same path precedence as defaults install; positional selection default `"all"` (at most one; extra → usage error). Flags: `--dry-run`, `--force`, `--path`.
- Same confirm flow: initial `PlanSkills` → `skippedSkillFiles` → `confirmOverwriteSkills` (148) → maybe force → `MaterialiseSkills`.
- `internal/workspace/skills.go`: `StageSkills()` (38-229) returns 11 definitions: reconcile, requirements, code-context, sprint-index, technical-handbook, area-reasoning, reasoning, plan, execute, review, smoke. Fields include Stage, Name (`ultraplan-<stage>` except reconcile = `ultraplan-reconcile-review-smoke`), DisplayName, ShortDescription, Prerequisites, Prompt, PromptAvailable, StageWorkflow, and behavior flags SkipValidation (reconcile), ManualStateRepair (reconcile), StatusPromptOnly (execute), CanonicalFlow (code-context).
- `ResolveStageSkills(selection)` (231): trims/lowercases; `""|"all"` → all; else matches `skill.Stage == TrimPrefix(selection,"ultraplan-")` or `skill.Name == selection`; unknown → error listing stages.
- `PlanSkills` (248): dir set {`.agents`, `.agents/skills`, per-skill `<name>`, `<name>/agents`} created if missing; non-IsNotExist stat errors here ARE returned (unlike defaults). Files from `stageSkillFiles` (344): `SKILL.md` (renderStageSkill, 354) and `agents/openai.yaml` (renderStageSkillMetadata, 434). Same create/equal/differ(+force→overwrite/else skip) logic.
- Rendering: SKILL.md = YAML frontmatter (`name`, `description` restricting to explicit `$name` invocation) + 14-step operating contract assembled from per-flag variants of ownerRule/prerequisiteRule/validationStep/promptStep/stateRule/targetResolutionRule + stage workflow + canonical prompt. openai.yaml sets `display_name`, `short_description`, `default_prompt`, `policy.allow_implicit_invocation: false`.
- `MaterialiseSkills` (309): re-plans, re-resolves, writes ops except skips.

### Discovery and paths (`internal/workspace/discovery.go`, `paths.go`)
- `Discover(opts)` (discovery.go:21): ExplicitPath → `requireWorkspace`; else EnvWorkspace → `requireWorkspace`; else upward walk from StartDir (default `os.Getwd()`) checking `HasMarker` until filesystem root (`parent == start`). Not found → error suggesting `ultraplan init-workspace`. `HasMarker` = stat `ultraplan.yml` succeeds and is not a directory (64-67). `normalize` = filepath.Abs+Clean (69-78); no symlink evaluation anywhere.
- `ResolveInside(root, rel)` (paths.go:9): normalize root; if rel absolute use as-is else Join(root, rel); normalize; reject unless `isInside` (Rel(root,cand) == "." or not prefixed `..`). Lexical only — symlinks are not resolved. Absolute paths that land inside the root are accepted.
- `Rel(root, path)` (paths.go:28): slash-form relative path for display; falls back to Clean(path) when outside root or Rel errors.
- `Validate(root)` (`validation.go:14`): required files present-and-non-dir, required dirs present-and-dir → `ValidationResult{Valid, Issues}`.

### App-level discovery helper
- `discoverWorkspace(deps)` (app.go:287) maps DiscoverOptions{ExplicitPath: deps.workspaceFlag, EnvWorkspace: ULTRAPLAN_WORKSPACE, StartDir: deps.workDir} and wraps failures as ExitWorkspace. Consumed by: code, config, health, project, run (incl. run cancel/follow path at run_commands.go:251), serve, sprint, storage, study, tui commands. init-workspace/defaults/skills do NOT call it (they act directly on their resolved target dir, with no marker requirement).

## 3. Inputs / outputs

Inputs: argv flags; stdin (one confirmation line, only when conflicts exist and not --force/dry-run); env `ULTRAPLAN_WORKSPACE`; cwd; embedded assets compiled into the binary (`//go:embed` scaffold files, inline consts); existing filesystem state under the target dir.
Outputs: human-readable lines on stdout (`Workspace:`, `Selection:` for skills, op lines, confirmation transcript); exit codes 0 ok / 2 usage / 4 workspace-class failure; side-effect files/dirs (modes: dirs 0755, files 0644). No JSON output mode exists for these commands. No network, no git, no DB access on these paths.

## 4. Authoritative state

- Filesystem only. `ultraplan.yml` is both the discovery marker (any regular file) and the config document loaded by `internal/platform/config` (`config.Load` reads `<WorkspaceRoot>/ultraplan.yml`, config.go:160) — the scaffold writes a starter config with `version: 1`, runtime/models/execution/planning/smoke/logging/agentwrap keys.
- `prompts/**`, `templates/**`: user-editable overrides; presence alone changes effective prompt/template content.
- `.agents/skills/<name>/{SKILL.md,agents/openai.yaml}`: user-editable skill files; content determines what external agent tooling sees for `$ultraplan-*` invocations.
- No durable product-state DB involvement; nothing here registers with run control or journals operations.

## 5. Invariants (as implemented)

- Init/defaults/skills are idempotent: identical bytes ⇒ no operations; second full run reports "No changes needed."
- Dry-runs perform no writes (planning functions only stat/read).
- Customized files are never overwritten without an affirmative interactive confirmation or `--force`; read/confirmation failure preserves customizations (fail-safe direction: keep user data).
- All write targets pass through `ResolveInside` against the chosen root; escapes error out before any write.
- Defaults install never removes extra user files; skills materialise never deletes pre-existing unrelated directories under `.agents/skills`.
- Overwrite is whole-file truncation rewrite (`os.WriteFile`), not atomic temp+rename (contrast with fsync+rename used elsewhere in product state); permissions of overwritten files are reset to 0644.
- Marker check treats any regular `ultraplan.yml` as sufficient for discovery; structural completeness is a separate, opt-in `Validate`.
- Discovery precedence is strict: explicit > env > walk; env/explicit paths must themselves contain a marker (no walk from them).
- Embedded-default lookup keys on forward-slash relative paths (`filepath.ToSlash` normalization in `DefaultOverrideFile`).

## 6. Trust boundaries

- Embedded defaults (binary-controlled) vs workspace/project files (user-controlled): once a workspace `prompts/` or `templates/` file exists, its bytes become part of agent-facing prompt content rendered by sprint/study/review/smoke flows (see §8). Same for materialised skill files: they are plain markdown/yaml that downstream agent runtimes treat as instructions when explicitly invoked.
- Materialisation direction is binary→disk only; the CLI never reads `.agents/skills/**` back (no consumer in-repo).
- `ResolveInside` is the single containment gate for workspace-relative reads/writes across the codebase; it is lexical (no symlink following), so a symlink inside the workspace pointing outside passes containment while resolving outside at I/O time.
- Confirmation prompt input comes from stdin, which may be non-interactive (empty ⇒ keep customizations).

## 7. External effects & lifecycle semantics

- Effects: creates/overwrites files and directories under the user-chosen root; nothing else. No locks taken; concurrent invocations targeting the same root can interleave writes (last-writer-wins per file).
- Cancellation: no context propagation on these code paths; SIGINT mid-write can leave a partially written file (WriteFile truncate semantics). There is no transaction/rollback across the operation list — a mid-list failure returns an error leaving earlier ops applied.
- Retry/restart: rerunning is the recovery story; idempotency makes retry safe. A failed overwrite leaves the previous content replaced only if WriteFile already truncated; rerun rewrites fully.
- Error taxonomy: usage mistakes → ExitUsage(2) with message on stderr; planning/IO failures → ExitWorkspace(4); help exits 0 via stdout. Errors wrap causes (`%w`) preserving os error text including paths.

## 8. Immediate surface dependencies (who consumes this)

Downstream render-time consumers of the override rule (workspace file → builtin fallback):
- `internal/sprint/prompts.go:241 sprintPromptTemplate`: stage prompts `prompts/create-requirements.md`, `create-code-context.md`, `create-sprint-index.md`, `create-technical-handbook.md`, `create-area-reasoning.md`, `create-sprint-reasoning.md`, `plan-sprint.md` (via renderPromptFromDefault / projectReasoningPromptTemplate) plus injected templates `templates/requirements.md`, `templates/sprint-index.md`, `templates/sprint-plan.md` (appendInjectedWorkspaceFile). Read failure other than not-exist becomes "# Prompt Load Error" text embedded in the prompt; missing both sources → "# Missing Prompt Default". Note: embedded defaults `prompts/execute-sprint.md`, `prompts/meta-plan.md`, and `prompts/meta-synthesize.md` are materialisable but have no in-repo render-time consumer found (execute prompts are composed by `internal/sprint/execute.go:452 RenderExecutePrompt`).
- `internal/sprint/review.go:1763 loadReviewAsset`: `prompts/review.md` (must contain "Automated Sprint Review") and `templates/review.md` (must contain "Review Context", "Final Assessment"); empty/placeholder/missing-required-text fails the asset regardless of source.
- `internal/sprint/smoke_author.go:213`: `prompts/smoke.md` via sprintPromptTemplate.
- `internal/study/prompts.go:175 readWorkspaceFile`: `prompts/base.md`, `prompts/synthesize.md`, `templates/report.md`, `templates/repo-analysis.md`; non-not-exist read errors hard-fail.
- `internal/project/reasoning_defaults.go:52 ResolveReasoningDefault`: three reasoning defaults (`prompts/create-area-reasoning.md`, `prompts/create-sprint-reasoning.md`, `templates/sprint-reasoning.md`) resolve project-level `projects/<p>/<rel>` first, then workspace `<rel>`, then builtin; enforces .md extension, non-empty, non-directory.
- `internal/app/health_commands.go:52` reports discovery result as a health check.
- Containment consumers: study init/run/run_state/discovery/prompts; sprint service/store_fs/artifacts/discovery/reasoning/handbook/direct_inputs/qa_map/execute_target/code_context/review; project store_fs/validation/discovery; app usecases/web_usecases; display paths via `workspace.Rel`.

Upstream dependencies of this surface: none beyond Go stdlib (`embed`, `os`, `path/filepath`, `fmt`, `sort`, `strings`, `bufio`).

## 9. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace docs:
- TRD §5.1 (~L160-167): workspace resolution precedence explicit flag > `ULTRAPLAN_WORKSPACE` > cwd-with-marker > nearest parent; must stop at filesystem root.
- TRD §5.2 (~L171-178): required top-level structure README.md, ultraplan.yml, studies/.
- TRD §5.3 (~L196-200): normalize managed paths; commands must reject paths escaping the workspace; artifacts prefer workspace-relative paths.
- TRD §10.2 (L1111) and ARCHITECTURE.md (~L473-478): lookup prefers readable workspace override at matching path, else embedded default; fail early only when neither exists or override unreadable/invalid.
- ARCHITECTURE.md L480: "`init-workspace` must not export prompt or template files. `ultraplan defaults install` is the explicit operation for materializing editable copies." (Supersedes Sprint-02 reasoning that had init export base/synthesize prompts and repo-analysis/report templates.)
- PRD L233: prompts/templates exist only when intentionally customized, normally after `defaults install`. PRD L1018/L774 + system/contracts/surfaces/cli.md CLI-SAFE-001 (~L188-192): overwrites need confirmation or explicit flags; provide dry-run where feasible.
- Sprint 34 requirements L53-54, reasoning L168/L239: code-context skill manual-only, delegates to canonical flow command; dry-run non-mutating; normal materialization preserves customs; `--force` restores builtin; generated content synchronized with embedded definition; tests inspect SKILL.md + agents/openai.yaml.

In-repo docs: `docs/stage-skills.md` (skill table, materialisation layout `.agents/skills/<name>/{SKILL.md, agents/openai.yaml}`, `allow_implicit_invocation: false`, interaction contract, state ownership); repo README ~L145-180 (override precedence statement, confirm-before-overwrite and `--force` wording).

HISTORY (context only): Sprint 02 reasoning.md L147 describes the superseded init-exported-prompts behavior and names ultraplan.yml as the primary marker ("Workspaces with only `.ultraplan/` will not be discovered").

## 10. Tests (evidence map)

- `internal/workspace/workspace_test.go`: TestDiscoverPrecedenceAndParents (explicit beats env beats parent walk); TestResolveInsideRejectsEscape; TestInitAndValidate (dry-plan writes nothing; init creates required set; Validate passes; init does NOT materialize override files; InstallDefaults scaffolds full template/prompt bodies incl. `{{...}}` placeholders; init idempotent); TestEmbeddedPromptsDoNotRequireManualPromptOrTemplateReads (forbids `../../prompts/`-style read instructions in embedded prompts); TestCodeContextDefaultsEmbeddedAndMaterialized (byte equality embedded vs materialized); TestReviewDefaultsAreEmbeddedAndNotInitializedAsOverrides.
- `internal/workspace/skills_test.go`: TestMaterialiseAllStageSkills (11 skills; dry-run non-mutation; SKILL.md contains name/contract strings/embedded prompt; openai.yaml manual-only; second run zero ops); TestMaterialiseOneStageAndPreserveCustomization (custom preserved w/o force; force restores; single stage writes only its own dirs); TestResolveStageSkillsRejectsUnknownSelection; TestReviewSkillResolvesSprintPathAndDelegatesFanOut; TestReconciliationSkillCoversFindingTriageAndSmokeHarnessReadiness (incl. absence of "validate reconcile"); TestOnlyReviewAndCodeContextDelegateStageExecutionToCLI; TestExecuteSkillUsesCLIOnlyForStateAndPrompt (worktree-rule strings; forbidden CLI ops absent).
- `internal/app/app_test.go`: TestRunHelp/TestRunVersion/TestRunUnknownCommand/TestClassifiedErrorPreservesCauseAndCode; TestInitWorkspaceDryRunAndCreate (op lines; README content assertions; no override materialization); TestDefaultsInstallDryRunCreateSkipAndForce (dry-run, global `--workspace` variant, create, idempotent "No changes needed.", customized skip with confirmation listing, stdin "yes" overwrite, `--force` overwrite); helper initializedWorkspace.
- `internal/app/skills_commands_test.go`: TestSkillsMaterialiseDryRunOneAndAll (selection echo; dry-run no `.agents`; `materialize` alias; all 10 named stage dirs present); TestSkillsMaterialisePreservesCustomizedFilesUnlessConfirmed ("no" preserves; "yes" restores builtin).

Baseline: full `go test ./...` and `-race` green at frozen commit (review/baseline/go-test*.txt).

## 11. Explicit unknowns / open questions (for later reviewers)

1. External contract for `.agents/skills/**` layout: only `docs/stage-skills.md` documents it; the consuming agent-runtime convention (SKILL.md frontmatter fields, `agents/openai.yaml` schema, `allow_implicit_invocation` semantics) is defined outside this repository and was not verified here.
2. Symlink behavior: `ResolveInside`/`Discover` are purely lexical; whether symlink-following inside workspaces is an accepted risk or an unconsidered case is undocumented.
3. Concurrency expectations: nothing in docs states whether simultaneous `defaults install` / `skills materialise` / stage runs over one workspace are supported; no locking exists on these paths.
4. Whether `init-workspace` ignoring the global `--workspace` flag (accepting only local `--path`) is intentional; help text does not mention the global flag either way.
5. Whether non-IsNotExist stat errors being ignored during defaults dir-planning (defaults.go:31) versus returned during skills dir-planning (skills.go:277-279) reflects intent.
6. Windows support depth: mixed use of `filepath.FromSlash`/`ToSlash` suggests cross-platform intent, but no CI/platform evidence was inspected in this pack.
7. The reconcile skill references harness concepts (`ultraplan-smoke.json`, protocol-v1 manifest, `../ultraplan-go-smoke`) whose owning surface lives in sprint/smoke code outside this pack.
8. No documentation found in ultraplan-workspace establishing a general project-above-workspace override chain for prompts/templates beyond the three reasoning defaults (project/reasoning_defaults.go implements it only there).
