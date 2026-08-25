Frozen commit is an ancestor of HEAD with no diffs on the target paths, so the working tree matches the frozen commit for this surface. Continuing with file reads.All evidence gathered. Writing the context pack now.Verifying two cited contract locations before finishing:Context pack written: `review/surfaces/context-code-extraction.md` (283 lines, descriptive only).

Verified against frozen target `8eef70f` (zero diff on this surface's paths) and frozen workspace `ab12dc3`. Contents:

- **Purpose/control flow**: CLI dispatch → `runCode` → `codeextract.Extract` → render (text/JSON) → output sink → status-to-exit mapping, with file:line cites throughout (`internal/app/code_commands.go`, `internal/codeextract/{parser,resolver,service,domain}.go`)
- **Mechanics**: citation regex + line-spec normalization, source-table row rules, 3-stage resolution with symlink-evaluated containment and invocation-scoped basename fallback, status aggregation precedence (`validation` > `partial` > `ok`) including the emergent all-sources-unresolvable boundary
- **Trust boundaries**: report-controlled paths vs the lexical+symlink gates; reports themselves uncontained; `--output` at trusted-operator position
- **Contracts**: TRD §16 (TRD.md:1572-1607), sprint-13 requirements ACs/constraints, reasoning.md Decisions 3–6
- **Dependencies**: study-task-execution linkage is textual convention only — notably study's `citation.shape` validator and codeextract's parser define "citation" differently (no shared code)
- **Tests evidence map** plus 11 explicit unknowns (fence-scanned citations, escaped pipes, parse-time range expansion, TOCTOU on returned unevaluated paths, missing `reasoning/code-extraction.md` artifact, etc.)