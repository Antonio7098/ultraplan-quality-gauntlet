Target verified at frozen commit, clean tree. Now reading the TUI surface files in parallel.All evidence gathered. Checking whether surface packs are registered in `review/index.md` before writing.Orchestrator owns `index.md`; my deliverable is the pack file itself. Writing the context pack now.Pack written. Now verifying a few remaining claims before finalizing.Both claims verified (TUI never sets `readOnly`; `OperationPartial` has no producer). One line-ref refinement:Context pack written: `review/surfaces/context-tui-console.md` (118 lines, 11 sections, descriptive only — no defect claims).

Coverage highlights:

- **Control flow**: full key-dispatch precedence (`app.go`), confirm→AcceptOperation→RunOperation→RecordOperationEvent→FinishOperation pipeline with the TUI digest formula `sha256(canonical+NUL+fingerprint)`, durable manager semantics (coalescing, control loop, terminal mapping), 1 Hz run-view polling, dashboard/refresh data path with retained-event window arithmetic.
- **State/invariants**: in-memory model only; accept-before-execute, stale-fingerprint recheck, bounded event windows (100/200/200/32 KiB), route-guarded async drops, QA verdict-neutral rendering.
- **Trust boundaries**: keystroke-built OperationRequests with scope validation, server-issued fingerprints, local-TTY assumption, SIGINT study-cancel lane, hard-coded diagnostic-override rationale.
- **Contracts**: TRD §7.4, ARCHITECTURE/PRD boundary rules, user-guide durable-run paragraphs, in-tree `doc.go`/`tuiHelp()`; noted cli-reference.md has no dedicated `tui` section.
- **Tests**: full evidence map incl. baseline coverage (68.3%) and factual gaps (no test drives `beginOperation`'s durable branch, quit-refusal, c-key durable-cancel branches).
- **Unknowns**: 11 neutral open questions, including unreachable `ActionConfirm` binding, study-cancel bypassing durable acceptance, lossy UI event delivery, `Error`-field reuse as a success notice.

All line references spot-checked against sources at the frozen commit.