package gauntlet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const stateName = "state.json"

// Store serializes in-process state changes and persists them atomically.
type Store struct {
	mu   sync.Mutex
	root string
	r    *Review
}

func NewStore(root string, r *Review) *Store { return &Store{root: root, r: r} }

func (s *Store) Update(fn func(*Review) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(s.r); err != nil {
		return err
	}
	s.r.UpdatedAt = time.Now().UTC()
	return saveAtomic(filepath.Join(s.root, stateName), s.r)
}

func (s *Store) Snapshot() Review {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.r)
	var out Review
	_ = json.Unmarshal(b, &out)
	return out
}

func Init(reviewRoot, target, workspace, model, opencodePath string) (*Review, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("target path is required")
	}
	if model == "" {
		model = "openrouter/stealth/ox-alpha"
	}
	resolvedOC, err := resolveExecutable(opencodePath)
	if err != nil {
		return nil, err
	}
	targetPath, targetSnap, err := inspectRepo("ultraplan-go", target)
	if err != nil {
		return nil, err
	}
	workspacePath := ""
	workspaceSnap := FrozenRepo{}
	if workspace != "" {
		workspacePath, workspaceSnap, err = inspectRepo("ultraplan-workspace", workspace)
		if err != nil {
			return nil, err
		}
	}
	absReview, err := filepath.Abs(reviewRoot)
	if err != nil {
		return nil, err
	}
	projectRoot := filepath.Dir(absReview)
	absRuntime := filepath.Join(projectRoot, ".quality-gauntlet")
	if err := os.MkdirAll(absReview, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absRuntime, 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(absReview, stateName)); err == nil {
		return nil, fmt.Errorf("review already initialized at %s", absReview)
	}
	now := time.Now().UTC()
	r := &Review{
		Version:      StateVersion,
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        model,
		Target:       targetSnap,
		Workspace:    workspaceSnap,
		Bindings:     Bindings{TargetPath: targetPath, WorkspacePath: workspacePath, OpenCodePath: resolvedOC},
		Baseline:     BaselineState{Status: StatusPending},
		Jobs:         InitialJobs(),
		ProjectRoot:  projectRoot,
		ReviewRoot:   absReview,
		RuntimeRoot:  absRuntime,
		AgentwrapSHA: "3d1e4cbb6e036bc5cd288ffcb9423bbd0bf4b1b9",
	}
	if err := saveAtomic(filepath.Join(absReview, stateName), r); err != nil {
		return nil, err
	}
	return r, nil
}

func Load(reviewRoot string) (*Review, error) {
	path := filepath.Join(reviewRoot, stateName)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Review
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if r.Version != StateVersion {
		return nil, fmt.Errorf("unsupported state version %d", r.Version)
	}
	abs, err := filepath.Abs(reviewRoot)
	if err != nil {
		return nil, err
	}
	// Review/project/runtime roots are location-relative and intentionally rebase
	// on load. This makes a copied in-progress review self-relocating.
	r.ReviewRoot = abs
	r.ProjectRoot = filepath.Dir(abs)
	r.RuntimeRoot = filepath.Join(r.ProjectRoot, ".quality-gauntlet")
	return &r, nil
}

func Bind(reviewRoot, target, workspace, opencodePath string, allowDrift bool) (*Review, error) {
	r, err := Load(reviewRoot)
	if err != nil {
		return nil, err
	}
	if target != "" {
		p, snap, err := inspectRepo(r.Target.Label, target)
		if err != nil {
			return nil, err
		}
		if !allowDrift && r.Target.Commit != "" && snap.Commit != r.Target.Commit {
			return nil, fmt.Errorf("target commit %s does not match frozen commit %s", snap.Commit, r.Target.Commit)
		}
		r.Bindings.TargetPath = p
		if r.Target.Commit != "" && snap.Commit != r.Target.Commit {
			snap.Dirty = snap.Dirty || r.Target.Dirty
			r.Target = snap
		}
	}
	if workspace != "" {
		p, snap, err := inspectRepo(r.Workspace.Label, workspace)
		if err != nil {
			return nil, err
		}
		if !allowDrift && r.Workspace.Commit != "" && snap.Commit != r.Workspace.Commit {
			return nil, fmt.Errorf("workspace commit %s does not match frozen commit %s", snap.Commit, r.Workspace.Commit)
		}
		r.Bindings.WorkspacePath = p
		if r.Workspace.Commit != "" && snap.Commit != r.Workspace.Commit {
			snap.Dirty = snap.Dirty || r.Workspace.Dirty
			r.Workspace = snap
		}
	}
	if opencodePath != "" {
		p, err := resolveExecutable(opencodePath)
		if err != nil {
			return nil, err
		}
		r.Bindings.OpenCodePath = p
	}
	r.UpdatedAt = time.Now().UTC()
	if err := saveAtomic(filepath.Join(reviewRoot, stateName), r); err != nil {
		return nil, err
	}
	return r, nil
}

func VerifyBindings(r *Review, allowDrift bool) error {
	checks := []struct {
		frozen FrozenRepo
		path   string
	}{{r.Target, r.Bindings.TargetPath}}
	if r.Workspace.Commit != "" {
		checks = append(checks, struct {
			frozen FrozenRepo
			path   string
		}{r.Workspace, r.Bindings.WorkspacePath})
	}
	for _, c := range checks {
		if c.path == "" {
			return fmt.Errorf("%s is not bound; run qgauntlet bind", c.frozen.Label)
		}
		_, snap, err := inspectRepo(c.frozen.Label, c.path)
		if err != nil {
			return err
		}
		if !allowDrift && c.frozen.Commit != "" && snap.Commit != c.frozen.Commit {
			return fmt.Errorf("%s moved from frozen commit %s to %s; rebind the matching checkout or pass --allow-drift", c.frozen.Label, c.frozen.Commit, snap.Commit)
		}
	}
	if r.Bindings.OpenCodePath == "" {
		return errors.New("OpenCode executable is not bound")
	}
	if st, err := os.Stat(r.Bindings.OpenCodePath); err != nil || st.IsDir() {
		return fmt.Errorf("OpenCode executable %q is unavailable", r.Bindings.OpenCodePath)
	}
	return nil
}

func RecoverRunning(reviewRoot string) (int, error) {
	r, err := Load(reviewRoot)
	if err != nil {
		return 0, err
	}
	lock, err := AcquireRunLock(r.RuntimeRoot)
	if err != nil {
		return 0, err
	}
	defer ReleaseRunLock(lock)
	return recoverRunningUnlocked(reviewRoot)
}

func recoverRunningUnlocked(reviewRoot string) (int, error) {
	r, err := Load(reviewRoot)
	if err != nil {
		return 0, err
	}
	count := 0
	now := time.Now().UTC()
	for i := range r.Jobs {
		if r.Jobs[i].Status != StatusRunning {
			continue
		}
		count++
		r.Jobs[i].Status = StatusFailed
		if len(r.Jobs[i].Attempts) > 0 {
			a := &r.Jobs[i].Attempts[len(r.Jobs[i].Attempts)-1]
			if a.Status == StatusRunning {
				a.Status = StatusFailed
				a.FinishedAt = now
				a.LastActivity = now
				a.ErrorCategory = "orchestrator_interrupted"
				a.Error = "previous orchestrator exited while this task was running; AgentWrap process ownership was lost"
			}
		}
	}
	if count == 0 {
		return 0, nil
	}
	r.UpdatedAt = now
	return count, saveAtomic(filepath.Join(reviewRoot, stateName), r)
}

func saveAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + fmt.Sprintf(".tmp-%d", os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	ok = true
	return nil
}

func resolveExecutable(path string) (string, error) {
	if path == "" {
		p, err := exec.LookPath("opencode")
		if err != nil {
			return "", fmt.Errorf("opencode not found on PATH; pass --opencode /absolute/path/to/opencode: %w", err)
		}
		path = p
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("opencode executable: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("opencode path %q is a directory", abs)
	}
	return abs, nil
}

func inspectRepo(label, path string) (string, FrozenRepo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", FrozenRepo{}, err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", FrozenRepo{}, fmt.Errorf("%s path: %w", label, err)
	}
	sha, err := gitOutput(abs, "rev-parse", "HEAD")
	if err != nil {
		return "", FrozenRepo{}, fmt.Errorf("%s is not a readable git checkout: %w", label, err)
	}
	status, err := gitOutput(abs, "status", "--porcelain")
	if err != nil {
		return "", FrozenRepo{}, err
	}
	return abs, FrozenRepo{Label: label, Commit: strings.TrimSpace(sha), Dirty: strings.TrimSpace(status) != ""}, nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// AcquireRunLock prevents two orchestrators from mutating one review concurrently.
func AcquireRunLock(runtimeRoot string) (*os.File, error) {
	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(runtimeRoot, "orchestrator.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another qgauntlet orchestrator appears to be running: %w", err)
	}
	return f, nil
}

func ReleaseRunLock(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func FindJob(r *Review, id string) (*Job, error) {
	for i := range r.Jobs {
		if r.Jobs[i].ID == id {
			return &r.Jobs[i], nil
		}
	}
	return nil, fmt.Errorf("unknown job %q", id)
}

func jobsForStage(r *Review, stage string) []int {
	var idx []int
	for i := range r.Jobs {
		if r.Jobs[i].Stage == stage {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(i, j int) bool { return r.Jobs[idx[i]].ID < r.Jobs[idx[j]].ID })
	return idx
}
