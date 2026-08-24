# UltraPlan Quality Gauntlet

A deliberately one-off, high-volume correctness/security/reliability review harness for **UltraPlan Go**.

It is the companion to `ultraplan-architecture-gauntlet`, but it asks a different question:

> **Where can UltraPlan be wrong, unsafe, insecure, unreliable, misleading, unverified, or operationally surprising?**

The harness is **surface-first**. It does not ask giant agents to scan the whole repository for one concern. Six independent agents first discover small product-behaviour surfaces, an arbiter reconciles them into a canonical surface graph, and later agents deeply review one surface or one seam at a time. Results are then falsified and aggregated hierarchically.

## Targets

- implementation: `Antonio7098/ultraplan-go`
- authoritative planning/architecture context: `Antonio7098/ultraplan-workspace`
- runtime SDK: [`Antonio7098/agentwrap`](https://github.com/Antonio7098/agentwrap), pinned to `3d1e4cbb6e036bc5cd288ffcb9423bbd0bf4b1b9`
- OpenCode model: `openrouter/stealth/ox-alpha`
- OpenCode version used by the owner when this harness was built: `1.18.21`

AgentWrap is used for the actual OpenCode subprocess lifecycle, structured events, cancellation/cleanup, classified transient retries, and model enumeration. Every job attempt uses an isolated OpenCode SQLite database and disables OpenCode filesystem snapshots.

## Why surface-first?

A directory is not necessarily a behaviour. A surface such as `web-operation-cancellation` can cross `web -> app -> sprint -> runcontrol -> runtime`, while a large package such as `internal/sprint` contains several independent behaviours.

The discovery phase aims for small reviewable surfaces with:

- one primary workflow;
- identifiable entrypoints and outputs;
- a few state authorities;
- a bounded set of directly relevant files/tests;
- explicit trust/failure boundaries.

Risk controls redundancy: low-risk surfaces get two independent reviewers, normal four, high five, critical six.

## Review pipeline

```text
DETERMINISTIC BASELINE
        │
        ▼
6 INDEPENDENT SURFACE MAPPERS
        │
        ▼
SURFACE MAP ARBITER
        │
        ├── surfaces
        ├── seams
        └── domains
        │
        ▼
1 CONTEXT BUILDER / SURFACE
        │
        ▼
RISK-WEIGHTED INDEPENDENT SURFACE REVIEWERS
        │
        ├── correctness
        ├── failure / concurrency
        ├── security / misuse
        └── verification / operability
        │
        ├──────────────┐
        ▼              ▼
SEAM REVIEWS     CROSS-SURFACE INVARIANTS
        └───────┬──────┘
                ▼
       1 TRIBUNAL / SURFACE
      (falsify + reproduce)
                │
                ▼
          DOMAIN CHAIRS
                │
       ┌────────┼────────┐
       ▼        ▼        ▼
 correctness security  assurance
   synthesis  synthesis synthesis
       └────────┼────────┘
                ▼
          FINAL ARBITER
```

Broad roles may use bounded `review-worker` subagents. Narrow independent surface reviewers do **not** use subagents, to avoid correlated reasoning. Tribunals may use a dedicated `repro-worker` to try to prove or falsify important candidates.

## Operational hardening learned from the architecture gauntlet

This harness explicitly addresses the failures encountered during the earlier long run:

- **No ignored durable results.** `review/` is intentionally tracked. Only `.quality-gauntlet/` is ignored.
- **No future-shell PATH dependency.** `init` resolves OpenCode to an absolute executable path and stores it; `bind` can change it after moving machines.
- **No shared OpenCode DB between parallel jobs.** Each job attempt receives `.quality-gauntlet/jobs/<job>/attempt-N/opencode.db` through AgentWrap's request-scoped DB support.
- **No shared OpenCode snapshot object store.** AgentWrap starts OpenCode with filesystem snapshots disabled.
- **Stale running recovery.** Acquiring the single-orchestrator lock means a prior orchestrator no longer owns those jobs; persisted `running` jobs are converted to retryable failures automatically. `qgauntlet recover` is also available explicitly.
- **Integrated wedge watchdog.** A job is cancelled if AgentWrap emits no event for 20 minutes by default. There is also a 90-minute absolute attempt timeout. Both are configurable.
- **Process cleanup.** AgentWrap owns subprocess cancellation/process-group cleanup; on Linux its OpenCode adapter uses parent-death signalling.
- **Atomic state.** `review/state.json` is written by temp-file + fsync + rename.
- **Portable state.** Frozen commit identity is separate from local bindings. Move the review and run `qgauntlet bind --target ... --workspace ... --opencode ...`; no global path-replacement script is required.
- **Single orchestrator.** An OS file lock prevents two harness processes racing the same `state.json`.
- **Retry budget.** The supplied orchestrator uses a larger pass/attempt budget for the high-volume surface-review stage.

## Build

Requires Go 1.22+ and OpenCode.

```bash
go test ./...
go build -o bin/qgauntlet ./cmd/qgauntlet
```

The module pins AgentWrap at the commit above.

## Initialize

Run from this repository so OpenCode discovers the bundled `.opencode/agents/` definitions:

```bash
./bin/qgauntlet init \
  --target ../ultraplan-go \
  --workspace ../ultraplan-workspace
```

The default model is already `openrouter/stealth/ox-alpha`.

If OpenCode is not on the current shell PATH, provide it explicitly:

```bash
./bin/qgauntlet init \
  --target ../ultraplan-go \
  --workspace ../ultraplan-workspace \
  --opencode "$HOME/.opencode/bin/opencode"
```

Initialization freezes the exact target/workspace commits and records whether they were dirty at initialization.

## Doctor before a long run

```bash
./bin/qgauntlet doctor
```

`doctor` goes through AgentWrap's OpenCode model lister and requires the configured model to appear in the actual installed OpenCode model list. This deliberately avoids trusting a stale public model slug.

## Deterministic baseline

```bash
./bin/qgauntlet baseline
```

The baseline records, where available:

```text
go test ./...
go test -race ./...
go vet ./...
go test -cover ./...
go list ./...
staticcheck ./...
govulncheck ./...
gosec ./...
golangci-lint run ./...
```

Optional analyzers are run only when installed. A failing test/analyzer is **review evidence**, not an automatic harness failure.

## Inspect

```bash
./bin/qgauntlet status
./bin/qgauntlet next
./bin/qgauntlet prompt --id map-01-user-operations
./bin/qgauntlet index
```

After `map-arbiter` succeeds, the harness parses and validates its JSON surface map and dynamically creates all context/review/seam/tribunal/domain/synthesis jobs. The map is persisted to `review/surfaces/map.json`.

## Run manually stage by stage

```bash
./bin/qgauntlet run --stage map --parallel 6
./bin/qgauntlet run --stage map-arbiter --parallel 1
./bin/qgauntlet run --stage context --parallel 8
./bin/qgauntlet run --stage surface-review --parallel 8
./bin/qgauntlet run --stage seam --parallel 6
./bin/qgauntlet run --stage invariant --parallel 6
./bin/qgauntlet run --stage tribunal --parallel 6
./bin/qgauntlet run --stage domain --parallel 4
./bin/qgauntlet run --stage synth --parallel 3
./bin/qgauntlet run --stage arbiter --parallel 1
```

A retry pass is explicit:

```bash
./bin/qgauntlet run --stage surface-review --parallel 8 --retry-failed --max-attempts 30
```

AgentWrap itself gets a small bounded classified retry budget inside each harness attempt (`--agent-retries`, default 2). The outer task retry preserves every attempt for auditability.

## Run unattended

The supplied script runs stages in order, retries failures, indexes and commits tracked review progress after every pass/stage:

```bash
bash scripts/orchestrate.sh
```

For SSH/session disconnect resilience:

```bash
setsid nohup bash scripts/orchestrate.sh \
  > .quality-gauntlet/orchestrator.log 2>&1 < /dev/null &
```

This survives a terminal/SSH disconnect. It cannot prevent a hosting platform from stopping the VM itself; use an appropriate VM/keep-alive policy for multi-hour runs.

`QG_RUNNER=fake` exercises the whole orchestrator without model calls. `QG_PUSH=0` disables automatic `git push`. `QG_PARALLEL=8`, `QG_IDLE_TIMEOUT=20m`, and `QG_TASK_TIMEOUT=90m` tune execution.

## Moving an in-progress review

Copy the repository including tracked `review/`, then rebind local resources:

```bash
./bin/qgauntlet bind \
  --target /new/path/ultraplan-go \
  --workspace /new/path/ultraplan-workspace \
  --opencode "$(command -v opencode)"

./bin/qgauntlet doctor
./bin/qgauntlet recover
```

By default `bind` rejects a checkout whose HEAD differs from the frozen commit.

## Output

```text
review/                         # tracked, durable
  state.json
  baseline/
  surfaces/map.json
  jobs/<job>/
    attempt-01-prompt.md
    attempt-01-events.ndjson
    attempt-01-result.md
    result.md                   # latest successful output
  index.md
  final.md                      # after final arbiter

.quality-gauntlet/              # ignored, disposable
  orchestrator.lock
  jobs/<job>/attempt-N/opencode.db
```

The final report separates confirmed defects, probable-but-unreproduced defects, disputed/rejected findings, test gaps, cross-surface defect families, operational blind spots, and ordered remediation/regression-test work.

## Review doctrine sources

The prompts synthesize ideas from open-source work including:

- `addyosmani/agent-skills` — code review, security/hardening, TDD, debugging, observability, performance, API design, doubt-driven review;
- `trailofbits/skills` — audit-context building, false-positive checks, sharp-edge/misuse review, variant analysis;
- Sentry's open-source review/security skills — high-confidence exploitability emphasis;
- OpenAI Codex review examples — read-only defect-first verification;
- OpenClaw autoreview — source-aware review paired with behavioural validation.

The harness is tailored to UltraPlan rather than mechanically applying those frameworks.
