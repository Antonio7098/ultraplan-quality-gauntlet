package gauntlet

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func NextStage(r *Review) string {
	if r.Baseline.Status != StatusPassed {
		return "baseline"
	}
	for _, stage := range StageOrder[1:] {
		idx := jobsForStage(r, stage)
		if len(idx) == 0 {
			if !r.Expanded && stage != StageMap && stage != StageMapArbiter {
				return StageMapArbiter
			}
			continue
		}
		for _, i := range idx {
			if r.Jobs[i].Status != StatusPassed && r.Jobs[i].Status != StatusSkipped {
				return stage
			}
		}
	}
	return "complete"
}
func StatusText(r *Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model: %s\n", r.Model)
	fmt.Fprintf(&b, "target: %s @ %s dirty_at_init=%t\n", r.Bindings.TargetPath, shortSHA(r.Target.Commit), r.Target.Dirty)
	if r.Workspace.Commit != "" {
		fmt.Fprintf(&b, "workspace: %s @ %s dirty_at_init=%t\n", r.Bindings.WorkspacePath, shortSHA(r.Workspace.Commit), r.Workspace.Dirty)
	}
	fmt.Fprintf(&b, "opencode: %s\nbaseline: %s\n", r.Bindings.OpenCodePath, r.Baseline.Status)
	for _, stage := range StageOrder[1:] {
		idx := jobsForStage(r, stage)
		if len(idx) == 0 {
			continue
		}
		c := map[string]int{}
		for _, i := range idx {
			c[r.Jobs[i].Status]++
		}
		fmt.Fprintf(&b, "%-15s %3d total | %3d passed %3d running %3d failed %3d pending\n", stage, len(idx), c[StatusPassed], c[StatusRunning], c[StatusFailed], c[StatusPending])
	}
	if r.SurfaceMap != nil {
		fmt.Fprintf(&b, "surfaces: %d | seams: %d | domains: %d\n", len(r.SurfaceMap.Surfaces), len(r.SurfaceMap.Seams), len(r.SurfaceMap.Domains))
	}
	fmt.Fprintf(&b, "next: %s\n", NextStage(r))
	return b.String()
}
func BuildIndex(reviewRoot string, r *Review) (string, error) {
	var b strings.Builder
	b.WriteString("# UltraPlan Quality Gauntlet — Review Index\n\n")
	fmt.Fprintf(&b, "- Target commit: `%s`\n", r.Target.Commit)
	if r.Workspace.Commit != "" {
		fmt.Fprintf(&b, "- Workspace commit: `%s`\n", r.Workspace.Commit)
	}
	fmt.Fprintf(&b, "- Model: `%s`\n- Generated: %s\n\n## Progress\n\n```text\n%s```\n\n", r.Model, time.Now().UTC().Format(time.RFC3339), StatusText(r))
	if r.SurfaceMap != nil {
		b.WriteString("## Product surfaces\n\n")
		surfaces := append([]Surface(nil), r.SurfaceMap.Surfaces...)
		sort.Slice(surfaces, func(i, j int) bool { return surfaces[i].ID < surfaces[j].ID })
		for _, s := range surfaces {
			fmt.Fprintf(&b, "- **%s** — %s (`%s`, %s)\n", s.ID, s.Name, s.Domain, s.Risk)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Jobs\n\n")
	jobs := append([]Job(nil), r.Jobs...)
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Stage == jobs[j].Stage {
			return jobs[i].ID < jobs[j].ID
		}
		return stageIndex(jobs[i].Stage) < stageIndex(jobs[j].Stage)
	})
	for _, j := range jobs {
		result := ""
		if j.Result != "" {
			result = fmt.Sprintf(" — [%s](%s)", filepath.Base(j.Result), filepath.ToSlash(j.Result))
		}
		fmt.Fprintf(&b, "- `%s` **%s** — %s%s\n", j.Stage, j.ID, j.Status, result)
	}
	path := filepath.Join(reviewRoot, "index.md")
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}
func PromoteFinal(reviewRoot string, r *Review) error {
	j, err := FindJob(r, "arbiter")
	if err != nil {
		return err
	}
	if j.Status != StatusPassed || j.Result == "" {
		return fmt.Errorf("arbiter has not completed")
	}
	b, err := os.ReadFile(filepath.Join(reviewRoot, filepath.FromSlash(j.Result)))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(reviewRoot, "final.md"), b, 0o644)
}
func stageIndex(stage string) int {
	for i, s := range StageOrder {
		if s == stage {
			return i
		}
	}
	return 999
}
func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
