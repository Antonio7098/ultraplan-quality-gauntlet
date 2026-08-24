package gauntlet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type BaselineOptions struct {
	CommandTimeout time.Duration
	AllowDrift     bool
}
type commandSpec struct {
	Name     string
	Args     []string
	Optional bool
}

func RunBaseline(ctx context.Context, reviewRoot string, opts BaselineOptions) error {
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = 30 * time.Minute
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
	if err := VerifyBindings(r, opts.AllowDrift); err != nil {
		return err
	}
	store := NewStore(reviewRoot, r)
	started := time.Now().UTC()
	if err := store.Update(func(r *Review) error {
		r.Baseline = BaselineState{Status: StatusRunning, StartedAt: started}
		return nil
	}); err != nil {
		return err
	}
	baseDir := filepath.Join(reviewRoot, "baseline")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return err
	}
	var results []BaselineCommand
	for _, spec := range baselineCommands() {
		res := runBaselineCommand(ctx, r.Bindings.TargetPath, baseDir, spec, opts.CommandTimeout)
		results = append(results, res)
		_ = store.Update(func(r *Review) error { r.Baseline.Commands = append([]BaselineCommand(nil), results...); return nil })
	}
	finished := time.Now().UTC()
	if err := store.Update(func(r *Review) error {
		r.Baseline.Status = StatusPassed
		r.Baseline.StartedAt = started
		r.Baseline.FinishedAt = finished
		r.Baseline.Commands = results
		return nil
	}); err != nil {
		return err
	}
	return writeBaselineSummary(baseDir, results)
}
func baselineCommands() []commandSpec {
	cmds := []commandSpec{{"go-test", []string{"go", "test", "./..."}, false}, {"go-test-race", []string{"go", "test", "-race", "./..."}, false}, {"go-vet", []string{"go", "vet", "./..."}, false}, {"go-test-cover", []string{"go", "test", "-cover", "./..."}, false}, {"go-list", []string{"go", "list", "./..."}, false}}
	for _, o := range []commandSpec{{"staticcheck", []string{"staticcheck", "./..."}, true}, {"govulncheck", []string{"govulncheck", "./..."}, true}, {"gosec", []string{"gosec", "./..."}, true}, {"golangci-lint", []string{"golangci-lint", "run", "./..."}, true}} {
		if _, err := exec.LookPath(o.Args[0]); err == nil {
			cmds = append(cmds, o)
		}
	}
	return cmds
}
func runBaselineCommand(parent context.Context, workDir, baseDir string, spec commandSpec, timeout time.Duration) BaselineCommand {
	started := time.Now().UTC()
	result := BaselineCommand{Name: spec.Name, Command: spec.Args, Status: StatusRunning, StartedAt: started}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Args[0], spec.Args[1:]...)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	finished := time.Now().UTC()
	result.FinishedAt = finished
	result.Duration = finished.Sub(started)
	outName := spec.Name + ".txt"
	result.OutputPath = filepath.ToSlash(filepath.Join("baseline", outName))
	_ = os.WriteFile(filepath.Join(baseDir, outName), output, 0o644)
	if err == nil {
		result.Status = StatusPassed
		return result
	}
	result.Status = StatusFailed
	result.Error = err.Error()
	if ctx.Err() != nil {
		result.Error = ctx.Err().Error()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		result.ExitCode = ee.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}
func writeBaselineSummary(baseDir string, results []BaselineCommand) error {
	v := struct {
		Commands []BaselineCommand `json:"commands"`
		Notes    []string          `json:"notes"`
	}{results, []string{"A failing baseline command is evidence for reviewers, not an automatic gauntlet failure.", "Optional analyzers are recorded only when present on PATH."}}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(baseDir, "summary.json"), append(b, '\n'), 0o644)
}
func BaselineText(r *Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "baseline: %s\n", r.Baseline.Status)
	for _, c := range r.Baseline.Commands {
		fmt.Fprintf(&b, "  %-20s %-7s exit=%d duration=%s\n", c.Name, c.Status, c.ExitCode, c.Duration.Round(time.Second))
	}
	return b.String()
}
