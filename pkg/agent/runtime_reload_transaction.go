package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// RuntimeGenerationIdentity is an opaque, comparable generation fence.
// Callers may retain and return it, but its private owner/id/config tuple cannot
// be forged outside this package.
type RuntimeGenerationIdentity struct {
	owner *AgentLoop
	id    uint64
	cfg   *config.Config
}

// SnapshotRuntimeGenerationIdentity captures the exact current generation for
// a later reload transaction. It grants no request or diagnostic authority.
func (al *AgentLoop) SnapshotRuntimeGenerationIdentity() (
	RuntimeGenerationIdentity,
	error,
) {
	generation, err := al.snapshotRuntimeGeneration()
	if err != nil {
		return RuntimeGenerationIdentity{}, err
	}
	return RuntimeGenerationIdentity{
		owner: al,
		id:    generation.id,
		cfg:   generation.cfg,
	}, nil
}

func (al *AgentLoop) runtimeGenerationIdentityMatches(
	identity RuntimeGenerationIdentity,
) bool {
	if al == nil {
		return false
	}
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.runtimeGenerationIdentityMatchesLocked(identity)
}

func (al *AgentLoop) runtimeGenerationIdentityMatchesLocked(
	identity RuntimeGenerationIdentity,
) bool {
	return identity.owner == al && identity.id != 0 &&
		al.runtimeGenerationID == identity.id && al.cfg == identity.cfg
}

// RuntimeReloadTransaction holds AgentLoop.reloadMu across a Gateway service
// transaction so no direct reload can interleave between A snapshot, B
// publication, service commit, and an exact rollback.
type RuntimeReloadTransaction struct {
	mu       sync.Mutex
	owner    *AgentLoop
	current  RuntimeGenerationIdentity
	pending  *RetainedRuntimeGeneration
	active   bool
	unlocked bool
}

// BeginRuntimeReloadTransaction serializes the complete service transaction
// and revalidates the caller's earlier generation snapshot.
func (al *AgentLoop) BeginRuntimeReloadTransaction(
	expected RuntimeGenerationIdentity,
) (*RuntimeReloadTransaction, error) {
	if al == nil || expected.owner != al || expected.id == 0 ||
		expected.cfg == nil {
		return nil, fmt.Errorf("expected runtime generation is invalid")
	}
	al.reloadMu.Lock()
	if al.closed || al.closing.Load() ||
		!al.runtimeGenerationIdentityMatches(expected) {
		al.reloadMu.Unlock()
		return nil, fmt.Errorf("expected runtime generation is stale")
	}
	return &RuntimeReloadTransaction{
		owner:   al,
		current: expected,
		active:  true,
	}, nil
}

// InitialConfig returns the exact generation config fenced by the
// transaction. It returns nil after Close.
func (transaction *RuntimeReloadTransaction) InitialConfig() *config.Config {
	if transaction == nil {
		return nil
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if !transaction.active {
		return nil
	}
	return transaction.current.cfg
}

// PublishRetainingPrevious publishes B and returns an opaque retained-A
// capability captured internally under reloadMu.
func (transaction *RuntimeReloadTransaction) PublishRetainingPrevious(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
) (*RetainedRuntimeGeneration, error) {
	if transaction == nil {
		return nil, fmt.Errorf("runtime reload transaction is not configured")
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if !transaction.active || transaction.owner == nil {
		return nil, fmt.Errorf("runtime reload transaction is closed")
	}
	if transaction.pending != nil {
		return nil, fmt.Errorf("runtime reload transaction already has a retained generation")
	}
	var retained *RetainedRuntimeGeneration
	_, err := transaction.owner.reloadProviderAndConfig(
		ctx,
		provider,
		cfg,
		executionPolicy,
		diagnosticPolicy,
		false,
		true,
		&transaction.current,
		&retained,
		nil,
	)
	if err != nil {
		return nil, err
	}
	if retained == nil {
		return nil, fmt.Errorf("previous runtime generation was not retained")
	}
	retained.transaction = transaction
	transaction.pending = retained
	transaction.current = RuntimeGenerationIdentity{
		owner: transaction.owner,
		id:    retained.successorID,
		cfg:   cfg,
	}
	return retained, nil
}

type retainedRuntimeGenerationState uint8

const (
	retainedRuntimeGenerationAvailable retainedRuntimeGenerationState = iota + 1
	retainedRuntimeGenerationInUse
	retainedRuntimeGenerationConsumed
)

// RetainedRuntimeGeneration is a single-use rollback/commit capability. It
// retains no registry: rollback rebuilds one from the exact saved policies and
// complete per-agent provider binding generation.
type RetainedRuntimeGeneration struct {
	mu sync.Mutex

	owner            *AgentLoop
	transaction      *RuntimeReloadTransaction
	generationID     uint64
	cfg              *config.Config
	executionPolicy  isolation.ExecutionPolicy
	diagnosticPolicy logger.DiagnosticPolicy
	providers        *agentRegistryProviderGeneration
	successorID      uint64
	state            retainedRuntimeGenerationState
}

func newRetainedRuntimeGeneration(
	owner *AgentLoop,
	generation runtimeGeneration,
	providerGeneration *agentRegistryProviderGeneration,
	successorID uint64,
) *RetainedRuntimeGeneration {
	return &RetainedRuntimeGeneration{
		owner:            owner,
		generationID:     generation.id,
		cfg:              generation.cfg,
		executionPolicy:  generation.executionPolicy,
		diagnosticPolicy: generation.diagnosticPolicy,
		providers:        providerGeneration,
		successorID:      successorID,
		state:            retainedRuntimeGenerationAvailable,
	}
}

func (retained *RetainedRuntimeGeneration) beginUse(
	transaction *RuntimeReloadTransaction,
	successor RuntimeGenerationIdentity,
) error {
	if retained == nil {
		return fmt.Errorf("retained runtime generation is required")
	}
	retained.mu.Lock()
	defer retained.mu.Unlock()
	if retained.state != retainedRuntimeGenerationAvailable ||
		transaction == nil || retained.transaction != transaction ||
		retained.owner != transaction.owner || retained.generationID == 0 ||
		retained.cfg == nil || retained.providers == nil ||
		retained.providers.constructorProvider() == nil ||
		retained.successorID != successor.id {
		return fmt.Errorf("retained runtime generation is stale or consumed")
	}
	retained.state = retainedRuntimeGenerationInUse
	return nil
}

func (retained *RetainedRuntimeGeneration) finishUse(success bool) {
	if retained == nil {
		return
	}
	retained.mu.Lock()
	if retained.state == retainedRuntimeGenerationInUse {
		if success {
			retained.state = retainedRuntimeGenerationConsumed
		} else {
			retained.state = retainedRuntimeGenerationAvailable
		}
	}
	retained.mu.Unlock()
}

// Rollback restores retained A only while its exact successor B remains
// current. It returns a retained-B capability for cleanup after A services
// recover.
func (transaction *RuntimeReloadTransaction) Rollback(
	ctx context.Context,
	retained *RetainedRuntimeGeneration,
) (*RetainedRuntimeGeneration, error) {
	if transaction == nil {
		return nil, fmt.Errorf("runtime reload transaction is not configured")
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if !transaction.active || transaction.owner == nil {
		return nil, fmt.Errorf("runtime reload transaction is closed")
	}
	if transaction.pending != retained {
		return nil, fmt.Errorf("retained runtime generation is not current for this transaction")
	}
	if err := retained.beginUse(transaction, transaction.current); err != nil {
		return nil, err
	}
	success := false
	defer func() { retained.finishUse(success) }()

	var failed *RetainedRuntimeGeneration
	_, err := transaction.owner.reloadProviderAndConfig(
		ctx,
		retained.providers.constructorProvider(),
		retained.cfg,
		retained.executionPolicy,
		retained.diagnosticPolicy,
		false,
		true,
		&transaction.current,
		&failed,
		retained.providers,
	)
	if err != nil {
		return nil, err
	}
	if failed == nil {
		return nil, fmt.Errorf("failed runtime generation was not retained")
	}
	failed.transaction = transaction
	transaction.current = RuntimeGenerationIdentity{
		owner: transaction.owner,
		id:    failed.successorID,
		cfg:   retained.cfg,
	}
	transaction.pending = failed
	success = true
	return failed, nil
}

// CommitRetained closes a generation retained for rollback after its exact
// successor has committed. The capability is consumed once.
func (transaction *RuntimeReloadTransaction) CommitRetained(
	ctx context.Context,
	retained *RetainedRuntimeGeneration,
) error {
	_ = ctx
	if transaction == nil {
		return fmt.Errorf("runtime reload transaction is not configured")
	}
	transaction.mu.Lock()
	if !transaction.active || transaction.owner == nil {
		transaction.mu.Unlock()
		return fmt.Errorf("runtime reload transaction is closed")
	}
	if transaction.pending != retained {
		transaction.mu.Unlock()
		return fmt.Errorf("retained runtime generation is not current for this transaction")
	}
	if err := retained.beginUse(transaction, transaction.current); err != nil {
		transaction.mu.Unlock()
		return err
	}
	retained.finishUse(true)
	providerGeneration := retained.providers
	transaction.pending = nil
	owner := transaction.owner
	transaction.active = false
	transaction.unlocked = true
	transaction.mu.Unlock()
	owner.reloadMu.Unlock()
	providerGeneration.closeAll()
	return nil
}

// Close releases reloadMu. It is idempotent; retained capabilities remain
// explicit caller obligations and cannot be used by another transaction.
func (transaction *RuntimeReloadTransaction) Close() {
	if transaction == nil {
		return
	}
	transaction.mu.Lock()
	if !transaction.active || transaction.unlocked {
		transaction.mu.Unlock()
		return
	}
	transaction.active = false
	transaction.unlocked = true
	owner := transaction.owner
	pending := transaction.pending
	transaction.pending = nil
	var providerGeneration *agentRegistryProviderGeneration
	if pending != nil {
		pending.mu.Lock()
		if pending.state == retainedRuntimeGenerationAvailable {
			pending.state = retainedRuntimeGenerationConsumed
			providerGeneration = pending.providers
		}
		pending.mu.Unlock()
	}
	transaction.mu.Unlock()
	owner.reloadMu.Unlock()
	providerGeneration.closeAll()
}
