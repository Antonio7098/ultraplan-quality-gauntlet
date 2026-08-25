Frozen commit verified. Reading the primary surface files now.Core files read. Now tracing wiring, callers, config, tests, and workspace contracts in parallel.All context gathered and verified. Writing the context pack.Context pack written: `review/surfaces/context-repo-publication.md` (164 lines, 11 sections, matching the existing pack format).

Coverage summary:

- **Purpose & modes**: `off`/`commit`/`commit-and-push` via `git.stage_completion`; owned-paths-only publication always ordered after durable stage persistence.
- **Control flow traced end-to-end**: `stagePublisher` wiring (app layer, 6 injection sites) → `CommandPublisher.Publish` step-by-step (temp index, CAS `update-ref`, flock lock, bounded prompt-free push) → sprint adapters (planning/execute/review/smoke with exact call-site ordering at flow.go:149/169/215, execute.go:341, review.go:633, smoke.go:39-58) → study adapters (direct publish vs run-loop deferred consumer goroutine).
- **State & boundaries**: three repo classes (workspace/target worktree/harness), `<git-common-dir>/ultraplan-publish.lock`, roadmap delivery marks (`project/roadmap_status.go:15`), remote-name validation at both config load and pre-push.
- **Contracts**: operative contract is implementation docs (docs/configuration.md:220-227, docs/user-guide.md:305-307); workspace has no dedicated publication contract (TRD.md:2237 is FUTURE-INTENT only).
- **Tests mapped**: publisher_test.go index-preservation and push-retry pins; sprint/study path-set and ordering tests; smoke roadmap mark; config validation.
- **Unknowns recorded**: non-repo studies, diverged-upstream retry semantics, hook interactions, Windows no-lock build, `Publications` rendering consumers.

One flagged unknown was resolved during verification (env-var mapping confirmed at config.go:137-139). No defects hunted, per assignment.