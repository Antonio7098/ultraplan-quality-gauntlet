I have full coverage of the surface. Writing the context pack now.Context pack written to `review/surfaces/context-shared-usecase-vocabulary.md` (132 lines), covering:

- **Vocabulary core**: 27-kind `OperationKind` enum (operations.go:74-102), capability interfaces (`OperationalUseCases`, `WebOperations`, `DurableOperationManager`, `RunUseCases`), prepare→run pipeline with server-authority fingerprinting and the `failedOperation` error taxonomy
- **Three acceptance digest regimes** landing in one alias column: web `sha256(session+"\x00"+token)` (security.go:448-451), TUI `sha256(CanonicalRequest+"\x00"+InputFingerprint)` (tui/app.go:236-238), CLI empty digest via `beginDurableCLICommand` (durable_operations.go:55) — all into `Acceptance.OperationAlias`/`ConfirmationDigest` on run-control rows
- **Cancellation divergence**: per-door reason vocabularies ("user_request" vs "user_requested" vs CLI canonical set vs QA prose) and target-check ladders (hub session check, Target.Kind=="operation", CancelQA membership validation, TUI/CLI unchecked)
- **readOnly composition split**: serve forces `readOnly:true` twice, TUI leaves false → Status may rewrite flow-state.json; both help texts quoted side by side as coexisting contracts
- **Replay polling triplication**: `run follow`, web `followDurableOperationEvents` (identical 512/250ms/1s cadence), TUI refresh window — plus adjacent SSE/in-memory replay mechanisms
- Contracts (user-guide, local-web, api-contracts API-IDEMP-001), test topology (27-kind contract table, durable-CLI source inventory), 10 explicit unknowns (e.g., no cross-door durable-parity test exists; three outcome-mapping implementations unverified for equivalence)

Descriptive only — no defect judgments made.