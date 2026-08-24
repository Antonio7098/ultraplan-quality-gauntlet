package gauntlet

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewerLensesRiskWeighting(t *testing.T) {
	cases := map[string]int{"critical": 6, "high": 5, "normal": 4, "low": 2}
	for risk, want := range cases {
		if got := len(reviewerLenses(risk)); got != want {
			t.Fatalf("risk %s got %d reviewers want %d", risk, got, want)
		}
	}
}

func TestParseAndValidateSurfaceMap(t *testing.T) {
	m := fakeSurfaceMap()
	b, _ := json.MarshalIndent(m, "", "  ")
	got, err := ParseSurfaceMap("```json\n" + string(b) + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSurfaceMap(got); err != nil {
		t.Fatal(err)
	}
	if len(got.Surfaces) != 8 {
		t.Fatalf("surfaces=%d", len(got.Surfaces))
	}
}

func TestRecoverRunning(t *testing.T) {
	review, target, workspace, oc := fixture(t)
	r, err := Init(review, target, workspace, "", oc)
	if err != nil {
		t.Fatal(err)
	}
	r.Jobs[0].Status = StatusRunning
	r.Jobs[0].Attempts = []Attempt{{Number: 1, Status: StatusRunning, StartedAt: time.Now().UTC()}}
	if err := saveAtomic(filepath.Join(review, stateName), r); err != nil {
		t.Fatal(err)
	}
	n, err := RecoverRunning(review)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered=%d", n)
	}
	r, _ = Load(review)
	if r.Jobs[0].Status != StatusFailed || r.Jobs[0].Attempts[0].ErrorCategory != "orchestrator_interrupted" {
		t.Fatalf("unexpected recovered job: %#v", r.Jobs[0])
	}
}

func TestLoadRebasesReviewRootsAfterCopy(t *testing.T) {
	review, target, workspace, oc := fixture(t)
	old, err := Init(review, target, workspace, "", oc)
	if err != nil {
		t.Fatal(err)
	}
	newProject := t.TempDir()
	newReview := filepath.Join(newProject, "review")
	if err := os.MkdirAll(newReview, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(review, stateName))
	if err := os.WriteFile(filepath.Join(newReview, stateName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := Load(newReview)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ReviewRoot != newReview || moved.ProjectRoot != newProject || moved.RuntimeRoot != filepath.Join(newProject, ".quality-gauntlet") {
		t.Fatalf("roots not rebased old=%s moved=%#v", old.ReviewRoot, moved)
	}
}

func TestBindAllowsRelocationAtFrozenCommit(t *testing.T) {
	review, target, workspace, oc := fixture(t)
	r, err := Init(review, target, workspace, "", oc)
	if err != nil {
		t.Fatal(err)
	}
	newTarget := cloneLocal(t, target)
	newWorkspace := cloneLocal(t, workspace)
	bound, err := Bind(review, newTarget, newWorkspace, oc, false)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Target.Commit != r.Target.Commit || bound.Bindings.TargetPath == r.Bindings.TargetPath {
		t.Fatalf("binding did not relocate safely: %#v", bound.Bindings)
	}
}

func TestFullFakePipeline(t *testing.T) {
	review, target, workspace, oc := fixture(t)
	r, err := Init(review, target, workspace, "", oc)
	if err != nil {
		t.Fatal(err)
	}
	r.Baseline.Status = StatusPassed
	if err := saveAtomic(filepath.Join(review, stateName), r); err != nil {
		t.Fatal(err)
	}
	opts := RunOptions{Parallel: 12, RetryFailed: true, MaxAttempts: 3, IdleTimeout: time.Second, TaskTimeout: time.Second, AgentRetries: 1}
	for _, stage := range []string{StageMap, StageMapArbiter, StageContext, StageReview, StageSeam, StageInvariant, StageTribunal, StageDomain, StageSynth, StageArbiter} {
		opts.Stage = stage
		if err := RunStage(context.Background(), review, FakeExecutor{}, opts); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	r, err = Load(review)
	if err != nil {
		t.Fatal(err)
	}
	if NextStage(r) != "complete" {
		t.Fatalf("next=%s\n%s", NextStage(r), StatusText(r))
	}
	if !r.Expanded || r.SurfaceMap == nil || len(r.Jobs) < 60 {
		t.Fatalf("expansion incomplete: %t jobs=%d", r.Expanded, len(r.Jobs))
	}
	if _, err := os.Stat(filepath.Join(review, "final.md")); err != nil {
		t.Fatalf("final missing: %v", err)
	}
}

func TestRunRequiresBaseline(t *testing.T) {
	review, target, workspace, oc := fixture(t)
	if _, err := Init(review, target, workspace, "", oc); err != nil {
		t.Fatal(err)
	}
	if err := RunStage(context.Background(), review, FakeExecutor{}, RunOptions{Stage: StageMap, Parallel: 1, MaxAttempts: 1}); err == nil {
		t.Fatal("expected baseline gate")
	}
}

func fixture(t *testing.T) (review, target, workspace, oc string) {
	t.Helper()
	root := t.TempDir()
	target = filepath.Join(root, "target")
	workspace = filepath.Join(root, "workspace")
	initGit(t, target)
	initGit(t, workspace)
	oc = filepath.Join(root, "opencode")
	if err := os.WriteFile(oc, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	review = filepath.Join(root, "review")
	return
}
func initGit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"git", "init", "-q", "-b", "main"}, {"git", "config", "user.name", "Test"}, {"git", "config", "user.email", "test@example.invalid"}} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, b)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644)
	for _, args := range [][]string{{"git", "add", "."}, {"git", "commit", "-q", "-m", "fixture"}} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, b)
		}
	}
}
func cloneLocal(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clone")
	c := exec.Command("git", "clone", "-q", src, dst)
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, b)
	}
	return dst
}
