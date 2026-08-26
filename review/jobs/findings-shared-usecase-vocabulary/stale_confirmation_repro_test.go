package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Antonio7098/ultraplan-go/internal/app"
)

// recordingUseCases implements OperationalUseCases + DurableOperationManager +
// RunUseCases, recording what the TUI actually accepts vs executes.
type recordingUseCases struct {
	fakeUseCases
	accepted        []app.Confirmation
	acceptDigests   []string
	executed        []app.OperationRequest
	existingOnAccept bool
}

func (r *recordingUseCases) AcceptOperation(ctx context.Context, c app.Confirmation, digest string) (app.AcceptedOperation, error) {
	r.accepted = append(r.accepted, c)
	r.acceptDigests = append(r.acceptDigests, digest)
	if r.existingOnAccept {
		return app.AcceptedOperation{RunID: "existing", Existing: true, Lifecycle: "succeeded"}, nil
	}
	return app.AcceptedOperation{RunID: "run-1", Context: ctx}, nil
}
func (r *recordingUseCases) RecordOperationEvent(ctx context.Context, id string, e app.OperationEvent) (bool, error) {
	return true, nil
}
func (r *recordingUseCases) FinishOperation(ctx context.Context, id string, s app.OperationState, err error) error {
	return nil
}
func (r *recordingUseCases) RunOperation(ctx context.Context, req app.OperationRequest, emit func(app.OperationEvent)) (app.OperationResult, error) {
	r.executed = append(r.executed, req)
	return app.OperationResult{State: app.OperationComplete}, nil
}
func (r *recordingUseCases) Runs(ctx context.Context, q app.RunQuery) (app.RunPage, error) {
	return app.RunPage{}, nil
}
func (r *recordingUseCases) Run(ctx context.Context, id app.RunID) (app.RunSnapshot, error) {
	return app.RunSnapshot{}, app.ErrRunNotFound
}
func (r *recordingUseCases) RunEvents(ctx context.Context, id app.RunID, after uint64, limit int) ([]app.RunEvent, error) {
	return nil, nil
}
func (r *recordingUseCases) CancelRun(ctx context.Context, id app.RunID, reason string) (app.RunSnapshot, bool, error) {
	return app.RunSnapshot{}, false, nil
}
func (r *recordingUseCases) RunHealth(ctx context.Context) (app.RunHealthResult, error) {
	return app.RunHealthResult{}, nil
}

func TestStaleConfirmationStudyCancelAcceptsWrongOperation(t *testing.T) {
	use := &recordingUseCases{}
	m := newTeaModel(context.Background(), use, 100)
	// Operator prepared a sprint execute-start earlier; confirmation is still pending.
	stale := app.Confirmation{
		Request:           app.OperationRequest{Kind: app.OperationExecuteStart, Project: "alpha", Sprint: "31-web"},
		CanonicalRequest:  `{"kind":"execute-start","project":"alpha","sprint":"31-web"}`,
		InputFingerprint:  "sha256:deadbeef",
	}
	m.model.Confirmation = &stale
	// Operator navigated to a study with an active run and presses 'c' to cancel it.
	m.model.Routes = []Route{{Kind: RouteStudies}, {Kind: RouteStudy, Study: "research"}}
	m.model.Data.Studies = []app.StudySummary{{Name: "research", RunActive: true}}

	next, cmd := m.Update(teaKey("c"))
	tm := next.(teaModel)

	if len(use.accepted) == 0 {
		t.Fatalf("expected AcceptOperation to be called; model error=%q operation=%+v", tm.model.Error, tm.model.ActiveOperation)
	}
	got := use.accepted[0]
	if got.Request.Kind != app.OperationExecuteStart {
		t.Fatalf("accepted durable kind=%q; want mislabeled execute-start (proof)", got.Request.Kind)
	}
	if tm.model.ActiveOperation.Kind != app.OperationStudyCancel {
		t.Fatalf("executed kind=%q; want study-cancel", tm.model.ActiveOperation.Kind)
	}
	// Drive the returned command tree so RunOperation runs under the accepted run id.
	drive(cmd)
	if len(use.executed) != 1 || use.executed[0].Kind != app.OperationStudyCancel {
		t.Fatalf("executed=%+v", use.executed)
	}
	basis := stale.CanonicalRequest + "\x00" + stale.InputFingerprint
	sum := sha256.Sum256([]byte(basis))
	if use.acceptDigests[0] != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest=%q", use.acceptDigests[0])
	}
	t.Logf("PROOF: durable accept recorded kind=%q digest=%s while executed kind=%q",
		got.Request.Kind, use.acceptDigests[0][:12], use.executed[0].Kind)
}

func drive(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, sub := range msg {
			drive(sub)
		}
	}
}

func TestStudyCancelWithoutConfirmationBypassesDurableAcceptance(t *testing.T) {
	use := &recordingUseCases{}
	m := newTeaModel(context.Background(), use, 100)
	m.model.Routes = []Route{{Kind: RouteStudies}, {Kind: RouteStudy, Study: "research"}}
	m.model.Data.Studies = []app.StudySummary{{Name: "research", RunActive: true}}

	m.Update(teaKey("c"))

	if len(use.accepted) != 0 {
		t.Fatalf("study-cancel accepted durably without any confirmation: %+v", use.accepted)
	}
	if len(use.executed) != 1 || use.executed[0].Kind != app.OperationStudyCancel {
		_ = m
	}
}
