Let me check how completed jobs recorded their results so I mirror the expected output artifacts.All evidence gathered. Writing the context pack now.Pack written. Now verifying a final set of line references against sources, then writing the job result summary.Context pack complete: `review/surfaces/context-run-cli-observation.md` (~32 KB, 11 sections, descriptive only — no defect claims), with result summaries in `review/jobs/context-run-cli-observation/{attempt-01-result.md,result.md}`.

Coverage highlights:

- **Control flow**: all five doors (`list/show/follow/cancel/diagnostics`) traced from `app.go:161` dispatch through `runRepository` (per-process pool + startup Reconcile even for reads, run_control.go:37-75); keyset-cursor List (lifecycle.go:196-291); follow replay loop (512-batch, 250 ms catch-up / 1 s idle, terminal-drain stop, ctx-done = observation-only exit); CAS-idempotent cancel with four-reason whitelist; Health + O_EXCL/0600/≤1 MiB payload-free support export; `RunUseCases` adapter shared with web/TUI.
- **State/invariants**: authority lives entirely in `internal/runcontrol`'s SQLite store; validated-on-read snapshots, bounded opaque cursors, two-layer journal sanitization before display, private-file discipline, stable exit-class mapping.
- **Trust boundaries**: strict opaque RunID grammar pre-SQL; text modes print no payload fields; marker-deny redaction at app + storage layers; POSIX-only local trust model.
- **Contracts**: cli-reference.md:492-520 (+stable-JSON exclusion :474-491, exit classes :18-30), user-guide.md:374-391, TRD §18C incl. the :1255 typed retention-gap requirement, PRD:162.
- **Tests**: full evidence map plus factual gaps (no pagination/usage-error tests, no non-terminal-follow test, no CLI↔web replay parity test).
- **Unknowns**: 12 neutral open questions for reviewers, including CLI follow lacking web's `cursor_ahead`/`replay_gap` responses vs TRD:1255, and read-triggered reconcile mutating durable state during "inspection" commands.

All line references spot-checked against the frozen commit.