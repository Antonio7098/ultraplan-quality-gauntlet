Surface files are identical between the frozen commit and HEAD, so I'll read working-tree sources directly.Context pack noted. Now inspecting tests, callers, and wiring myself.Tests pin index preservation and retry idempotence. Now checking call-site ordering, config validation, and the workspace-side contracts.The execute target is a dedicated `git worktree add -b ultraplan/...` — so branch exists and isn't detached. Now checking where `AuthorChangedPaths` originate and how `Publications` render, then running runtime experiments.`AuthorChangedPaths` come from ultraplan's own snapshot diff gated by manifest allowlist — not raw harness input. Now running the surface tests as a baseline, then adversarial experiments.I'll replicate the frozen commit into /tmp so I can drive the real package without touching the target tree.Experiment A confirmed: owned-path staged-only entries are discarded — but this exactly mirrors `git commit -- <paths>` partial-commit semantics. Recording that; next: linked-worktree publish, push-rejection exit code, upstream `.`, and GIT_DIR inheritance.Push rejection is correctly surfaced. Now the GIT_DIR inheritance experiment.Split-brain confirmed: `GIT_DIR` in the environment makes commits land in a different repository's object/ref store while `Result` reports success against the intended root. Checking whether config validation always runs after env overrides, plus the upstream `.` case:Real defect found: with a local-tracking upstream (`branch.<b>.remote = "."`), publication "pushes" to `.` and **fast-forwards the tracked local branch** (user's `main` moved to include the sprint commit), reporting `Remote:"." Pushed:true`. Now verifying config validation ordering and remaining study-side ordering:All evidence gathered. Final report:

---

## Findings — repo-publication correctness review

### F1 — Local-tracking upstream causes publication to fast-forward an unrelated local branch, reported as successful push (Medium severity, High confidence)

**Claim.** When the current branch's upstream is a *local* branch (`branch.<name>.remote = "."`, as created by `git switch -c feat --track <sibling>`, `git branch --set-upstream-to=<local>` — stacked-branch workflows), `Publish` treats `"."` as a valid remote name and pushes `HEAD:<upstreamRef>` **to the repository itself**, moving the tracked local branch.

**Observable bad outcome (reproduced).** Repo with `dev` tracking local `main`: after `Publish(ModeCommitAndPush)` of a commit on `dev`, result was `{Remote:"." Pushed:true err:nil}` and **`main` was fast-forwarded to include the ultraplan commit** (`## dev...main` showed 0/0 after being ahead). An unowned ref mutation plus misleading success reporting ("pushed" to remote `"."`).

**Trigger/preconditions.** Mode `commit-and-push`; current branch's `%(upstream:remotename)` = `.`; tracked ref passes `validRemoteName` (dot allowed, publisher.go:304-315).

**Path.** publisher.go:140-154 — `for-each-ref %(upstream:remotename)` → `"."` passes validation at :146 → `push --porcelain -- . HEAD:refs/heads/main`. Same code path also accepts non-`refs/heads/*` upstream refs (e.g., a misconfigured `branch.x.merge = refs/tags/v1` would attempt to create/move a remote tag).

**Controls/counter-evidence searched.** Config validation (config.go:480-486) doesn't constrain upstream semantics; docs/configuration.md:222 says "using its upstream when present" without excluding local upstreams; no test covers local-tracking upstream; sprint worktree branches (`ultraplan/...`) have no upstream so the trigger is the workspace/study repos' checked-out branch.

**Fix + regression test.** Treat `.` (or any upstream whose remotename isn't a configured remote / whose remoteref isn't `refs/heads/*`) as no-upstream — fall back to policy.Remote — or fail explicitly. Test: branch tracks a sibling local branch; publish; assert sibling ref unchanged and either fallback push or clear error.

### F2 — Inherited `GIT_DIR`/`GIT_WORK_TREE` produce split-brain git operations committed into the wrong repository while `Result` reports success (Medium severity, High confidence)

**Claim.** `gitEnvironment` (publisher.go:284-302) preserves the whole parent environment except two keys. A `GIT_DIR` exported in the user's shell makes every child git command use a foreign object/ref store while `-C dir` still supplies the working tree.

**Observable bad outcome (reproduced).** With `GIT_DIR=<other-repo>/.git`, `Publish(Root=innocent)` returned `{Repository:".../innocent" Committed:true}` — but the commit exists **only in the other repo** (`git log` confirms; innocent has zero commits). Git resolves worktree=CWD when only `GIT_DIR` is set, so staging reads innocent's files and objects/refs land elsewhere. If the foreign repo has a matching remote configured, stage content gets pushed there too — externally visible and irreversible.

**Trigger/preconditions.** Any `GIT_*` location override (`GIT_DIR`, `GIT_WORK_TREE`; likewise inherited `GIT_INDEX_FILE` undermines the temp-index isolation for `update-ref`/`reset`, which run with `nil` extraEnv) present in the CLI process environment.

**Path.** publisher.go:268-269 — env built from `os.Environ()` unsanitized; :79/:100/:208 etc. all inherit it; result fields (:95) derived from commands that interpret `GIT_DIR` inconsistently with each other, producing the split-brain and the false `Repository`.

**Controls/counter-evidence searched.** No sanitization anywhere in the repo (grep: only `GIT_INDEX_FILE` at publisher.go:224); docs don't require a scrubbed environment; agent runtime env isolation doesn't apply to ultraplan's own process.

**Fix + regression test.** Unconditionally strip `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_COMMON_DIR`, `GIT_NAMESPACE` from the child env (the overrides map already supports key removal). Test: set `GIT_DIR` to a second repo, publish into first, assert first repo contains the commit and second is untouched.

### Defended non-issues (hypotheses raised and disproved)

- **Owned-path staged-only index entries discarded** by the post-commit `reset -q HEAD -- paths` (publisher.go:256): demonstrated (staged v1 lost, dangling blob), but this is byte-for-byte git's own `git commit -- <paths>` partial-commit semantics; docs promise preservation only for *unrelated* staged changes. By design.
- **No-change republish pushes pre-existing unpushed local commits**: pinned intentionally by `TestPublisherRetriesPushWithoutDuplicateCommit` and sanctioned by docs ("Rerunning the stage pushes that commit"); pushing "the current branch" inherently publishes its local commits.
- **Non-fast-forward / missing remote push failures**: reproduced — exit code surfaces as an error, remote untouched, local commit retained per docs.
- **Linked-worktree execution**: verified end-to-end — commit goes to the worktree's branch, source branch untouched, lock/temp index correctly placed in shared common dir.
- **Roadmap mark vs publish-path divergence in smoke** (smoke.go:39 vs publication.go:115): unreachable — the review gate forces `DiagnosticOnly` for fail/blocked verdicts and rejects invalid verdicts (smoke_protocol.go:196-208), so `SmokePass && !DiagnosticOnly` implies pass-ish review verdict. Mark is idempotent, and retry converges since roadmap.md stays in the owned path set.
- **Deferred study consumer goroutine races** (run_loop.go:252-261): single consumer, buffered channel sized to task count, main goroutine touches `result` only after `<-publicationDone`; no race, no deadlock; drain-after-cancel yields an error rather than silent loss.
- **`stagePublisher` ignored ParseDuration error** (git_publication.go:14): unreachable — `config.Load` validates after env/YAML application (config.go:175), bounding mode/remote/timeout.
- **`for-each-ref` glob injection via branch names**: impossible — `*?[` are illegal in git refnames.
- **Windows build lacks flock** (lock_other.go): CAS `update-ref` still serializes the dangerous step; no docs claim locking on Windows. Residual risk limited to duplicate concurrent pushes of the same commit.
- **Zero-valid-path Publish pushing bare HEAD**: not reachable from any adapter (all call sites pass ≥1 always-non-empty owned path; `OutputPath`/`SummaryPath` are always constructed absolute).

Baseline: `go test ./internal/platform/gitpublish` green at the frozen commit; surface files identical between `5d54525` and working tree.