package gauntlet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func InitialJobs() []Job {
	maps := []struct{ id, lens string }{
		{"map-01-user-operations", "Map user-visible product operations and workflows. Discover coherent product surfaces from what a user can ask UltraPlan to do."},
		{"map-02-state-authorities", "Map durable and ephemeral state authorities, then group behaviours around the state they read and mutate."},
		{"map-03-trust-boundaries", "Map all untrusted/external inputs and capability transitions, then identify product surfaces around those trust boundaries."},
		{"map-04-execution-paths", "Map actual execution lifecycles from request through runtime/process/cancellation/result/persistence/observation."},
		{"map-05-interface-surfaces", "Map CLI, TUI, and web interface behaviours, shared use cases, and divergent interface-specific paths."},
		{"map-06-test-topology", "Map tests, fault harnesses, smoke paths, and the product behaviours each actually protects; infer surface boundaries from behavioural coverage."},
	}
	jobs := make([]Job, 0, 7)
	deps := make([]string, 0, len(maps))
	for _, m := range maps {
		jobs = append(jobs, Job{ID: m.id, Stage: StageMap, Agent: "quality-surface-mapper", Kind: "surface-map", Lens: m.lens, Status: StatusPending})
		deps = append(deps, m.id)
	}
	jobs = append(jobs, Job{ID: "map-arbiter", Stage: StageMapArbiter, Agent: "quality-surface-arbiter", Kind: "surface-map-arbiter", Status: StatusPending, DependsOn: deps})
	return jobs
}

var invariantLenses = []struct{ id, lens string }{
	{"invariant-cancellation", "Across every cancellable surface, determine whether cancellation is idempotent, truthful, bounded, and unable to overwrite a stronger terminal fact."},
	{"invariant-error-truth", "Trace how failures become public/operational outcomes. Find swallowed, mislabeled, downgraded, duplicated, or misleading errors."},
	{"invariant-durability", "Find any acknowledged or externally visible mutation whose authoritative fact can exist only in volatile memory or be lost across restart."},
	{"invariant-trust-flow", "Trace untrusted data across surfaces into filesystem, process, runtime, persistence, HTTP, prompts, and rendering. Look for missing validating owners."},
	{"invariant-identity", "Check run/stage/task/attempt/session/correlation identities for consistent propagation and accidental aliasing across surfaces."},
	{"invariant-secrets", "Check whether credentials, tokens, sensitive config, model content, paths, or private payloads can leak into logs, events, persisted state, prompts, or HTTP output."},
	{"invariant-resource-bounds", "Check every externally influenced collection, output stream, queue, subscriber set, process, retry loop, file, and retained event set for explicit bounds."},
	{"invariant-retry-idempotency", "Trace retry and replay semantics across surfaces. Look for duplicate effects, unknown outcomes, per-attempt keys, and check-then-act races."},
	{"invariant-migration-compat", "Check persisted formats, migration paths, backward compatibility, stale state handling, and restart across version boundaries."},
	{"invariant-interface-parity", "Compare CLI/TUI/web semantics for the same operations: validation, cancellation, errors, status, lifecycle, and evidence."},
	{"invariant-observability", "Determine whether emitted events/logs/metrics/status can reconstruct actual execution truth, especially after failure and restart."},
	{"invariant-capability-containment", "Check permissions, shell/process capabilities, external-directory access, agent/tool boundaries, and fail-open defaults across the system."},
}

func ExpandFromSurfaceMap(reviewRoot string) (*Review, error) {
	r, err := Load(reviewRoot)
	if err != nil {
		return nil, err
	}
	if r.Expanded {
		return r, nil
	}
	arb, err := FindJob(r, "map-arbiter")
	if err != nil {
		return nil, err
	}
	if arb.Status != StatusPassed || arb.Result == "" {
		return nil, fmt.Errorf("map-arbiter must pass before expansion")
	}
	b, err := os.ReadFile(filepath.Join(reviewRoot, filepath.FromSlash(arb.Result)))
	if err != nil {
		return nil, err
	}
	m, err := ParseSurfaceMap(string(b))
	if err != nil {
		return nil, fmt.Errorf("parse map-arbiter result: %w", err)
	}
	if err := ValidateSurfaceMap(m); err != nil {
		return nil, err
	}
	r.SurfaceMap = &m
	r.Jobs = append(r.Jobs, expandedJobs(m)...)
	r.Expanded = true
	r.UpdatedAt = nowUTC()
	if err := saveAtomic(filepath.Join(reviewRoot, stateName), r); err != nil {
		return nil, err
	}
	mapDir := filepath.Join(reviewRoot, "surfaces")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		return nil, err
	}
	if err := saveAtomic(filepath.Join(mapDir, "map.json"), m); err != nil {
		return nil, err
	}
	return r, nil
}

func expandedJobs(m SurfaceMap) []Job {
	var jobs []Job
	for _, s := range m.Surfaces {
		ctxID := "context-" + s.ID
		jobs = append(jobs, Job{ID: ctxID, Stage: StageContext, Agent: "quality-context-builder", Kind: "surface-context", SurfaceID: s.ID, DomainID: s.Domain, Status: StatusPending})
		for _, lens := range reviewerLenses(s.Risk) {
			jobs = append(jobs, Job{ID: fmt.Sprintf("review-%s-%s", s.ID, lens.id), Stage: StageReview, Agent: "quality-reviewer", Kind: "surface-review", Lens: lens.prompt, SurfaceID: s.ID, DomainID: s.Domain, Status: StatusPending, DependsOn: []string{ctxID}})
		}
	}
	for _, seam := range m.Seams {
		jobs = append(jobs, Job{ID: "seam-" + seam.ID, Stage: StageSeam, Agent: "quality-seam-reviewer", Kind: "seam-review", SeamID: seam.ID, Status: StatusPending, DependsOn: []string{"context-" + seam.From, "context-" + seam.To}})
	}
	for _, inv := range invariantLenses {
		jobs = append(jobs, Job{ID: inv.id, Stage: StageInvariant, Agent: "quality-invariant-reviewer", Kind: "cross-surface-invariant", Lens: inv.lens, Status: StatusPending})
	}
	for _, s := range m.Surfaces {
		jobs = append(jobs, Job{ID: "tribunal-" + s.ID, Stage: StageTribunal, Agent: "quality-tribunal", Kind: "surface-tribunal", SurfaceID: s.ID, DomainID: s.Domain, Status: StatusPending})
	}
	for _, d := range m.Domains {
		jobs = append(jobs, Job{ID: "domain-" + d.ID, Stage: StageDomain, Agent: "quality-domain-chair", Kind: "domain-chair", DomainID: d.ID, Status: StatusPending})
	}
	jobs = append(jobs,
		Job{ID: "synth-correctness", Stage: StageSynth, Agent: "quality-synth", Kind: "system-synthesis", Lens: "Production correctness: what can make UltraPlan produce a wrong result, lose truth, or violate a product invariant?", Status: StatusPending},
		Job{ID: "synth-security-reliability", Stage: StageSynth, Agent: "quality-synth", Kind: "system-synthesis", Lens: "Adversarial safety and reliability: what can malicious, malformed, concurrent, degraded, or unexpected environments make UltraPlan do?", Status: StatusPending},
		Job{ID: "synth-assurance", Stage: StageSynth, Agent: "quality-synth", Kind: "system-synthesis", Lens: "Assurance quality: where does UltraPlan appear safer or more correct than its tests, smoke evidence, and diagnostics actually prove?", Status: StatusPending},
		Job{ID: "arbiter", Stage: StageArbiter, Agent: "quality-arbiter", Kind: "final-arbiter", Status: StatusPending},
	)
	return jobs
}

type reviewerLens struct{ id, prompt string }

func reviewerLenses(risk string) []reviewerLens {
	base := []reviewerLens{
		{"correctness", "Correctness: wrong outcomes, invariant violations, edge cases, state inconsistencies, parsing/validation mistakes, and misleading success."},
		{"failure", "Failure/concurrency: cancellation, restart, partial progress, retry, idempotency, races, liveness, resource ownership, and unknown outcomes."},
		{"security", "Security/misuse: attacker-controlled or malformed inputs, trust transitions, filesystem/process/runtime capability abuse, unsafe defaults, secrets, and exploitability."},
		{"verification", "Verification/operability: missing or misleading tests, fake-only confidence, observability gaps, error truth, performance/resource hazards, and bugs the current verification would allow."},
	}
	switch normalizeRisk(risk) {
	case "critical":
		return append(base, reviewerLens{"correctness-b", "Independent correctness review. Do not assume another reviewer exists; reconstruct the surface and try to disprove its behavioural guarantees from scratch."}, reviewerLens{"security-b", "Independent adversarial security review. Require a concrete attacker-controlled source, path, sink/consequence, and missing control before calling something exploitable."})
	case "high":
		return append(base, reviewerLens{"adversarial", "Fresh-context adversarial general review: assume the implementation is overconfident. Find concrete contract violations or state/failure paths that the obvious review lenses may miss."})
	case "low":
		return []reviewerLens{base[0], {id: "assurance", prompt: "Combined security/verification review for a low-risk surface. Look for concrete misuse, missing boundary validation, and test gaps; do not invent speculative vulnerabilities."}}
	default:
		return base
	}
}

func ParseSurfaceMap(text string) (SurfaceMap, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return SurfaceMap{}, err
	}
	var m SurfaceMap
	if err := json.Unmarshal([]byte(jsonText), &m); err != nil {
		return SurfaceMap{}, err
	}
	return m, nil
}

func ValidateSurfaceMap(m SurfaceMap) error {
	if len(m.Surfaces) < 8 {
		return fmt.Errorf("surface map has only %d surfaces; expected a meaningful product decomposition", len(m.Surfaces))
	}
	if len(m.Surfaces) > 80 {
		return fmt.Errorf("surface map has %d surfaces; decomposition is too fragmented", len(m.Surfaces))
	}
	seen := map[string]bool{}
	for _, s := range m.Surfaces {
		if !validID(s.ID) {
			return fmt.Errorf("invalid surface id %q", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate surface id %q", s.ID)
		}
		seen[s.ID] = true
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Purpose) == "" || strings.TrimSpace(s.Domain) == "" {
			return fmt.Errorf("surface %s is missing name, purpose, or domain", s.ID)
		}
		if normalizeRisk(s.Risk) == "" {
			return fmt.Errorf("surface %s has invalid risk %q", s.ID, s.Risk)
		}
	}
	for _, seam := range m.Seams {
		if !validID(seam.ID) || !seen[seam.From] || !seen[seam.To] {
			return fmt.Errorf("invalid seam %q (%s -> %s)", seam.ID, seam.From, seam.To)
		}
	}
	if len(m.Domains) == 0 {
		return fmt.Errorf("surface map has no domains")
	}
	return nil
}

func extractJSONObject(text string) (string, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return "", fmt.Errorf("no JSON object found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated JSON object")
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizeRisk(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "critical", "high", "normal", "low":
		return strings.ToLower(strings.TrimSpace(r))
	default:
		return ""
	}
}

func sortedSurfaceIDs(m SurfaceMap) []string {
	ids := make([]string, 0, len(m.Surfaces))
	for _, s := range m.Surfaces {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}

func nowUTC() time.Time { return time.Now().UTC() }
