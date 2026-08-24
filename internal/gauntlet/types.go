package gauntlet

import "time"

const StateVersion = 1

const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

const (
	StageMap        = "map"
	StageMapArbiter = "map-arbiter"
	StageContext    = "context"
	StageReview     = "surface-review"
	StageSeam       = "seam"
	StageInvariant  = "invariant"
	StageTribunal   = "tribunal"
	StageDomain     = "domain"
	StageSynth      = "synth"
	StageArbiter    = "arbiter"
)

var StageOrder = []string{
	"baseline",
	StageMap,
	StageMapArbiter,
	StageContext,
	StageReview,
	StageSeam,
	StageInvariant,
	StageTribunal,
	StageDomain,
	StageSynth,
	StageArbiter,
}

type FrozenRepo struct {
	Label  string `json:"label"`
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
}

type Bindings struct {
	TargetPath    string `json:"target_path"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	OpenCodePath  string `json:"opencode_path"`
}

type BaselineCommand struct {
	Name       string        `json:"name"`
	Command    []string      `json:"command"`
	Status     string        `json:"status"`
	ExitCode   int           `json:"exit_code"`
	StartedAt  time.Time     `json:"started_at,omitempty"`
	FinishedAt time.Time     `json:"finished_at,omitempty"`
	Duration   time.Duration `json:"duration,omitempty"`
	OutputPath string        `json:"output_path,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type BaselineState struct {
	Status     string            `json:"status"`
	StartedAt  time.Time         `json:"started_at,omitempty"`
	FinishedAt time.Time         `json:"finished_at,omitempty"`
	Commands   []BaselineCommand `json:"commands,omitempty"`
}

type Attempt struct {
	Number        int       `json:"number"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	LastActivity  time.Time `json:"last_activity,omitempty"`
	RuntimeRunID  string    `json:"runtime_run_id,omitempty"`
	ErrorCategory string    `json:"error_category,omitempty"`
	Error         string    `json:"error,omitempty"`
	PromptPath    string    `json:"prompt_path,omitempty"`
	ResultPath    string    `json:"result_path,omitempty"`
	EventsPath    string    `json:"events_path,omitempty"`
	DatabasePath  string    `json:"database_path,omitempty"`
}

type Job struct {
	ID        string    `json:"id"`
	Stage     string    `json:"stage"`
	Agent     string    `json:"agent"`
	Kind      string    `json:"kind"`
	Lens      string    `json:"lens,omitempty"`
	SurfaceID string    `json:"surface_id,omitempty"`
	SeamID    string    `json:"seam_id,omitempty"`
	DomainID  string    `json:"domain_id,omitempty"`
	Status    string    `json:"status"`
	DependsOn []string  `json:"depends_on,omitempty"`
	Attempts  []Attempt `json:"attempts,omitempty"`
	Result    string    `json:"result,omitempty"`
}

type Surface struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Domain          string   `json:"domain"`
	Risk            string   `json:"risk"`
	Purpose         string   `json:"purpose"`
	Entrypoints     []string `json:"entrypoints,omitempty"`
	Paths           []string `json:"paths,omitempty"`
	State           []string `json:"state,omitempty"`
	TrustBoundaries []string `json:"trust_boundaries,omitempty"`
	Dependencies    []string `json:"dependencies,omitempty"`
}

type Seam struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Contract string `json:"contract"`
	Risk     string `json:"risk,omitempty"`
}

type Domain struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SurfaceIDs []string `json:"surface_ids"`
}

type SurfaceMap struct {
	Surfaces []Surface `json:"surfaces"`
	Seams    []Seam    `json:"seams"`
	Domains  []Domain  `json:"domains"`
}

type Review struct {
	Version      int           `json:"version"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Model        string        `json:"model"`
	Target       FrozenRepo    `json:"target"`
	Workspace    FrozenRepo    `json:"workspace,omitempty"`
	Bindings     Bindings      `json:"bindings"`
	Baseline     BaselineState `json:"baseline"`
	SurfaceMap   *SurfaceMap   `json:"surface_map,omitempty"`
	Expanded     bool          `json:"expanded"`
	Jobs         []Job         `json:"jobs"`
	ProjectRoot  string        `json:"project_root"`
	ReviewRoot   string        `json:"review_root"`
	RuntimeRoot  string        `json:"runtime_root"`
	AgentwrapSHA string        `json:"agentwrap_sha"`
}

type RunOptions struct {
	Stage        string
	Parallel     int
	MaxAttempts  int
	IdleTimeout  time.Duration
	TaskTimeout  time.Duration
	AgentRetries int
	AllowDrift   bool
	RetryFailed  bool
}
