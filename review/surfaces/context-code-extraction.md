# Context Pack: `code-extraction` — Code citation extraction

Target: ultraplan-go @ 8eef70f4903b25580719960009a170945bdad9ad (frozen; HEAD 50d6f0d has zero
diff on this surface's paths). Planning context: ultraplan-workspace @ ab12dc38059c9bf485f9aced9075bcd7d924cac5.
Domain: foundation. Risk: low. Descriptive only — no defect judgments.

## 1. Purpose

Standalone post-hoc audit of cited code snippets inside generated Markdown reports (study
reports, sprint artifacts, any file passed by the operator). Given one or more report paths,
the command:

1. Parses inline backtick citations `` `path:line` ``, `` `path:start-end` ``, `` `path:a,b,c` ``
   (plus the legacy en-dash range form) and a Markdown "sources" table mapping source names to
   local root directories.
2. Resolves every citation against those roots with escape rejection and a bounded basename
   fallback, then extracts 1-based line snippets from the real files.
3. Emits deterministic human text or sprint-local JSON, aggregating per-reference resolution
   status into an overall `ok` / `partial` / `validation` status that maps to distinct exit codes.

No runtime, provider, network, database, or git involvement anywhere on this surface.

## 2. Entrypoints and control flow

### 2.1 CLI dispatch (`internal/app/app.go:173-174`)
Top-level `code` → `runCode(deps, args[1:])` via `failOrOK`. Exit classes (app.go:15-25):
0 OK, 1 error, 2 usage, 4 workspace/filesystem, 5 validation, 8 partial.

### 2.2 `runCode` (internal/app/code_commands.go:19-78)
- No args ⇒ ExitUsage pointing at help (:20-22). Exactly one arg `--help`/`-h` ⇒ help text to
  stdout, exit 0 (:23-26). Any other flag-shaped arg reaches `parseCodeArgs` and is rejected as
  unknown (:98-99) — e.g. `code foo --help` is ExitUsage.
- `parseCodeArgs` (:80-108): loop parser; `--json`; `--output <path>` where next arg must exist
  and not start with `-` (:87-92); `--output=<path>` accepted even if value starts with `-`
  (:93-97); last occurrence wins; ≥1 positional report required (:104-106). Flags may appear in
  any position relative to positionals.
- Workspace discovery `discoverWorkspace(deps)` (app.go:287-298): explicit `--workspace` >
  `ULTRAPLAN_WORKSPACE` env > walk-up from workDir to filesystem root looking for marker file
  `ultraplan.yml` (workspace/discovery.go:21-51). Failure ⇒ classified ExitWorkspace.
- Report paths: joined onto `deps.workDir` when relative (:36-42); absolute paths used verbatim.
  Reports are NOT required to live inside the workspace.
- `codeextract.Extract(Request{WorkspaceRoot: root.Path, Reports})` (:43-46): hard error ⇒
  classified ExitWorkspace `code.extract:` — aborts everything (no partial output).
- Render into a bytes.Buffer: `RenderJSON` if `--json` else `RenderText(&buf, root.Path, …)`
  (:47-55); render error ⇒ ExitError.
- Output sink (:56-69): `--output` — path joined onto workDir when relative, `MkdirAll(dir,0755)`
  then truncating `WriteFile(0644)`; failure ⇒ ExitWorkspace `code.output:` (test asserts empty
  stdout in this case). Otherwise raw write of buffer to stdout (unclassified error would fall to
  ExitError).
- Status mapping AFTER output is written (:70-77): `StatusValidation` ⇒ classed ExitValidation(5)
  "code.extract: validation failed"; `StatusPartial` ⇒ ExitPartial(8) "code.extract: unresolved
  references"; else nil. So stdout/file always carry full extraction results even for non-zero
  exits; stderr carries the single classified summary line via `fail` (app.go:229-241).

## 3. Parsing (`internal/codeextract/parser.go`)

- Citation regex (:11): backtick-delimited, body = non-backtick non-newline run matching
  `<anything-without-colon> : <digit>([0-9,\-– ])*`. Scans the ENTIRE report content — citations
  inside fenced code blocks or prose are matched identically. Dedupe by exact citation string,
  first occurrence wins, BEFORE validity checks (:32-37), so a repeated malformed citation yields
  one diagnostic. `strings.Cut` on first ":"; empty path or spec ⇒ "malformed citation" (:38-42).
- `parseLineSpec` (:59-95): TrimSpace + en-dash→hyphen normalization. Range form must split into
  exactly two hyphen parts ("1-2-3" malformed); both ends positive ints; end<start rejected;
  expanded eagerly to an explicit `[]int` start..end inclusive at parse time (before any
  file-size knowledge). List form splits on ",", preserves given order in both lines and the
  normalized spec string. Non-positive/non-numeric ⇒ "line numbers must be positive integers".
  Spaces around numbers are tolerated by TrimSpace (e.g. `` `f.go:1 - 3` `` parses as 1-3).
- References sorted stably by citation byte-string ascending (:55); normalized range spec is
  always `%d-%d` hyphen form (:81) — the legacy en-dash never appears in output.
- Source table `parseSources` (:105-142): scans every line; trimmed line must start AND end with
  "|"; `splitMarkdownRow` (:144-151) trims outer pipes then splits on EVERY "|" (no escaped-pipe
  handling); ≥3 cells required; cells[0] must Atoi as index — header (`#`) and separator rows
  fail silently and are skipped, not diagnosed (:118-121). name=cells[1], path=cells[2] with
  backticks trimmed; empty name/path ⇒ "malformed source row"; duplicate name ⇒ "duplicate source
  row", later row skipped. Sorted stably by Index then Name (:135-140).

## 4. Resolution (`internal/codeextract/resolver.go`)

- Ignored fallback dirs (:11-17): `.git`, `.hg`, `.svn`, `node_modules`, `.ultraplan`.
- `newResolver` (:28-47): per source row, `resolveSourceRoot` (:49-62) tries workspace-relative
  candidate first, then report-dir-relative; each candidate must os.Stat as an existing directory
  AND pass `containedPath(workspaceRoot, candidate)` — containment is checked against the
  workspace root for BOTH candidates, including the report-dir-relative one. Failure ⇒ per-source
  diagnostic "source root not found or outside workspace"; that source is excluded from
  resolution.
- `containedPath` (:113-134): Abs both ends; EvalSymlinks each (falling back to the raw Abs path
  when EvalSymlinks errors, e.g. nonexistent leaf); containment decided on the EVALUATED paths
  (symlink-aware); returns the UNEVALUATED absolute candidate. Subsequent Stat/read re-follow
  symlinks on that returned path.
- `inside` (:136-142): Rel-based prefix check (`.` allowed, no `..` prefix).
- `resolve(citedPath)` (:64-99), in order:
  1. NUL-byte rejection (:65-67); absolute-path rejection (:68-70);
     Clean(FromSlash) then reject "." and any ".."-prefixed result (:71-74) — lexical escape gate.
  2. For each resolved source in sorted order: `existingContainedFile(root, cleaned)` (:101-111) =
     Join + containedPath against that source root + Stat non-dir; then if the first path segment
     equals the source name, strip it and retry (:79-85).
  3. Basename fallback `basenameMatches` (:144-169): WalkDir each source root, SkipDir on ignored
     names, collect regular files whose entry name equals Base(cleaned) and are inside the root;
     results sorted lexicographically; cached per resolver instance (one extraction invocation —
     comment at :23-25 states cache is intentionally invocation-scoped). Exactly one match ⇒
     accepted (must sit inside some source root, which the bounded walk guarantees); >1 ⇒
     "ambiguous basename match"; 0 ⇒ "file not found".
- Per-ref failure diagnostics carry ReportPath/Citation/Path (+SourceName and ResolvedPath when
  resolution succeeded but snippet reading failed).

## 5. Service orchestration and status aggregation (`internal/codeextract/service.go`)

- `Extract` (:12-47): iterates reports in argument order; unreadable report file ⇒ hard error
  wrapping the os.ReadFile cause (:50-53) — fail-fast across the batch, nothing rendered.
- `extractReport` (:49-110):
  - refs>0 && sources==0 ⇒ report-level diagnostic "missing source table", ALL refs marked
    unresolved with that reason, early return (:56-70).
  - Otherwise resolve sources (diagnostics appended :71-73), then per ref: resolve → readSnippet;
    either failure marks the reference unresolved while later refs/reports continue (:82-103).
- `readSnippet` (:112-126): whole-file read; CRLF→LF normalization; split "\n"; 1-based; any
  requested line out of [1,len] fails the WHOLE reference ("line %d out of range"); list order
  preserved in output.
- Unresolved aggregation (:20-29): every diagnostic with Citation!="" OR Reason!="" from all
  reports lands in Result.Unresolved — including report-level items like missing-table and
  source-root failures (which have no Citation but have Reason).
- Status precedence (:31-45): `validation` iff (a) any report has diagnostics>0 ∧ Sources==0 ∧
  References>0, OR (b) any diagnostic reason is exactly "duplicate source row" /
  "malformed source row". Checked after ALL reports are collected; early return keeps the full
  Reports slice. Note boundary (a) also fires when a report HAS a source table whose every root
  failed resolution and references exist (Sources==0) — such reports classify as validation, not
  partial. Otherwise `partial` iff Unresolved non-empty; else `ok`.

## 6. Rendering (`service.go:128-167`, domain.go)

- Text (:128-161): per report `Report: <path-as-rel-or-raw>` then per-reference blocks
  (Source/Path/Lines/optional Resolved/Snippet lines `    N: text` or `Unresolved: reason`);
  `References: 0` line when a report has none; trailing `Unresolved: <count>` plus
  `<rel> [<citation>]: reason` lines. `rel()` (:169-177) slash-normalizes; paths outside the
  workspace root print as-is.
- JSON (:163-167): `json.Encoder` with 2-space indent; field order fixed by struct definition
  (domain.go:8-59); no maps serialized ⇒ byte-deterministic for identical inputs.
  Types: Result{reports, unresolved omitempty, status}, ReportResult{path, sources, references,
  diagnostics all omitempty}, Source{index,name,path,root}, Reference{report_path, source_name,
  citation, cited_path, line_spec, resolved_path, status("resolved"/"unresolved"), snippet,
  unresolved}, Line{number,text}, Diagnostic{report_path, source_name, citation, path, reason};
  `UnresolvedDiagnostic = Diagnostic` alias.

## 7. Inputs / outputs

Inputs: report files (any readable path), workspace root (marker discovery or flags/env), the
real filesystem under parsed source roots (reads + directory walks), CLI args/env/workDir.
Outputs: stdout text or JSON, or one optional output file (parent dirs auto-created 0755, file
written 0644, truncating existing content); stderr classified message on non-OK statuses.
External effects limited to: file reads, WalkDir traversals bounded to source roots, optional
MkdirAll+WriteFile. No network, no subprocess, no DB, no git.

## 8. Authoritative state and ownership boundaries

Stateless surface: no persisted state of its own beyond the optional output file; every run
re-derives everything from current disk contents. Product behavior owned by `internal/codeextract`
(parser.go/resolver.go/service.go/domain.go/doc.go); `internal/app/code_commands.go` only parses
args, invokes, renders, maps exits (contract enforced by sprint requirements, §14). The sole
in-tree consumer of `codeextract.Extract/RenderText/RenderJSON` is `runCode` (verified by grep).
Restart semantics: trivially re-runnable/idempotent reads; the only mutation is the output-file
overwrite.

## 9. Invariants (as implemented)

1. A citation can never cause a read outside the workspace-root-contained source roots:
   lexical gates (absolute/`..`/NUL) + symlink-evaluated containment on every candidate
   (resolveSourceRoot, existingContainedFile) + basename walk bounded to roots.
2. Duplicate/malformed source rows and reference-bearing reports lacking any usable source table
   escalate to the validation outcome; individual unresolved references degrade to partial.
3. One bad report does not prevent other reports from being inspected — EXCEPT an unreadable
   report file, which fails the entire command before any output.
4. Output ordering is fully deterministic: reports in argument order, refs by citation sort,
   sources by index/name, basename matches lexicographic, JSON struct-field order.
5. Requested line numbers are echoed verbatim in output (normalized specs), and out-of-range
   requests fail loudly as unresolved references rather than clamping.
6. En-dash inputs normalize to hyphen ranges in both spec echo and behavior.
7. Extraction results are written (stdout or file) regardless of partial/validation status.

## 10. Trust boundaries

- Report Markdown is report-controlled input steering filesystem access: it chooses source-root
  directories and per-citation paths. Controls: absolute-path rejection, `..` cleaning rejection,
  NUL rejection, EvalSymlinks-backed containment within workspaceRoot (roots) and within source
  roots (files), ambiguity refusal on multi-match basenames, VCS/deps dir exclusion during walks.
- The report file itself and its location are not containment-checked — any readable path the
  invoking user passes is processed; only SOURCE roots must reside under the workspace root.
- The `--output` path sits at trusted-operator position (CLI invoker): relative to workDir,
  parents auto-created 0755, file truncated at 0644; no path restriction applies.
- Snippet/report content is emitted verbatim into stdout/JSON/output file (no redaction layer on
  this surface; requirements forbid printing secrets/prompts/environment, and the content here is
  whatever the cited files contain).

## 11. Cancellation / retry / restart / error semantics

Synchronous, context-free (`deps.ctx` unused by this command); no cancellation points, no
retries. Errors: usage (ExitUsage), workspace discovery + unreadable report + output-write
failure (ExitWorkspace), render/stdout write (ExitError/unclassified), validation status
(exit 5), partial status (exit 8). Crash mid-run leaves at most partially-created output parent
dirs (MkdirAll precedes WriteFile); no cleanup, none needed beyond the output artifact.

## 12. Immediate surface dependencies

- **study-task-execution** (declared upstream producer): study runs/synthesis emit the reports
  this command audits — `studies/<s>/reports/source/<dim-ref>/<source>.md` and
  `studies/<s>/reports/final/<dim-ref>.md` (internal/study/reports.go:8-14); the report template
  embeds the `| # | Source | Path |` table (workspace/scaffold/templates/report.md:13-15) and
  instructs `path/to/file.ts:NN` evidence format (:71). Linkage is purely textual convention:
  internal/study and internal/codeextract share NO Go types or imports. Note they define
  "citation" differently: study validation's `citation.shape` regex accepts bare
  `word.ext:N(-N)` without backticks and without comma lists (internal/study/validation.go:79-86,
  223+), whereas codeextract requires backticks and supports lists/en-dash — a report can pass
  study validation yet yield zero or different extractions here.
- **foundation/config**: workspace discovery (marker file, env, flags) shared with every command;
  exit-class vocabulary in app.go.
- Nothing else: platform/runtime, sprint, storage, web, TUI are untouched by and uninvolved in
  this surface.

## 13. Contracts (CURRENT-CONTRACT evidence)

- TRD.md §16 (workspace docs/TRD.md:1572-1607): supported citation forms; sources-table row
  shape `| 1 | source-name | \`sources/source-name\` |`; workspace- AND report-relative source
  paths; 4-step resolution algorithm (source-root-relative → source-prefix strip → basename
  search excluding .git/node_modules/known dirs → record unresolved); output fields incl.
  structured JSON refs/status.
- Sprint 13 requirements.md AC-12…AC-25 (:51-70) & Constraints (:85-96): thin app adapter;
  validation-style outcome for missing tables; en-dash tolerance + normalization; resolution
  order; ignored-dir set; deterministic argument-order multi-report processing; success/partial/
  validation/usage/workspace exit mapping; module ownership (`internal/codeextract` owns
  behavior; no global parser/resolver packages); local-only resolution with no `..` escapes;
  bounded basename fallback; offline fixture tests; deterministic tested JSON (release-wide
  schema stability explicitly deferred to Sprint 14, Non-Goal :82).
- reasoning.md Decisions 3-6 (:141-183): ownership; parsing/resolution rules (Decision 4 records
  "source table required only when supported code citations are present"); deterministic output +
  exact exit-code mapping (Decision 5); local-only safe path handling + invocation-scoped
  basename cache (Decision 6).
- User docs mirror the surface (docs/cli-reference.md:428-434; README).

## 14. Tests (evidence map)

internal/codeextract/codeextract_test.go (package-level fixtures, t.TempDir):
- TestExtractResolvesRangesListsBasenamesAndIgnoredDirs (:10-42): en-dash range `1–2`, basename
  fallback hitting `sub/target.go` while ignoring `node_modules/target.go`, snippet text checks,
  text render contains "Unresolved: 0".
- TestExtractReportsPathEscapeOutOfRangeAndMalformedSpecs (:44-61): `../secret.go:1` escape,
  out-of-range `main.go:9`, inverted range `2-1` ⇒ exactly 3 unresolved, status partial.
- TestExtractReportWithSourceTableAndNoSupportedCitations (:63-90): markdown-link citations not
  matched, prose-only report with valid table ⇒ ok, "References: 0".
internal/app/code_commands_test.go (drives full app.Run against a scaffolded workspace via
initializedWorkspace, app_test.go:257-264):
- Help (:11-23); text+JSON round trip incl. `--output` writes file with empty stdout and parsed
  status/reference fields (:25-66); argument-order rendering of multiple reports (:68-86);
  `--output <dir>` write failure ⇒ ExitWorkspace with empty stdout (:88-102); unresolved ref ⇒
  ExitPartial + "Unresolved:" in stdout; missing-table report ⇒ ExitValidation (:104-123);
  unreadable report listed first ⇒ ExitWorkspace fast-fail naming the path (:125-141).
Baseline: go test ./..., -race, vet, -cover green at frozen commit (review/baseline).

## 15. Explicit unknowns / open questions (for later reviewers)

1. No in-tree caller besides the CLI dispatch; whether scripts/CI invoke `ultraplan code` is not
   observable from the repo.
2. JSON shape is sprint-local by explicit decision (Sprint 14 owns release-wide schema stability).
3. Parser scans fenced code blocks too — a backtick citation quoted inside a code fence is
   extracted like any other; fixtures do not cover fences.
4. Table cells split on every "|": escaped pipes (`\|`) inside names/paths would mis-split;
   untested.
5. Ranges expand to explicit []int at parse time before file size is known; readSnippet then
   bounds-checks per line and fails the whole reference on the first out-of-range line.
6. containedPath decides containment on EvalSymlinks-resolved paths but returns the unevaluated
   absolute path, which callers Stat/read (re-following symlinks). Behavior under concurrent
   symlink swap (TOCTOU) is untested; tests cover static layouts only.
7. All-sources-unresolvable reports (broken table path + existing citations) classify as
   validation (exit 5) via the Sources==0 branch rather than partial — emergent boundary of the
   aggregation rule at service.go:32.
8. Basename fallback cost is bounded only by source-root tree size minus five ignored dir names;
   cache is per-invocation so N distinct basenames walk the trees N times.
9. Windows-specific path behaviors (FromSlash conversions, separator handling, symlink semantics)
   are untested in the fixture suite (linux CI).
10. Sprint-required area-reasoning artifact `reasoning/code-extraction.md` does not exist in the
    frozen workspace; consolidated reasoning.md Decision 4 carries the equivalent decisions
    (requirements.md:19 lists the file; flow-state.json suggests stage history, not investigated).
11. An untracked `.ultraplan/` runtime dir exists in the target working tree; the frozen-commit
    diff shows zero changes to this surface's paths, so reviewed content == frozen content.

— End of context pack. Descriptive only; no defect claims made or implied.
