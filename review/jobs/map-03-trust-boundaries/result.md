I'm the `map-03-trust-boundaries` mapper. Let me get oriented in the target first, then delegate bounded discovery work.I'm the `map-03-trust-boundaries` surface mapper. The target is ultraplan-go @ f73c4dc. I'll delegate six bounded trust-boundary discovery questions to `review-worker` subagents in parallel, each returning facts only (entrypoints, sources, controls, evidence) — no conclusions.Six bounded discoveries returned. Before synthesizing, let me verify a few load-bearing claims that anchor surface seams.Only `go.mod`/`go.sum` differ from the frozen commit (SDK bump) — all source line references hold for f73c4dc. Writing the surface map now.Map complete and written to `review/maps/map-03-trust-boundaries.md`.

**Deliverable summary** (`map-03-trust-boundaries`, trust-boundary lens):

- **Method**: 6 parallel bounded `review-worker` discoveries + direct verification of load-bearing claims. Working tree delta vs frozen `f73c4dc` confirmed as go.mod/go.sum only — all line references valid.
- **Inventories**: 13 untrusted input channels (CLI/env/config files → agent output → external process stdout → HTTP/TUI → durable state re-read as input) and 8 capability transitions (dry-run→execute gates, accept-before-execute, git publication CAS, force-unlock/reset guards, file↔DB authority handoff, sandbox grants, subprocess spawn env matrix, run fencing).
- **12 candidate surfaces** in 5 proposed domains:
  - A Admission: `cli-config-admission` (normal), `durable-operation-gateway` (**critical** — single accept-before-execute chokepoint)
  - B Agent trust transitions: `agent-artifact-validation`, `structured-untrusted-decoding`, `session-identity-continuation` (all high)
  - C External process & repo mutation: `subprocess-execution-capability` (high), `publication-capability` (normal)
  - D Persistence trust: `filesystem-containment-persistence` (high), `durable-journal-fencing` (**critical**), `state-authority-handoff` (high — zero test evidence across its branches, recorded as fact)
  - E Observation: `web-http-boundary` (high), `tui-console-entry` (low)
- **8 cross-surface seams**, including the notable fact that four distinct path-containment implementations coexist rather than one shared resolver, and three divergent PID-liveness predicates.

No defects reported in this phase; asymmetries are logged as labeled "seam observations" for downstream reviewers.