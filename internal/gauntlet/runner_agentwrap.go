package gauntlet

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Antonio7098/agentwrap"
	awopencode "github.com/Antonio7098/agentwrap/opencode"
)

type ExecuteResult struct {
	Output        string
	RuntimeRunID  string
	ErrorCategory string
}

type Executor interface {
	Execute(context.Context, Review, Job, int, string, string, string, RunOptions) (ExecuteResult, error)
}

type AgentwrapExecutor struct{}

func (AgentwrapExecutor) Execute(ctx context.Context, review Review, job Job, attempt int, prompt, eventsPath, dbPath string, opts RunOptions) (ExecuteResult, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return ExecuteResult{}, err
	}
	base := awopencode.NewRuntime(
		awopencode.WithExecutable(review.Bindings.OpenCodePath),
		awopencode.WithExtraArgs("--agent", job.Agent),
		awopencode.WithSnapshots(false),
		awopencode.WithStderrLimit(64*1024),
	)
	var runtime agentwrap.Runtime = base
	if opts.AgentRetries > 1 {
		runtime = agentwrap.PolicyRunner{Runtime: base, Policy: agentwrap.BasicPolicy{
			MaxAttemptsPerTarget: opts.AgentRetries,
			RetryRateLimits:      true,
			Backoff:              agentwrap.ExponentialBackoff{Initial: 2 * time.Second, Factor: 2, Max: 30 * time.Second},
		}}
	}
	perm := &agentwrap.PermissionPolicy{
		Default: agentwrap.PermissionActionAllow,
		Tools: map[agentwrap.PermissionTool]agentwrap.PermissionAction{
			agentwrap.PermissionToolEdit:     agentwrap.PermissionActionDeny,
			agentwrap.PermissionToolQuestion: agentwrap.PermissionActionDeny,
		},
		UnsupportedBehavior: agentwrap.PermissionUnsupportedBestEffort,
		Metadata:            map[string]string{"purpose": "read-only quality review"},
	}
	provider, model := splitModel(review.Model)
	req := agentwrap.RunRequest{
		Prompt: prompt, WorkDir: review.ProjectRoot, Provider: agentwrap.ProviderID(provider), Model: agentwrap.ModelID(model), Timeout: opts.TaskTimeout,
		PermissionPolicy: perm,
		Metadata: map[string]string{
			awopencode.MetadataDatabasePath: dbPath,
			"qgauntlet.job":                 job.ID,
			"qgauntlet.attempt":             fmt.Sprintf("%d", attempt),
		},
	}
	run, err := runtime.StartRun(ctx, req)
	if err != nil {
		return ExecuteResult{}, err
	}
	result := ExecuteResult{RuntimeRunID: string(run.ID())}
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		_ = run.Cancel(context.Background())
		return result, err
	}
	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		_ = run.Cancel(context.Background())
		return result, err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	activity := make(chan struct{}, 1)
	eventsDone := make(chan struct{})
	var lastText strings.Builder
	go func() {
		defer close(eventsDone)
		enc := json.NewEncoder(w)
		for ev := range run.Events() {
			ev.Raw = nil
			if t := eventText(ev.Payload); t != "" {
				lastText.Reset()
				lastText.WriteString(t)
			}
			_ = enc.Encode(ev)
			_ = w.Flush()
			select {
			case activity <- struct{}{}:
			default:
			}
		}
	}()
	type waitOutcome struct {
		result agentwrap.RunResult
		err    error
	}
	waitCh := make(chan waitOutcome, 1)
	go func() { r, e := run.Wait(context.Background()); waitCh <- waitOutcome{r, e} }()

	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = 20 * time.Minute
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			cc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = run.Cancel(cc)
			cancel()
			waitChannel(eventsDone, 10*time.Second)
			return result, ctx.Err()
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			cc, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			_ = run.Cancel(cc)
			cancel()
			select {
			case o := <-waitCh:
				waitChannel(eventsDone, 10*time.Second)
				if o.result.Err != nil {
					result.ErrorCategory = string(o.result.Err.Category)
				}
				return result, fmt.Errorf("idle watchdog: no AgentWrap event for %s", idle)
			case <-time.After(15 * time.Second):
				return result, fmt.Errorf("idle watchdog: cancellation did not terminate AgentWrap run within 15s after %s of silence", idle)
			}
		case o := <-waitCh:
			waitChannel(eventsDone, 10*time.Second)
			result.Output = o.result.TerminalOutput
			if strings.TrimSpace(result.Output) == "" {
				result.Output = lastText.String()
			}
			if o.result.RunID != "" {
				result.RuntimeRunID = string(o.result.RunID)
			}
			if o.result.Err != nil {
				result.ErrorCategory = string(o.result.Err.Category)
			}
			if o.err != nil {
				return result, o.err
			}
			if o.result.Err != nil {
				return result, o.result.Err
			}
			if strings.TrimSpace(result.Output) == "" {
				return result, errors.New("AgentWrap run completed without terminal output")
			}
			return result, nil
		}
	}
}

func eventText(payload any) string {
	m, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	if kind, _ := m["event_kind"].(string); kind != "message" {
		return ""
	}
	part, ok := m["part"].(map[string]any)
	if !ok {
		return ""
	}
	t, _ := part["text"].(string)
	return strings.TrimSpace(t)
}

func splitModel(model string) (string, string) {
	if i := strings.Index(model, "/"); i >= 0 {
		return model[:i], model[i+1:]
	}
	return "", model
}

func waitChannel(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

type FakeExecutor struct{}

func (FakeExecutor) Execute(_ context.Context, review Review, job Job, _ int, _ string, _ string, _ string, _ RunOptions) (ExecuteResult, error) {
	if job.Kind == "surface-map-arbiter" {
		m := fakeSurfaceMap()
		b, _ := json.MarshalIndent(m, "", "  ")
		return ExecuteResult{Output: string(b), RuntimeRunID: "fake-map"}, nil
	}
	if job.Kind == "final-arbiter" {
		return ExecuteResult{Output: "# Final Quality Review\n\nFake-run synthesis completed.\n", RuntimeRunID: "fake-final"}, nil
	}
	return ExecuteResult{Output: fmt.Sprintf("# %s\n\nFake result for %s. Target %s.\n", job.ID, job.Kind, review.Target.Commit), RuntimeRunID: "fake-" + job.ID}, nil
}
func fakeSurfaceMap() SurfaceMap {
	return SurfaceMap{
		Surfaces: []Surface{
			{ID: "workspace-discovery", Name: "Workspace discovery", Domain: "workspace", Risk: "normal", Purpose: "Discover and validate the active workspace"},
			{ID: "configuration-resolution", Name: "Configuration resolution", Domain: "workspace", Risk: "high", Purpose: "Resolve configuration and model/runtime choices"},
			{ID: "study-run", Name: "Study run", Domain: "study", Risk: "high", Purpose: "Execute one study analysis and persist evidence"},
			{ID: "study-run-loop", Name: "Study run loop", Domain: "study", Risk: "critical", Purpose: "Schedule and resume many study scopes"},
			{ID: "sprint-code-context", Name: "Sprint code context", Domain: "sprint", Risk: "normal", Purpose: "Build bounded source context"},
			{ID: "sprint-execute", Name: "Sprint execute", Domain: "sprint", Risk: "critical", Purpose: "Execute planned sprint tasks"},
			{ID: "run-terminal-arbitration", Name: "Run terminal arbitration", Domain: "runcontrol", Risk: "critical", Purpose: "Persist exactly one terminal result"},
			{ID: "web-operation-lifecycle", Name: "Web operation lifecycle", Domain: "web", Risk: "high", Purpose: "Expose and observe operations"},
		},
		Seams: []Seam{
			{ID: "sprint-runcontrol-terminal", From: "sprint-execute", To: "run-terminal-arbitration", Contract: "Product and operational terminal truth agree", Risk: "critical"},
			{ID: "web-sprint-lifecycle", From: "web-operation-lifecycle", To: "sprint-execute", Contract: "Web lifecycle reflects execution truth", Risk: "high"},
			{ID: "study-runloop-run", From: "study-run-loop", To: "study-run", Contract: "Scheduler and individual run semantics agree", Risk: "high"},
		},
		Domains: []Domain{
			{ID: "workspace", Name: "Workspace", SurfaceIDs: []string{"workspace-discovery", "configuration-resolution"}},
			{ID: "study", Name: "Study", SurfaceIDs: []string{"study-run", "study-run-loop"}},
			{ID: "sprint", Name: "Sprint", SurfaceIDs: []string{"sprint-code-context", "sprint-execute"}},
			{ID: "runcontrol", Name: "Run control", SurfaceIDs: []string{"run-terminal-arbitration"}},
			{ID: "web", Name: "Web", SurfaceIDs: []string{"web-operation-lifecycle"}},
		},
	}
}

func RunStage(ctx context.Context, reviewRoot string, executor Executor, opts RunOptions) error {
	if opts.Stage == "" {
		return errors.New("stage is required")
	}
	if opts.Parallel <= 0 {
		opts.Parallel = 4
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 15
	}
	if opts.AgentRetries <= 0 {
		opts.AgentRetries = 2
	}
	if opts.TaskTimeout <= 0 {
		opts.TaskTimeout = 90 * time.Minute
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 20 * time.Minute
	}
	r, err := Load(reviewRoot)
	if err != nil {
		return err
	}
	lock, err := AcquireRunLock(r.RuntimeRoot)
	if err != nil {
		return err
	}
	defer ReleaseRunLock(lock)
	if _, err := recoverRunningUnlocked(reviewRoot); err != nil {
		return err
	}
	r, err = Load(reviewRoot)
	if err != nil {
		return err
	}
	if err := VerifyBindings(r, opts.AllowDrift); err != nil {
		return err
	}
	if err := stageReady(r, opts.Stage); err != nil {
		return err
	}
	idx := jobsForStage(r, opts.Stage)
	if len(idx) == 0 {
		return fmt.Errorf("no jobs for stage %q", opts.Stage)
	}
	store := NewStore(reviewRoot, r)
	sem := make(chan struct{}, opts.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, i := range idx {
		job := r.Jobs[i]
		if job.Status == StatusPassed || job.Status == StatusSkipped {
			continue
		}
		if job.Status == StatusFailed && !opts.RetryFailed {
			continue
		}
		if len(job.Attempts) >= opts.MaxAttempts {
			errs = append(errs, fmt.Errorf("%s exhausted %d attempts", job.ID, opts.MaxAttempts))
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := runOne(ctx, store, executor, id, opts); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				mu.Unlock()
			}
		}(job.ID)
	}
	wg.Wait()
	if opts.Stage == StageMapArbiter {
		latest, e := Load(reviewRoot)
		if e == nil {
			arb, _ := FindJob(latest, "map-arbiter")
			if arb != nil && arb.Status == StatusPassed {
				_, e = ExpandFromSurfaceMap(reviewRoot)
			}
		}
		if e != nil {
			errs = append(errs, e)
		}
	}
	if opts.Stage == StageArbiter {
		latest, e := Load(reviewRoot)
		if e == nil {
			e = PromoteFinal(reviewRoot, latest)
		}
		if e != nil {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}
func runOne(ctx context.Context, store *Store, executor Executor, jobID string, opts RunOptions) error {
	snap := store.Snapshot()
	job, err := FindJob(&snap, jobID)
	if err != nil {
		return err
	}
	if err := dependenciesPassed(&snap, *job); err != nil {
		return err
	}
	n := len(job.Attempts) + 1
	jobDir := filepath.Join(snap.ReviewRoot, "jobs", job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return err
	}
	prompt, err := RenderPrompt(snap, *job)
	if err != nil {
		return err
	}
	pr := filepath.ToSlash(filepath.Join("jobs", job.ID, fmt.Sprintf("attempt-%02d-prompt.md", n)))
	rr := filepath.ToSlash(filepath.Join("jobs", job.ID, fmt.Sprintf("attempt-%02d-result.md", n)))
	er := filepath.ToSlash(filepath.Join("jobs", job.ID, fmt.Sprintf("attempt-%02d-events.ndjson", n)))
	pp := filepath.Join(snap.ReviewRoot, filepath.FromSlash(pr))
	rp := filepath.Join(snap.ReviewRoot, filepath.FromSlash(rr))
	ep := filepath.Join(snap.ReviewRoot, filepath.FromSlash(er))
	db := filepath.Join(snap.RuntimeRoot, "jobs", job.ID, fmt.Sprintf("attempt-%02d", n), "opencode.db")
	if err := os.WriteFile(pp, []byte(prompt), 0o644); err != nil {
		return err
	}
	started := time.Now().UTC()
	if err := store.Update(func(r *Review) error {
		j, _ := FindJob(r, jobID)
		j.Status = StatusRunning
		j.Attempts = append(j.Attempts, Attempt{Number: n, Status: StatusRunning, StartedAt: started, LastActivity: started, PromptPath: pr, ResultPath: rr, EventsPath: er, DatabasePath: db})
		return nil
	}); err != nil {
		return err
	}
	res, execErr := executor.Execute(ctx, snap, *job, n, prompt, ep, db, opts)
	finished := time.Now().UTC()
	status := StatusPassed
	errText := ""
	cat := res.ErrorCategory
	if execErr != nil {
		status = StatusFailed
		errText = execErr.Error()
	}
	if strings.TrimSpace(res.Output) != "" {
		if err := os.WriteFile(rp, []byte(res.Output), 0o644); err != nil {
			status = StatusFailed
			errText = "persist result: " + err.Error()
		}
	}
	if status == StatusPassed && job.Kind == "surface-map-arbiter" {
		m, e := ParseSurfaceMap(res.Output)
		if e == nil {
			e = ValidateSurfaceMap(m)
		}
		if e != nil {
			status = StatusFailed
			cat = "invalid_surface_map"
			errText = e.Error()
		}
	}
	if status == StatusPassed {
		_ = os.WriteFile(filepath.Join(jobDir, "result.md"), []byte(res.Output), 0o644)
	}
	if err := store.Update(func(r *Review) error {
		j, _ := FindJob(r, jobID)
		j.Status = status
		if status == StatusPassed {
			j.Result = filepath.ToSlash(filepath.Join("jobs", job.ID, "result.md"))
		}
		a := &j.Attempts[len(j.Attempts)-1]
		a.Status = status
		a.FinishedAt = finished
		a.LastActivity = finished
		a.RuntimeRunID = res.RuntimeRunID
		a.ErrorCategory = cat
		a.Error = errText
		return nil
	}); err != nil {
		return err
	}
	if execErr != nil {
		return execErr
	}
	if status != StatusPassed {
		return errors.New(errText)
	}
	return nil
}
func stageReady(r *Review, stage string) error {
	if r.Baseline.Status != StatusPassed {
		return fmt.Errorf("baseline is %s; run qgauntlet baseline first", r.Baseline.Status)
	}
	if stage == StageMap {
		return nil
	}
	if stage == StageMapArbiter {
		return requireStagePassed(r, StageMap)
	}
	if !r.Expanded {
		return errors.New("surface map has not been expanded; complete map-arbiter first")
	}
	for _, prior := range []string{StageContext, StageReview, StageSeam, StageInvariant, StageTribunal, StageDomain, StageSynth, StageArbiter} {
		if prior == stage {
			return nil
		}
		if err := requireStagePassed(r, prior); err != nil {
			return err
		}
	}
	return fmt.Errorf("unknown stage %q", stage)
}
func requireStagePassed(r *Review, stage string) error {
	idx := jobsForStage(r, stage)
	if len(idx) == 0 {
		return fmt.Errorf("stage %s has no jobs", stage)
	}
	for _, i := range idx {
		if r.Jobs[i].Status != StatusPassed && r.Jobs[i].Status != StatusSkipped {
			return fmt.Errorf("stage %s is incomplete: %s is %s", stage, r.Jobs[i].ID, r.Jobs[i].Status)
		}
	}
	return nil
}
func dependenciesPassed(r *Review, job Job) error {
	for _, id := range job.DependsOn {
		j, err := FindJob(r, id)
		if err != nil {
			return err
		}
		if j.Status != StatusPassed && j.Status != StatusSkipped {
			return fmt.Errorf("dependency %s is %s", id, j.Status)
		}
	}
	return nil
}
