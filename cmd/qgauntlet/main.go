package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Antonio7098/ultraplan-quality-gauntlet/internal/gauntlet"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "bind":
		err = cmdBind(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "baseline":
		err = cmdBaseline(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "next":
		err = cmdNext(os.Args[2:])
	case "prompt":
		err = cmdPrompt(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "recover":
		err = cmdRecover(os.Args[2:])
	case "expand":
		err = cmdExpand(os.Args[2:])
	case "index":
		err = cmdIndex(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Print(`qgauntlet — surface-first UltraPlan correctness/security/quality review

Usage:
  qgauntlet init     --target PATH --workspace PATH [--model openrouter/stealth/ox-alpha] [--opencode PATH]
  qgauntlet bind     [--target PATH] [--workspace PATH] [--opencode PATH]
  qgauntlet doctor
  qgauntlet baseline [--timeout 30m]
  qgauntlet status
  qgauntlet next
  qgauntlet prompt   --id JOB
  qgauntlet run      --stage STAGE [--parallel 6] [--runner agentwrap|fake] [--retry-failed]
  qgauntlet recover
  qgauntlet expand
  qgauntlet index
`)
}
func commonReviewFlag(fs *flag.FlagSet) *string {
	return fs.String("review", "review", "durable tracked review directory")
}
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	target := fs.String("target", "", "implementation checkout")
	workspace := fs.String("workspace", "", "planning workspace checkout")
	model := fs.String("model", "openrouter/stealth/ox-alpha", "OpenCode model slug")
	oc := fs.String("opencode", "", "OpenCode executable")
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.Init(*review, *target, *workspace, *model, *oc)
	if err != nil {
		return err
	}
	fmt.Printf("initialized quality gauntlet: %d discovery jobs\ntarget %s @ %s\nopencode %s\n", len(r.Jobs), r.Bindings.TargetPath, r.Target.Commit, r.Bindings.OpenCodePath)
	return nil
}
func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	target := fs.String("target", "", "new target checkout")
	workspace := fs.String("workspace", "", "new workspace checkout")
	oc := fs.String("opencode", "", "new OpenCode executable")
	drift := fs.Bool("allow-drift", false, "allow different commit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.Bind(*review, *target, *workspace, *oc, *drift)
	if err != nil {
		return err
	}
	fmt.Printf("target=%s\nworkspace=%s\nopencode=%s\n", r.Bindings.TargetPath, r.Bindings.WorkspacePath, r.Bindings.OpenCodePath)
	return nil
}
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rep, err := gauntlet.Doctor(context.Background(), *review)
	fmt.Print(rep.String())
	return err
}
func cmdBaseline(args []string) error {
	fs := flag.NewFlagSet("baseline", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	timeout := fs.Duration("timeout", 30*time.Minute, "timeout per command")
	drift := fs.Bool("allow-drift", false, "allow commit drift")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return gauntlet.RunBaseline(context.Background(), *review, gauntlet.BaselineOptions{CommandTimeout: *timeout, AllowDrift: *drift})
}
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.Load(*review)
	if err != nil {
		return err
	}
	fmt.Print(gauntlet.StatusText(r))
	return nil
}
func cmdNext(args []string) error {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.Load(*review)
	if err != nil {
		return err
	}
	fmt.Println(gauntlet.NextStage(r))
	return nil
}
func cmdPrompt(args []string) error {
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	id := fs.String("id", "", "job id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.Load(*review)
	if err != nil {
		return err
	}
	j, err := gauntlet.FindJob(r, *id)
	if err != nil {
		return err
	}
	p, err := gauntlet.RenderPrompt(*r, *j)
	if err != nil {
		return err
	}
	fmt.Print(p)
	return nil
}
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	stage := fs.String("stage", "", "stage")
	parallel := fs.Int("parallel", 6, "parallel jobs")
	runner := fs.String("runner", "agentwrap", "agentwrap or fake")
	max := fs.Int("max-attempts", 15, "outer attempts")
	agentRetries := fs.Int("agent-retries", 2, "AgentWrap retries")
	idle := fs.Duration("idle-timeout", 20*time.Minute, "event silence watchdog")
	task := fs.Duration("task-timeout", 90*time.Minute, "absolute attempt timeout")
	retry := fs.Bool("retry-failed", false, "retry failed jobs")
	drift := fs.Bool("allow-drift", false, "allow commit drift")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var ex gauntlet.Executor
	switch *runner {
	case "agentwrap":
		ex = gauntlet.AgentwrapExecutor{}
	case "fake":
		ex = gauntlet.FakeExecutor{}
	default:
		return fmt.Errorf("unknown runner %q", *runner)
	}
	return gauntlet.RunStage(context.Background(), *review, ex, gauntlet.RunOptions{Stage: *stage, Parallel: *parallel, MaxAttempts: *max, IdleTimeout: *idle, TaskTimeout: *task, AgentRetries: *agentRetries, RetryFailed: *retry, AllowDrift: *drift})
}
func cmdRecover(args []string) error {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	n, err := gauntlet.RecoverRunning(*review)
	if err != nil {
		return err
	}
	fmt.Printf("recovered %d stale running jobs\n", n)
	return nil
}
func cmdExpand(args []string) error {
	fs := flag.NewFlagSet("expand", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.ExpandFromSurfaceMap(*review)
	if err != nil {
		return err
	}
	fmt.Printf("expanded: %d surfaces, %d seams, %d total jobs\n", len(r.SurfaceMap.Surfaces), len(r.SurfaceMap.Seams), len(r.Jobs))
	return nil
}
func cmdIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	review := commonReviewFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := gauntlet.Load(*review)
	if err != nil {
		return err
	}
	p, err := gauntlet.BuildIndex(*review, r)
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(p)
	fmt.Println(abs)
	return nil
}
