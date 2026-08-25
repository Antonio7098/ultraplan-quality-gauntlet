HEAD differs from the frozen commit only in `internal/sprint/smoke_author.go` + tests; `internal/runcontrol` is identical to the frozen commit. Reading the surface now.All evidence gathered. Writing the context pack now.Context pack for `run-journal-fencing` complete.

**Deliverables:**
- `review/surfaces/context-run-journal-fencing.md` — 156-line neutral context pack
- `review/jobs/context-run-journal-fencing/result.md` — job summary

**Verification:** frozen target `8eef70f` confirmed — working tree differs only in `internal/sprint/smoke_author.go`+tests; `internal/runcontrol`/`internal/app` are byte-identical to the frozen SHA. Workspace at `ab12dc3`, untouched.

**Pack coverage:**
- **Control flow**: OpenSQLite pragma/mode path, Accept quota gates, Claim fencing-generation allocation + claim-once CAS, Append triple-guard fenced CAS with inline compaction, Heartbeat dual-CAS, cancellation request(ownerless)/acknowledge(fenced) split, single-winner ProposeTerminal, two-pass Reconcile with exact birth-identity decision matrix, all read paths + CLI `run show|cancel|follow|diagnostics`.
- **State**: full schema-v1 inventory (CHECKs, immutability trigger, indexes), WAL/FULL/immediate posture, 0700/0600 symlink-rejecting boundary.
- **Trust boundaries**: durable rows re-read as strict input; agent payloads through producer-side filter + storage-side sanitizer allowlist with omission accounting.
- **Invariants/seams**: monotonic gapless sequences, terminal-arbitration single winner, conservative liveness (stall never terminalizes), quota/liveness coupling with reserved recovery event types; named seams (`outer-inner-double-fencing`, `persistence-gated-delivery`, etc.).
- **Contracts/tests**: TRD §18C/§19/§20 mapped to mechanisms; 20 test functions itemized by pinned property; baseline green.
- **14 explicit unknowns** for later reviewers (never-written `accepted`/`claimed` event types, dead alias columns, undocumented claim-once permanence, unverified `_txlock` pragma, reconcile batch starvation shape, unreachable `cancellation_state='uncertain'`, permanent append failure under saturated non-removable events, …).

Descriptive only — no defect claims made or implied, per doctrine.