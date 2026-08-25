# Context Pack: `study-authoring` — Study scaffolding, validation, summary

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen).
Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: study-analysis. Risk: normal. Descriptive only — no defect judgments.

## 1. Purpose

This surface owns the runtime-free lifecycle of comparative research studies rooted at `studies/<study>/`:

1. **Study creation from YAML** (`study init`): parse/validate a user-authored `study-init.yml`, plan a deterministic artifact tree, optionally shallow-clone URL-backed sources with `git clone --depth 1`, and write normalized sidecars (`study-init.yml`, `study.json`, README, dimension markdown, per-source `.ultraplan-source.yml`) that establish source-dimension applicability.
2. **Discovery and normalization**: studies, sources (directory vs markdown), dimensions (filename-derived identity), optional `study.json` config (dimension priority + per-study model override), and the applicability filter used everywhere source×dimension pairs are created.
3. **Deterministic prompt rendering** (`study <s> prompt analysis|synthesis`): compose workspace-overridable templates plus dimension/source/report inputs into a byte-stable prompt and a JSON manifest; never touches the runtime.
4. **Artifact validation** (`study <s> validate [--json]`): structural checks plus lenient content/rating/citation shape checks over generated reports; aggregates into a pass/fail result with redacted output.
5. **Summary regeneration** (`study <s> summary`): rebuild `summary.csv` deterministically from existing report ratings.
6. **Run-history refresh** (`study <s> runs summary`): sync the terminal-task ledger `runs/tasks.jsonl` from run-state and render `runs/summary.md`.
7. **Publication adapter** (`publication.go`): study-side calls into `gitpublish` for completed executions, run-all summaries, and run-loop state.

The authoring commands themselves never initialize the agent runtime (pinned by TestStudyValidateDoesNotInitializeRuntime); execution surfaces consume this surface's discovery/prompt/validation helpers.

## 2. Entrypoints and control flow

### 2.1 CLI dispatch (`internal/app/study_commands.go:26` runStudy)
Grammar: `study init <yml> [--dry-run|--force|--no-clone|--output <dir>]`, `study list`, `study <s> list`, `study <s> summary`, `study <s> validate [--json]`, `study <s> status [--json]`, `study <s> runs summary`, `study <s> prompt analysis <dim> <src>|synthesis <dim> [--output <file>]`. Runtime verbs (`run`, `run-all`, `run-loop`, `synthesize`) dispatch to other surfaces. Exit classes (app.go:16-24): 0 ok, 2 usage, 3 config, 4 workspace, 5 validation, 8 partial.

### 2.2 Init (`internal/study/init.go:55` Init)
- `buildInitPlan` (:93): `parseInitYAML` (init_yaml.go:100 — reads any readable path, no containment on input; lenient `yaml.Unmarshal`; then `normalizeInit` :115) → `resolveStudyOutput` (:149): default `workspace.ResolveInside(root, "studies/<name>")`, or any contained dir via `--output`. Plan = dirs (studyDir, dimensions/, sources/, reports/, reports/source/, reports/final/), files (normalized `study-init.yml`, default `study.json` `{"version":1,"dimension_order":[]}` (config.go:83), README.md, one markdown per dimension `dimensions/<NN>-<slug>.md`, one sidecar per source `sources/<name>.ultraplan-source.yml`), clones for every source with non-empty URL → `sources/<name>` (`init.go:131-141`). `validatePlanPaths` (:156) double-checks every planned path is inside studyDir lexically (`inside` :217) AND inside the workspace (`ResolveInside`).
- Normalization grammar (init_yaml.go): name+description required; name must match `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` without ".." (:98,:224); `repos.count` must equal `len(items)` exactly — less ⇒ error, greater ⇒ "assisted completion is deferred" (:128-133); same rule for `dimensions.count` (:134-139); sources need name/description and url-or-path; `path` must be safe-relative (:228); duplicate source names rejected; dimension items need number/name/title/description/purpose/steps/citations/questions; numbers normalized ("1"→"01", "1.2"→"01.02", dimension_refs.go:32); slugs kebab-normalized (:58); duplicate numbers/slugs rejected; `applicable_dimensions` normalized via markdown.go:98 (int/int64/float64/string/yaml.Node → zero-padded "NN", deduped, sorted). Failures aggregate as structured `InitValidationError{Problems}`.
- Rendering (init_render.go): normalized YAML round-trip of the definition; README lists sources/dimensions/generated paths/next commands; dimension markdown (# NN Title, description, Purpose, Steps/Citations/Questions); source sidecar with name/url/path/description/applicable_dimensions. `yamlQuote` (:115) escapes backslash and double-quote only.
- Execution: dry-run returns plan without mutation (test-pinned). `ensureWritable` (:181): existing studyDir && !force ⇒ `ErrInitOverwrite`; force only refuses when a planned file path exists as a directory. Then MkdirAll 0755 + plain `os.WriteFile` 0644 (no temp/rename/fsync) in plan order. `--no-clone` records SkippedClones; otherwise `runCloneActions` (init_clone.go:72) runs clones sequentially, collecting failures and continuing; any failure ⇒ files remain + `ClonePartialError` (unwraps to `ErrInitPartial`) ⇒ exit 8.
- Clone runner (init_clone.go:44): `exec.Command("git","clone","--depth","1",url,dest)` with inherited env; `CombinedOutput`; failure text passes `redactGitOutput` (:54): credential-URL regex `\b(scheme://)user:pass@` → `[redacted]@`, empty ⇒ "no git output", cap 4096 chars + truncation marker. Failure record: code `provider.git.clone_failed`, category provider, Retryable false. No destination-existence check, no ctx/timeout.

### 2.3 Discovery (`discovery.go`)
- `DiscoverStudies` (:14): non-hidden directories under `studies/`, sorted by name.
- `DiscoverSources` (:39): entries under `sources/`: skip hidden, skip entry literally named "reports", skip `*.ultraplan-source.yml|.yaml` sidecars. Directory ⇒ `SourceKindDirectory`; metadata precedence: local `.ultraplan-source.yml` inside the dir wins over sibling `<name>.ultraplan-source.yml` (`mergeApplicableDimensions` :155 — first non-empty wins). `.md` file ⇒ `SourceKindMarkdown`; frontmatter parsed (markdown.go:15), frontmatter applicability wins over sidecar; whole file read errors abort discovery. Other files ignored. Sorted by name/kind/path.
- `DiscoverDimensions` (:162): `dimensions/*.md` matching `^(\d+(\.\d+)*)([-_ ]+(.+))?\.md$` (dimension_refs.go:11); hidden/dirs/non-matching silently skipped; Number zero-padded per dotted component; sorted Number then File.
- `LoadStudyConfig` (config.go:37): absent file ⇒ default (Present=false); else strict JSON decode (`DisallowUnknownFields`, single value), version must be 1, Model trimmed; `dimension_order` refs resolved against discovered dimensions via aliases {Number, Slug, File, Ref()} with duplicates rejected. Sentinel errors `ErrStudyConfigMalformed/Unsupported/Invalid`.
- Ref resolution (resolve.go): exact alias match, then unambiguous prefix match; ambiguity/missing ⇒ `RefError` listing candidates. Dimension ref normalization accepts unpadded/dotted numbers.

### 2.4 Prompt rendering (`prompts.go`)
- Report paths (reports.go): source report `reports/source/<dim-ref>/<source-name>.md` (trailing `.md` of source trimmed); final report `reports/final/<dim-ref>.md`; `Ref()` = Number or `Number-Slug`.
- Applicability gate: `SourceAppliesToDimension` (:163) — empty `ApplicableDimensions` ⇒ applies to all; else exact match on normalized dimension Number. Violation ⇒ `ErrPromptInapplicable`.
- `BuildAnalysisPrompt` (:16): reads `prompts/base.md` via `readWorkspaceFile` (:175 — ResolveInside, fallback to embedded builtin recorded as `builtin:<rel>` in manifest Templates); reads dimension markdown raw; sets local `DisableCodeCitations` if flag set OR content sniff matches case-insensitive substrings "disable code citations"/"code citations disabled"/"code citations are disabled" (:28,:201). Directory sources: + `templates/repo-analysis.md`, Metadata section, Source Isolation Rules ("Inspect only the selected source directory…"), Citation Rules (file:line requirement, or disabled wording). Markdown sources: + `templates/report.md`, Document-Only Rules, Embedded Document = `stripFrontmatter(body)` (markdown.go:40 — leading `---` block only; unterminated block errors during parse, strip is best-effort). Sections joined by `joinSections` (:208) as `## <header>\n\n<content>\n\n` — deterministic.
- `BuildSynthesisPrompt` (:41): `prompts/synthesize.md` + `templates/report.md` + dimension; sources argument or rediscovery; filtered to applicable, stable-sorted by Name then Kind; every applicable source report must exist (`os.Stat`) else error naming the missing relative path; Synthesis Inputs lists `- source [kind]: path`; Output names `FinalReportPath`.
- Manifest (`PromptManifest`, domain.go:60): kind/study/dimension/source/source_kind/templates/dimension_path/input_document_path/source_reports/input_report_paths/expected_output_path — all workspace-relative slash paths except ExpectedOutputPath which is absolute.
- CLI (`runStudyPrompt` app/study_commands.go:1290): renders `--- manifest ---\n<json>\n--- prompt ---\n<text>` to stdout, or writes the same bytes to `--output <file>` after `ResolveInside` containment (:1400-1421).

### 2.5 Validation
- Rating parsing (`rating.go:14` ParseRating): collect scores from `\b\d{1,2}\s*/\s*10\b` fractions and `\brating:\s*(\d{1,2})\b` labels; multiple distinct scores ⇒ ambiguous; score outside 0..10 ⇒ invalid; no matches but >1 occurrence of "rating" ⇒ ambiguous; otherwise invalid format. States: valid/missing/invalid/ambiguous.
- `findRating` (validation.go:166): if a heading exactly equals (fold-case) "rating"/"rating summary", only that section's lines are candidates until the next heading; candidate lines matching either rating pattern are collected (stop at first non-match once inside a section); else the whole document is scanned.
- Source report validation (`ValidateSourceReport` :13 → `validatePerSourceReport` :33): read failure ⇒ failed check `content.read` (observed classified not-exist/not-readable/other); empty ⇒ `content.non_empty` failed; substring alias checks (case-insensitive contains anywhere): top-level "# ", "source info|source information", "summary", "rating", "question|answer"; rating.parse per state; `citation.shape` required iff directory source && !DisableCodeCitations — regex `\b[a-zA-Z0-9_.\-/]+\.[A-Za-z0-9]+:\d+(?:-\d+)?\b` (:224) needs ≥1 match; otherwise recorded skipped. Any failed check ⇒ ValidationResult failed.
- Final report validation (`ValidateFinalReport` :23): non-empty; aliases: "study parameters|study context", "| source |, | sources |" (sources studied table), "executive summary", "rating summary", "pattern|synthesis", "open questions|notable absences".
- Study-level aggregation (`validation_command.go:20` ValidateStudyArtifacts): existence checks for sources/, dimensions/, reports/source/, reports/final/, summary.csv; `study.config` check (skipped when absent, passed when present); `source.discovery`/`dimension.discovery` require ≥1; per dimension × applicable source: full source-report validation; non-applicable pairs emit status `inapplicable` check `source_dimension.applicability`; per dimension final report validated; `run_state.parse` check (:165) classifies `LoadRunState` outcome (missing⇒skipped, malformed/unsupported/other⇒failed). Overall Status failed if any check or report failed; counts tallied per status; SchemaVersion pinned to 1.
- CLI (`runStudyValidate` app/study_commands.go:1012): text render prints only failed items with `config.RedactValue` applied to observed/guidance; `--json` emits envelope `{schema_version:1, command:"study.validate", workspace, status ok|fail, generated_at, result}` where all check paths are workspace-rel'd and expected/observed/guidance redacted (:1086-1110); failure ⇒ ExitValidation(5). Service entrypoint `Service.ValidateStudy` (validation_command.go:12) = ListStudy + ValidateStudyArtifacts.

### 2.6 Summary regeneration (`summary.go:29` WriteSummary)
CSV columns: `source`, each dimension Ref in natural discovery order, `total`. Per cell: inapplicable ⇒ literal `N/A` (no warning); else read source report → `findRating`: valid ⇒ score; missing report/missing/ambiguous/invalid rating ⇒ warning + empty cell. Total sums scored cells only. Rows sorted total desc, then source name asc. Output buffered through `encoding/csv`, then `atomicWriteFile` (:94): CreateTemp `.summary.csv.*.tmp` in study dir → Write → Sync → Close → Rename → best-effort `syncDir`. Failure leaves prior summary.csv byte-intact (test-pinned via chmod 0500). CLI prints `Summary: <rel>` to stdout and warnings to stderr; warnings do not fail the command. Note: `Service.WriteSummary` (service.go:148) uses natural dimension order; `study.json dimension_order` does not reorder columns.

### 2.7 Runs summary (`app/study_runs_commands.go:10`)
`study <s> runs summary`: ListStudy → `LoadRunState` (errors mapped by `mapStudyRunLoopError` app/study_commands.go:354: malformed/unsupported⇒ExitValidation(5); missing/others fall through to ExitWorkspace(4)) → `SyncRunHistory` (run_history.go:150): for each terminal task (CompletedAt set) build record keyed `RunID|TaskID|Attempts|CompletedAt(RFC3339Nano)` (:306); skip keys already present; ledger fully rewritten atomically (temp + Sync + chmod 0644 + rename, :128) after trimming an invalid trailing JSONL line (:116 — only the last record may be partial; earlier malformed lines hard-fail); then `WriteRunHistorySummary` (run_history_summary.go:12) renders markdown tables (status counts, remaining tasks from state, dimension/source/runtime aggregations, recent 20, slowest 10, failed/cancelled) and writes with PLAIN `os.WriteFile` 0644.

### 2.8 Publication (`publication.go`)
- `publishExecution` (:10): gates on `s.publisher != nil` AND `result.Status == ExecutionStatusCompleted`; publishes `[OutputPath, extraPaths...]` under Root=study.Path with Message `ultraplan: study <s> complete <kind> <dim>[/<src>]` and Identity `study/<s>/<kind>/<subject>`; appends Result unless skipped-with-empty-repo; returns `(result, err)` — publication failure rides alongside a completed result (partial-success semantics). Call sites: run.go:83,103 (analysis success unless DeferPublication), synthesize.go:80,100, run_loop.go:255 (per completed task).
- `publishRunAllSummary` (:30): publisher && RunAllStatusCompleted; publishes SummaryPath.
- `publishRunLoopState` (:45): publisher presence only (no status gate); publishes RunStatePath + RunHistoryPath + RunHistorySummaryPath at end of RunLoop after state save + SyncRunHistory (run_loop.go:466).
- Wiring: `stagePublisher(cfg)` (app/git_publication.go:10) returns nil when `cfg.Git.StageCompletion == off` (gitpublish defaults ModeOff), else CommandPublisher{remote, push timeout}.

## 3. Inputs / outputs

Inputs: user-supplied `study-init.yml` bytes (any readable path); `studies/<s>/` tree (cloned repos, markdown sources, dimension markdown, generated reports, study.json, summary.csv, `.ultraplan/run-state.json` possibly DB-mirrored, `.ultraplan/runs/tasks.jsonl`); workspace override prompts/templates (`prompts/base.md`, `prompts/synthesize.md`, `templates/repo-analysis.md`, `templates/report.md`) with embedded builtin fallbacks; wall clock; git subprocess combined output.
Outputs: created study tree + git clones; prompt preview stdout or `--output` file (contained); validation results (stdout text or stable JSON envelope; no durable writes); replaced `summary.csv` (atomic); rewritten `.ultraplan/runs/tasks.jsonl` (atomic) + `runs/summary.md` (plain write); git commits/pushes via gitpublish when enabled; classified errors/exit codes; sentinel errors (`ErrInitValidation/Overwrite/Partial`, `RefError`, `ErrPromptInapplicable`, `ErrStudyConfig*`, `ErrRunState*` re-exported through checks).

## 4. Authoritative state

- `studies/<s>/study-init.yml` — normalized copy of the definition (informational; later commands re-discover from the tree rather than re-reading it).
- `studies/<s>/study.json` — optional authority for dimension priority (`dimension_order`) and per-study model override (`model`); strict schema v1. Consumed by execution surfaces via `listing.Config`.
- `studies/<s>/sources/**` — user/cloned content; applicability sidecars/frontmatter establish the source-dimension matrix.
- `studies/<s>/dimensions/*.md` — filename-derived identity (Number/Slug); file content is prompt input and citation-disable control text.
- `reports/source/<ref>/<source>.md`, `reports/final/<ref>.md` — artifacts written by execution surfaces; consumed here by validate/summary/synthesis-prompt.
- `summary.csv` — regenerated artifact.
- `.ultrapran/run-state.json` read-only here: validate's parse check and runs-summary input. NOTE: `LoadRunState` (state.go:27) is DB-first (`loadRunStateDatabase`) when a product-state row exists, so these two commands can touch `.ultraplan/run-control.db` (product-state-mirror seam).
- `.ultraplan/runs/{tasks.jsonl,summary.md}` — append-only ledger with key dedupe; derived markdown summary.
- No other durable state; no locks owned by this surface.

## 5. Invariants (as implemented)

- Containment: all outputs resolve inside the workspace (`ResolveInside`); generated files/clones additionally inside studyDir (lexical `inside` + ResolveInside double-check); prompt `--output` contained; escape attempts ⇒ ExitValidation.
- Naming safety: study/source names match filesystem-safe regex without ".."; source paths safe-relative; dimension filenames must parse as numbered markdown; numbers zero-padded; slugs kebab.
- Count coherence: repos.count/dimensions.count must equal explicit item counts exactly; mismatches rejected in both directions.
- Applicability totality: empty filter ⇒ all dimensions; filters compare normalized Numbers only; inapplicable pairs are skipped-not-failed across prompt/run/run-state/summary/validate (TRD §9A.3).
- Determinism: identical inputs produce byte-identical prompts and manifests (pinned); summary CSV ordering deterministic; validation JSON paths relativized.
- Closed rating grammar: four states; ambiguous ratings never become values (empty cell + warning).
- Write discipline: summary.csv replaced only via temp+fsync+rename; ledger rewritten atomically with trailing-torn-line tolerance; init writes are plain (non-atomic) and ordered dirs→files→clones.
- Publication gating: execution/run-all publications require Completed status; run-loop state publication requires only a configured publisher.
- Redaction-before-display: clone failure output credential-redacted and bounded ≤ ~4096 chars; validation observed/guidance passed through `config.RedactValue` in both text and JSON renderers (secret-leak test-pinned).

## 6. Trust boundaries

- `study-init.yml` is untrusted data that becomes: filenames (sanitized by regex+".." ban), git clone URLs (passed as argv to a child process; no scheme/host allowlist), and embedded text (README/dimension/sidecar content quoted via yamlQuote escaping backslash+quote only).
- Cloned external repositories enter as opaque data; they become directory sources whose contents feed analysis prompts in the task-execution surface (isolation rules instruct agents to stay inside the source dir).
- Clone child stderr/stdout re-enters as error text; redaction applied before display (unit + end-to-end fake-git tests).
- LLM-authored report bodies are data judged by lenient substring/regex validators; validator Observed/Guidance fields can echo report-derived strings and are redacted at render time.
- Dimension markdown content doubles as control input: three hardcoded substrings disable citation requirements at prompt-render time (content-as-control; see §11.1).
- Markdown source documents are embedded wholesale into prompts after frontmatter stripping (frontmatter values other than applicable_dimensions are parsed but unused beyond Frontmatter map).
- `study.json` is trusted-but-strict config (DisallowUnknownFields, single JSON value, version pinned) controlling priority/model.
- Run-history ledger lines are JSON decoded tolerantly at tail (last-line-only partial tolerance) and strictly elsewhere.

## 7. External effects & lifecycle semantics

- Effects confined to the workspace tree, child git processes (network access with ambient credentials via inherited env), and gitpublish commits/pushes when configured (default off).
- Crash semantics: init has no atomicity — a mid-init interruption leaves a partially created tree; a retry without `--force` refuses with `ErrInitOverwrite` (exit 5) until the tree is removed manually or `--force` regenerates known files while preserving unknown ones (test-pinned `notes.txt`). Clones always execute on non-dry-run init; there is no destination-existence skip.
- Cancellation: none of the authoring commands take a context except publication (`ctx` forwarded to Publish); clones have no timeout and no cancellation lane; SIGINT handling is the process-default.
- Retry: all commands are re-runnable; init guarded by overwrite logic; clone retries against an already-populated destination fail (git refuses non-empty target) and surface as partial completion; summary/validate/runs-summary reruns are idempotent (ledger dedupe by key).
- Error surfacing: validation failures exit 5 with actionable guidance strings; clone partial failures exit 8 while still printing created artifacts; missing/malformed inputs mostly exit 4 (workspace) except config/ref/prompt-inapplicable (5) and usage (2).
- Durability tiers within sibling artifacts: summary.csv and runs/tasks.jsonl atomic (temp+fsync+rename; syncDir best-effort for summary); runs/summary.md plain write; init files plain writes.

## 8. Immediate surface dependencies

- `workspace-scaffold-defaults` (internal/workspace): `ResolveInside`/`Rel` containment helpers; embedded scaffold prompts/templates via `DefaultOverrideFile`; `studies/` required dir from scaffold.
- `repo-publication` (internal/platform/gitpublish): Publisher interface; study/publication.go is one of that surface's listed adapters; policy from config (`Git.StageCompletion`, remote, push timeout).
- `product-state-mirror` (indirect): LoadRunState DB-first branch can consult `.ultrapran/run-control.db` from validate/runs-summary/status paths.
- Consumers of this surface's exports:
  - `study-task-execution`: BuildAnalysisPrompt/BuildSynthesisPrompt, ValidateSourceReport/ValidateFinalReport (post-run gate + synthesis preflight), ReportRating, GetApplicableSources, `listing.Config.Model` override resolution (run.go:51, synthesize.go:48).
  - `study-runloop-scheduler`: NewRunState task graph built from the same discovery/applicability (run_state.go:102-174), publishRunLoopState, SyncRunHistory, LockInfoForStatus for status display.
  - web/TUI: StudySummaries findings via ValidateStudyArtifacts (study_usecases.go:134), StudyDimensions/StudyReports listings via ListStudy + SourceReportPath/FinalReportPath (web_dimensions.go), Validations use case.
  - `code-extraction`: parses reports written under this surface's path conventions.
- Upstream inputs consumed but not owned: run-state schema/state_database (run-loop surface), report contents (task-execution), config.RedactValue (config-inspection-health).

## 9. Contracts (CURRENT-CONTRACT evidence)

From ultraplan-workspace/projects/ultraplan-go/docs/TRD.md:
- §8.1 Study: Name filesystem-safe; root under `studies/` unless custom output; deterministic sorting for listing/task creation.
- §8.2 Source: unique names; markdown sources directly under `sources/`; ApplicableDimensions normalized two-digit; empty ⇒ all dimensions; prefix lookup only when unambiguous; kinds distinguished.
- §8.3 Dimension: zero-padded number, kebab slug, lookup by number/slug/filename/unambiguous prefix.
- §9.1 Input schema: name, description, counts, items, optional clone behavior, optional output dir.
- §9.2 Assisted completion: count greater than items MAY request runtime suggestions cached under `.ultraplan/cache/study-init/<study>/`, disabled by `--no-assist`, dry-run reports shortage without runtime. Implementation currently rejects both mismatch directions with "assisted completion is deferred"; no suggestion flow, cache, or `--no-assist` flag exists (PRD §6 phrases it as capability "can ask").
- §9.3 Generated files: study-init.yml, README.md, dimensions/NN-slug.md, sources/, reports/source|final. Implementation additionally emits study.json and per-source sidecars (superset).
- §9.4 Cloning: `git clone --depth 1` into `sources/<name>`; "Skip existing source directories unless `--force`"; record failures without hiding successes; partial-completion status. Implementation has no existing-destination skip; it always clones.
- §9.5 Force: overwrite generated structure, never delete unrelated paths outside the study dir (implementation preserves unknown files, deletes nothing).
- §9A.1-.3: discovery ignores hidden/non-md/nested md; frontmatter helpers parse leading block only, invalid values reported with path+offending value (test-pinned); filtering used everywhere pairs are created/validated/summarized/synthesized; inapplicable pairs skipped not failed. (TRD prose says "Directory sources are always applicable"; implementation honors directory-sidecar applicability filters — implemented and tested behavior.)
- §10.1: prompt inputs enumerated per kind — matches implementation section-for-section including document-only rules and citation wording.
- §15.3: report validator check lists match implementation; "Must enforce code citation shape unless the dimension explicitly disables that requirement."
- §17 Summary generation: deterministic order, rating formats `**8 / 10**`/`8/10`/`Rating: 8`, total column, sort by total desc, missing ratings as empty cells, inapplicable pairs distinct from missing reports and excluded from warnings, ambiguous ratings warn without inventing values.
From docs/cli-reference.md (in-repo): command grammars; prompt preview "does not invoke runtime execution"; validate `--json` stable envelope with redacted checks; init partial-completion exit class; summary regenerates deterministically without runtime.
FUTURE-INTENT (not current defect): PRD §6 assisted-completion suggestion pipeline remains unimplemented by explicit code message.

## 10. Tests (evidence map)

Package-internal (internal/study):
- init_test.go: dry-run plans without mutating FS (:10); artifacts created + skipped clones (:34); applicable_dimensions preserved through normalized YAML and sidecar (:55); clone runner args + ErrInitPartial (:70); six actionable validation cases incl. unsafe path and bad applicability (:94); structured multi-problem InitValidationError (:121); redactGitOutput strips credentials and bounds length (:143); output-path safety, overwrite refusal, --force preserving unknown notes.txt (:159).
- config_test.go: dimension_order mixed-ref resolution (:10); missing config natural behavior (:31); malformed/unknown-field/unsupported/unknown-dim/duplicate rejections (:46); model override parse (:76).
- study_test.go: dimension filename identity + dotted hierarchy + rejection of non-numeric (:11,:31); studies/sources/dimensions discovery sorting, hidden/file filtering, shallow scan, reports-dir exclusion, sidecar/local-sidecar precedence, frontmatter dedupe+sort, invalid applicability error carrying path+value (:51-:193); frontmatter parse/strip incl. unterminated block (:146); GetApplicableSources filtering/order (:195); resolve exact/prefix/ambiguous/missing for all three ref kinds (:265-:336).
- prompts_test.go: byte-equal determinism + manifest JSON equality (:12); disabled-citation wording (:41); inapplicable gate (:53); markdown frontmatter strip + document-only rules (:63); missing document (:84); synthesis filter/order and missing-report error isolating the right path (:100); builtin fallbacks for all four template files (:134).
- validation_test.go: passing source report incl. .py/.ts citation shapes (:10,:45); missing/empty/ambiguous/separate-rating-lines failures (:88); markdown skips citation.shape (:161); DisableCodeCitations dimension skips citation check (:184); rating-section precedence over body mentions (:219); final report pass/failure sets (:251,:280).
- rating_test.go: fraction bold/plain, label, label-fraction, missing, invalid, ambiguous (:5).
- summary_test.go: exact CSV bytes with N/A cells (:9); missing/ambiguous rating warnings with empty cells (:45,:70); failed write preserves prior summary via chmod 0500 (:96).
- validation_command_test.go: pass + inapplicable counting (:11); unsupported run-state fails overall (:37); report-body secret never appears in result (:63); malformed frontmatter propagates from service (:80); valid run-state passes (:115).
- publication_test.go: request Paths/Identity shape (:19); failed stage never published (:42).
App-level: study_init_commands_test.go (help/usage; dry-run no mutation; --no-clone/--output; overwrite hint; escape rejection; fake-git partial failure exit 8 with provider.git.clone_failed and e2e credential redaction), study_summary_commands_test.go (exact CSV + stderr warnings; help), study_validate_commands_test.go (text+JSON envelope fields; failure exit 5; JSON redaction of sk-test; runtime factory untouched), study_prompt_commands_test.go (preview format; --output file; missing/ambiguous/inapplicable errors; builtin fallback marker; broken symlink source → exit 4; help boundary text), study_runs_commands_test.go (help; rel-path output without absolute leakage).
Baseline: full `go test ./...` green at frozen commit; internal/study coverage 75.0% of statements (review/baseline/go-test-cover.txt).
Coverage gaps noted factually: GitCloneRunner exercised only via PATH-stubbed fake git; no test for mid-init IO failure leaving partial trees; no test pinning summary column order interaction with study.json dimension_order; no test pinning the exit class of `runs summary` with missing run state.

## 11. Explicit unknowns / open questions (for later reviewers)

1. `Dimension.DisableCodeCitations` is never populated by discovery (`dimensionFromFile` sets four fields only); it is computed transiently inside `BuildAnalysisPrompt` by substring-sniffing the dimension body, mutated on a local copy, and never persisted. Production callers of `ValidateSourceReport` (run.go, synthesize.go, validation_command.go) pass discovery-derived dimensions whose flag is false, so the citation-shape requirement does not observe the sniffed setting; the skip branch is reachable in-package only via hand-built Dimensions (tests, web projection field `CodeCitationsDisabled` likewise always false from listings).
2. TRD §9.4 specifies skipping existing source directories unless `--force`; implementation always runs clones. Re-init (with or without `--force`) over an already-cloned source fails inside git ("destination exists") and yields exit-8 partial completion after files were rewritten. Intended posture undocumented in-repo.
3. Assisted completion (TRD §9.2/PRD §6): count>items is currently a hard validation error naming deferred assistance; no cache dir, suggestion validation, or `--no-assist` exist. Whether the contract is "may" (optional capability) vs "must eventually" is unstated.
4. Init durability: plain `os.WriteFile` without temp/rename/fsync; crash windows leave partial trees that subsequent non-force runs refuse to touch (ErrInitOverwrite). No recovery tooling exists for half-initialized studies.
5. Input YAML path is unrestricted (any readable path, including outside the workspace); only outputs are contained. Clone URLs are arbitrary argv strings to `git` with no scheme validation; credentials rely on git's own config/redaction plus the failure-output regex.
6. `yamlQuote` escapes only backslash and double-quote; other YAML-special sequences are wrapped in double quotes — adequacy for exotic control characters in user descriptions is unverified (round-trip through yaml.Unmarshal is exercised only with benign fixtures).
7. Validator leniency: `containsSection` substring aliases accept matches anywhere in the document (e.g. the word "rating" in prose satisfies section.rating); citation.shape needs a single token match. These are deliberate shape heuristics per TRD §15.3, but their false-negative/positive tradeoffs are undocumented.
8. Rating extraction stops at the first non-matching line once inside a rating section; a rating formatted after a blank+prose line inside the section would be missed while the whole-document fallback is suppressed (section found ⇒ candidates limited to section). Interaction cases (multiple rating sections) follow first-section-wins.
9. `runs summary` maps a missing run state to ExitWorkspace(4) via mapStudyRunLoopError's default branch (malformed/unsupported get 5); whether missing deserves its own class is undocumented. `runs/summary.md` uses a plain write while tasks.jsonl is atomic — consumers must not assume equal crash guarantees.
10. `LoadRunState`'s DB-first branch means `validate` and `runs summary` can read (and classify) DB-resident state invisible in the JSON file; interplay with the productstate-study-mirror seam (row-wins, stale-file scenarios documented there) is inherited by these commands.
11. `publishRunLoopState` publishes run-state/history/summary whenever a publisher is configured, regardless of run outcome (failed/cancelled loops still commit state artifacts); execution/run-all publications are Completed-gated. Consistency intent undocumented.
12. Summary columns follow natural dimension order while execution honors study.json priority tiers; cross-artifact ordering expectations for downstream consumers are unstated.

— End of context pack. Descriptive only; no defect claims made or implied.
