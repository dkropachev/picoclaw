package attention

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	// WorkflowName and WorkflowRef are reserved for the shared private
	// user-attention workflow. Keeping the original values preserves every
	// persisted review-attention run and private context marker.
	WorkflowName = "Review attention gates"
	WorkflowRef  = "inline/review-attention-gates/v1"

	privateRunAdmissionUncertainFailure = "private attention admission quarantined"
)

var (
	ErrPrivateRunUnavailable = errors.New("attention private workflow is unavailable")
	// ErrPrivateRunAdmissionUncertain means the deterministic private run was
	// durably created but its admission could not be proven complete. Callers
	// must quarantine it rather than execute or recreate it automatically.
	ErrPrivateRunAdmissionUncertain = errors.New(
		"attention private workflow admission is uncertain",
	)
)

// DecisionBinding is the narrow durable authority that binds one product
// decision key to its deterministic private workflow run. Admit must invoke
// create at most once, atomically with a new binding; an exact historical
// duplicate returns existed=true without invoking create.
type DecisionBinding interface {
	Find(ctx context.Context, key string) (runID string, ok bool, err error)
	Admit(
		ctx context.Context,
		key string,
		create func(context.Context) error,
	) (runID string, existed bool, err error)
}

type PrivateRunnerConfig struct {
	Executor  *workflows.Executor
	Runs      workflows.RunStore
	Policies  PolicySource
	Decisions DecisionBinding
}

// PrivateRunner compiles and executes shared gate primitives in an authenticated
// private workflow root. Product adapters retain ownership of subjects,
// working-session projection, and durable decision-key meaning.
type PrivateRunner struct {
	executor  *workflows.Executor
	runs      workflows.RunStore
	policies  PolicySource
	decisions DecisionBinding
}

type PrivateLaunchRequest struct {
	DecisionKey     string                        `json:"-"`
	Policy          PreparedPolicy                `json:"-"`
	Selector        PolicySelector                `json:"-"`
	RevalidateLive  bool                          `json:"-"`
	Subject         map[string]any                `json:"-"`
	ReadOnlySession *workflows.ReadOnlySessionRef `json:"-"`
}

type PrivateLaunchResult struct {
	RunID    string
	Status   string
	Existing bool
	Noop     bool
}

func NewPrivateRunner(config PrivateRunnerConfig) (*PrivateRunner, error) {
	if config.Executor == nil {
		return nil, errors.New("attention workflow executor is required")
	}
	if config.Decisions == nil || nilInterface(config.Decisions) {
		return nil, errors.New("attention decision binding is required")
	}
	if config.Policies == nil || nilInterface(config.Policies) {
		return nil, errors.New("attention policy source is required")
	}
	runs := config.Runs
	if runs == nil || nilInterface(runs) {
		runs = config.Executor.Store
	}
	if runs == nil || nilInterface(runs) {
		runs = workflows.NewFileRunStore(config.Executor.WorkspaceDir)
	}
	return &PrivateRunner{
		executor:  config.Executor,
		runs:      runs,
		policies:  config.Policies,
		decisions: config.Decisions,
	}, nil
}

func (runner *PrivateRunner) Available() bool {
	return runner != nil && runner.executor != nil && runner.runs != nil &&
		runner.policies != nil && runner.decisions != nil
}

// CanonicalDecisionKey detaches a product decision identity into the exact JSON
// bytes used both by DecisionBinding and deterministic run-ID derivation.
func CanonicalDecisionKey(value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) == 0 || bytes.Equal(canonical, []byte("null")) {
		return "", ErrPrivateRunUnavailable
	}
	if err = validateCanonicalDecisionKey(string(canonical)); err != nil {
		return "", err
	}
	return string(canonical), nil
}

// RunIDForDecisionKey preserves the original stable review-attention identity:
// sha256 over {"version":1,"key":<canonical key>}, truncated to 128 bits.
func RunIDForDecisionKey(key string) (string, error) {
	if err := validateCanonicalDecisionKey(key); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		Version int             `json:"version"`
		Key     json.RawMessage `json:"key"`
	}{Version: 1, Key: json.RawMessage(key)})
	if err != nil {
		return "", ErrPrivateRunUnavailable
	}
	digest := sha256.Sum256(canonical)
	return "wr_" + hex.EncodeToString(digest[:16]), nil
}

// FindExisting returns the exact linked run when a durable decision already
// exists. A malformed or missing linked run fails closed.
func (runner *PrivateRunner) FindExisting(
	ctx context.Context,
	key string,
) (PrivateLaunchResult, bool, error) {
	if !runner.Available() {
		return PrivateLaunchResult{}, false, ErrPrivateRunUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		return PrivateLaunchResult{}, false, err
	}
	linkedRunID, found, err := runner.decisions.Find(ctx, key)
	if err != nil {
		return PrivateLaunchResult{}, false, err
	}
	if !found {
		if linkedRunID != "" {
			return PrivateLaunchResult{}, false, ErrPrivateRunUnavailable
		}
		return PrivateLaunchResult{}, false, nil
	}
	if linkedRunID != runID {
		return PrivateLaunchResult{}, false, ErrPrivateRunUnavailable
	}
	run, err := runner.runs.GetRun(ctx, runID)
	if err != nil || !ValidPrivateRun(run, runID) {
		return PrivateLaunchResult{}, false, ErrPrivateRunUnavailable
	}
	// Launch executes synchronously until the run is waiting or terminal. A
	// linked run that is still running is therefore either owned by another
	// currently active launcher or was stranded by a crash/admission failure.
	// Never project it as a completed exact replay: a live owner will advance it
	// shortly, while an abandoned owner requires explicit quarantine/recovery.
	if run.Status == workflows.RunStatusRunning {
		return PrivateLaunchResult{}, false, ErrPrivateRunAdmissionUncertain
	}
	return privateResultForRun(run, true), true, nil
}

func (runner *PrivateRunner) Launch(
	ctx context.Context,
	request PrivateLaunchRequest,
) (PrivateLaunchResult, error) {
	if !runner.Available() || !request.Policy.valid() {
		return PrivateLaunchResult{}, ErrPrivateRunUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, found, err := runner.FindExisting(ctx, request.DecisionKey); err != nil {
		return PrivateLaunchResult{}, err
	} else if found {
		return existing, nil
	}

	compilation, err := workflows.CompileGateWorkflow(
		WorkflowName,
		request.Policy.EffectiveGates(),
		request.Subject,
	)
	if err != nil || compilation == nil {
		return PrivateLaunchResult{}, ErrPrivateRunUnavailable
	}
	if compilation.Noop {
		if compilation.Workflow != nil || compilation.PrivateRoot != nil ||
			compilation.RequiresSession || compilation.RequiredSessionAgentID != "" ||
			!request.Policy.IsNoop() || request.ReadOnlySession != nil {
			return PrivateLaunchResult{}, ErrPrivateRunUnavailable
		}
		return PrivateLaunchResult{Noop: true}, nil
	}
	if compilation.Workflow == nil || compilation.PrivateRoot == nil {
		return PrivateLaunchResult{}, ErrPrivateRunUnavailable
	}
	workingAgentID := request.Policy.WorkingContextAgentID()
	if workingAgentID == "" {
		if compilation.RequiresSession || compilation.RequiredSessionAgentID != "" ||
			request.ReadOnlySession != nil {
			return PrivateLaunchResult{}, ErrPrivateRunUnavailable
		}
	} else {
		if !compilation.RequiresSession ||
			compilation.RequiredSessionAgentID != workingAgentID ||
			request.ReadOnlySession == nil ||
			request.ReadOnlySession.AgentID != workingAgentID ||
			request.ReadOnlySession.Session == "" ||
			request.ReadOnlySession.ExpectedRevision == "" {
			return PrivateLaunchResult{}, ErrPrivateRunUnavailable
		}
		readOnly := *request.ReadOnlySession
		compilation.PrivateRoot.ReadOnlySession = &readOnly
	}
	return runner.launchCompilation(ctx, request, compilation)
}

func (runner *PrivateRunner) launchCompilation(
	ctx context.Context,
	request PrivateLaunchRequest,
	compilation *workflows.GateCompilation,
) (PrivateLaunchResult, error) {
	runID, err := RunIDForDecisionKey(request.DecisionKey)
	if err != nil {
		return PrivateLaunchResult{}, err
	}
	executor := *runner.executor
	executor.Store = runner.runs
	baseAdmission := executor.AdmittedRunCreate
	duplicate := false
	durableCreated := false
	admissionUncertain := false
	executor.AdmittedRunCreate = func(
		admissionCtx context.Context,
		candidate *workflows.Run,
		create func() error,
	) error {
		if !ValidPrivateAdmissionCandidate(candidate, runID) {
			return workflows.ErrRunAdmissionConflict
		}
		admit := func(admitCtx context.Context) error {
			linkedRunID, existed, admitErr := runner.decisions.Admit(
				admitCtx,
				request.DecisionKey,
				func(createCtx context.Context) error {
					createCalls := 0
					var firstCreateErr error
					createSucceeded := false
					checkedCreate := func() error {
						createCalls++
						if createCalls != 1 {
							if createSucceeded {
								admissionUncertain = true
							}
							return workflows.ErrRunAdmissionUnavailable
						}
						firstCreateErr = create()
						createSucceeded = firstCreateErr == nil
						if createSucceeded {
							durableCreated = true
						}
						return firstCreateErr
					}
					var admissionErr error
					if baseAdmission != nil {
						admissionErr = baseAdmission(createCtx, candidate, checkedCreate)
					} else {
						admissionErr = checkedCreate()
					}
					if createSucceeded {
						if admissionErr != nil || createCalls != 1 {
							admissionUncertain = true
						}
						// The run now exists. Commit the enclosing decision link even
						// when the base hook failed afterward, then stop execution at
						// the outer admission boundary below.
						return nil
					}
					if admissionErr != nil {
						return admissionErr
					}
					if firstCreateErr != nil {
						return firstCreateErr
					}
					if createCalls != 1 {
						return workflows.ErrRunAdmissionUnavailable
					}
					return nil
				},
			)
			if admitErr != nil {
				if durableCreated || errors.Is(admitErr, ErrPrivateRunAdmissionUncertain) {
					admissionUncertain = true
					return ErrPrivateRunAdmissionUncertain
				}
				return admitErr
			}
			if linkedRunID != runID {
				if durableCreated {
					admissionUncertain = true
					return ErrPrivateRunAdmissionUncertain
				}
				return ErrPrivateRunUnavailable
			}
			if existed {
				if durableCreated {
					admissionUncertain = true
					return ErrPrivateRunAdmissionUncertain
				}
				duplicate = true
				// The executor must not execute its duplicate in-memory candidate.
				return workflows.ErrRunAdmissionConflict
			}
			if admissionUncertain {
				return ErrPrivateRunAdmissionUncertain
			}
			return nil
		}

		var policyErr error
		if request.RevalidateLive {
			policyErr = withRevalidatedPolicy(
				admissionCtx,
				runner.policies,
				request.Selector,
				request.Policy,
				admit,
			)
		} else {
			policyErr = admit(admissionCtx)
		}
		if policyErr != nil {
			switch {
			case durableCreated:
				admissionUncertain = true
				return workflows.ErrRunAdmissionUnavailable
			case errors.Is(policyErr, ErrPrivateRunAdmissionUncertain):
				admissionUncertain = true
				return workflows.ErrRunAdmissionUnavailable
			case errors.Is(policyErr, ErrPolicyChanged),
				errors.Is(policyErr, workflows.ErrRunAdmissionConflict):
				return workflows.ErrRunAdmissionConflict
			case errors.Is(policyErr, context.Canceled):
				return context.Canceled
			case errors.Is(policyErr, context.DeadlineExceeded):
				return context.DeadlineExceeded
			default:
				return workflows.ErrRunAdmissionUnavailable
			}
		}
		return nil
	}

	runResult, runErr := executor.Run(ctx, workflows.RunRequest{
		RunID:       runID,
		Workflow:    compilation.Workflow,
		WorkflowRef: WorkflowRef,
		PrivateRoot: compilation.PrivateRoot,
	})
	if admissionUncertain {
		runner.quarantineUncertainRun(ctx, runID)
		// Never project an unexecuted running run as a successful launch. The
		// dedicated error is the sole public signal for quarantine/recovery.
		return PrivateLaunchResult{}, ErrPrivateRunAdmissionUncertain
	}
	if duplicate {
		existing, found, loadErr := runner.FindExisting(ctx, request.DecisionKey)
		if loadErr != nil || !found {
			if loadErr != nil {
				return PrivateLaunchResult{}, loadErr
			}
			return PrivateLaunchResult{}, ErrPrivateRunUnavailable
		}
		return existing, nil
	}
	if runErr != nil && !durableCreated {
		// Admission is serialized by DecisionBinding before checking for an
		// orphan. That lets a concurrent creator finish and return existed=true,
		// while an exact unlinked run makes create fail without execution.
		if _, loadErr := runner.runs.GetRun(
			context.WithoutCancel(ctx),
			runID,
		); !errors.Is(loadErr, os.ErrNotExist) {
			return PrivateLaunchResult{}, ErrPrivateRunAdmissionUncertain
		}
	}
	if runErr != nil {
		if runResult != nil && runResult.RunID == runID && runResult.Status != "" {
			run, loadErr := runner.runs.GetRun(ctx, runID)
			if loadErr == nil && ValidPrivateRun(run, runID) &&
				run.Status == runResult.Status {
				return privateResultForRun(run, false), runErr
			}
		}
		return PrivateLaunchResult{}, runErr
	}
	if runResult == nil || runResult.RunID != runID || runResult.Status == "" {
		return PrivateLaunchResult{}, ErrPrivateRunUnavailable
	}
	run, err := runner.runs.GetRun(ctx, runID)
	if err != nil || !ValidPrivateRun(run, runID) || run.Status != runResult.Status {
		return PrivateLaunchResult{}, ErrPrivateRunUnavailable
	}
	return privateResultForRun(run, false), nil
}

func (runner *PrivateRunner) quarantineUncertainRun(ctx context.Context, runID string) {
	quarantineCtx := context.WithoutCancel(ctx)
	run, err := runner.runs.GetRun(quarantineCtx, runID)
	if err != nil || !ValidPrivateRun(run, runID) ||
		run.Status != workflows.RunStatusRunning || len(run.Jobs) != 0 ||
		len(run.Steps) != 0 || len(run.Outputs) != 0 || run.CompletedAt != nil ||
		run.Error != "" {
		return
	}
	completedAt := time.Now().UTC()
	run.Status = workflows.RunStatusFailed
	run.Error = privateRunAdmissionUncertainFailure
	run.CompletedAt = &completedAt
	run.UpdatedAt = completedAt
	if err = runner.runs.UpdateRun(quarantineCtx, run); err != nil {
		return
	}
	// Force a complete authenticated readback before returning control. The
	// caller still receives only the fixed uncertainty sentinel on any outcome.
	reloaded, err := runner.runs.GetRun(quarantineCtx, runID)
	if err != nil || !ValidPrivateRun(reloaded, runID) ||
		reloaded.Status != workflows.RunStatusFailed ||
		reloaded.Error != privateRunAdmissionUncertainFailure ||
		reloaded.CompletedAt == nil || len(reloaded.Jobs) != 0 ||
		len(reloaded.Steps) != 0 || len(reloaded.Outputs) != 0 {
		return
	}
}

func withRevalidatedPolicy(
	ctx context.Context,
	source PolicySource,
	selector PolicySelector,
	expected PreparedPolicy,
	use func(context.Context) error,
) error {
	if source == nil || nilInterface(source) || !expected.valid() || use == nil {
		return ErrInvalidPolicySource
	}
	guard := &policyCallbackGuard{}
	sourceErr := source.WithAttentionPolicy(
		ctx,
		selector,
		func(policyCtx context.Context, snapshot PolicySnapshot) error {
			return guard.invoke(func() error {
				if policyCtx == nil {
					return ErrInvalidPolicySource
				}
				if contextErr := policyCtx.Err(); contextErr != nil {
					return contextErr
				}
				current, resolveErr := resolvePolicy(snapshot)
				if resolveErr != nil {
					return resolveErr
				}
				if current.sourceRevision != expected.sourceRevision ||
					current.decisionRevision != expected.decisionRevision {
					return ErrPolicyChanged
				}
				return use(policyCtx)
			})
		},
	)
	return guard.finish(sourceErr)
}

func validateCanonicalDecisionKey(key string) error {
	if key == "" || key != string(bytes.TrimSpace([]byte(key))) {
		return ErrPrivateRunUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(key)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return ErrPrivateRunUnavailable
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrPrivateRunUnavailable
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, []byte(key)) {
		return ErrPrivateRunUnavailable
	}
	return nil
}

// ValidPrivateRun verifies the complete public shape and authenticated private
// marker of a shared attention workflow run.
func ValidPrivateRun(run *workflows.Run, runID string) bool {
	if run == nil || run.ID != runID || run.WorkflowRef != WorkflowRef ||
		run.ContextVisibility != workflows.WorkflowContextVisibilityPrivate ||
		!workflows.IsPrivateWorkflowRun(run) || run.ParentRunID != "" ||
		run.CallerJobID != "" || len(run.ChildRunIDs) != 0 ||
		run.RetryOfRunID != "" || run.Session != "" ||
		!deliveryIsEmpty(run.Delivery) || len(run.Inputs) != 0 ||
		len(run.Event) != 0 || run.Origin != nil {
		return false
	}
	switch run.Status {
	case workflows.RunStatusRunning, workflows.RunStatusWaiting,
		workflows.RunStatusSucceeded, workflows.RunStatusFailed,
		workflows.RunStatusCanceled, workflows.RunStatusSkipped:
		return true
	default:
		return false
	}
}

func ValidPrivateAdmissionCandidate(candidate *workflows.Run, runID string) bool {
	return candidate != nil && candidate.ID == runID &&
		candidate.WorkflowRef == WorkflowRef &&
		candidate.Status == workflows.RunStatusRunning &&
		candidate.ContextVisibility == workflows.WorkflowContextVisibilityPrivate &&
		workflows.IsPrivateWorkflowRun(candidate) && candidate.ParentRunID == "" &&
		candidate.CallerJobID == "" && len(candidate.ChildRunIDs) == 0 &&
		candidate.RetryOfRunID == "" && candidate.Session == "" &&
		deliveryIsEmpty(candidate.Delivery) && len(candidate.Inputs) == 0 &&
		len(candidate.Event) == 0 && candidate.Origin == nil
}

func deliveryIsEmpty(delivery workflows.Delivery) bool {
	return delivery.Channel == "" && delivery.ChatID == "" && delivery.TopicID == "" &&
		delivery.ThreadTS == "" && delivery.MessageID == "" &&
		delivery.ReplyToMessageID == "" && len(delivery.ReplyHandles) == 0
}

func privateResultForRun(run *workflows.Run, existing bool) PrivateLaunchResult {
	return PrivateLaunchResult{
		RunID:    run.ID,
		Status:   run.Status,
		Existing: existing,
	}
}
