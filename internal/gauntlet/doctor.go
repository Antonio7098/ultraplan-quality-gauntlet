package gauntlet

import (
	"context"
	"fmt"
	"github.com/Antonio7098/agentwrap"
	awopencode "github.com/Antonio7098/agentwrap/opencode"
	"os/exec"
	"strings"
	"time"
)

type DoctorReport struct {
	OpenCodePath, OpenCodeVersion, GitVersion, GoVersion, Model string
	ModelFound                                                  bool
	ModelCount                                                  int
}

func Doctor(ctx context.Context, reviewRoot string) (DoctorReport, error) {
	r, err := Load(reviewRoot)
	if err != nil {
		return DoctorReport{}, err
	}
	if err := VerifyBindings(r, false); err != nil {
		return DoctorReport{}, err
	}
	rep := DoctorReport{OpenCodePath: r.Bindings.OpenCodePath, Model: r.Model}
	rep.OpenCodeVersion = commandVersion(r.Bindings.OpenCodePath, "--version")
	rep.GitVersion = commandVersion("git", "--version")
	rep.GoVersion = commandVersion("go", "version")
	rt := awopencode.NewRuntime(awopencode.WithExecutable(r.Bindings.OpenCodePath), awopencode.WithSnapshots(false))
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	models, err := rt.ListModels(c, agentwrap.ModelsRequest{WorkDir: r.ProjectRoot})
	if err != nil {
		return rep, fmt.Errorf("AgentWrap/OpenCode model listing failed: %w", err)
	}
	rep.ModelCount = len(models)
	for _, m := range models {
		full := string(m.ID)
		if m.Provider != "" {
			full = string(m.Provider) + "/" + string(m.ID)
		}
		if full == r.Model {
			rep.ModelFound = true
			break
		}
	}
	if !rep.ModelFound {
		return rep, fmt.Errorf("configured model %q was not reported by OpenCode through AgentWrap", r.Model)
	}
	return rep, nil
}
func (r DoctorReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "opencode: %s (%s)\ngo: %s\ngit: %s\nmodel: %s found=%t (%d models enumerated)\n", r.OpenCodePath, r.OpenCodeVersion, r.GoVersion, r.GitVersion, r.Model, r.ModelFound, r.ModelCount)
	return b.String()
}
func commandVersion(name, arg string) string {
	b, err := exec.Command(name, arg).CombinedOutput()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return strings.TrimSpace(string(b))
}
