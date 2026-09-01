package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// ContextManager manages conversation context via a pluggable strategy.
// Exactly ONE ContextManager is active per AgentLoop, selected by config.
// The default ("legacy") preserves current summarization behavior.
type ContextManager interface {
	// Assemble builds budget-aware context from the ContextManager's own storage.
	// Called before BuildMessages. Returns assembled messages ready for LLM.
	Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error)

	// Compact compresses conversation history.
	// Called after turn completes (may be async internally) and on context overflow (sync).
	Compact(ctx context.Context, req *CompactRequest) error

	// Ingest records a message into the ContextManager's own storage.
	// Called after each message is committed to session storage.
	Ingest(ctx context.Context, req *IngestRequest) error

	// Clear removes all stored context for a session (messages, summaries, etc.).
	// Called when the user issues /clear or /reset.
	Clear(ctx context.Context, sessionKey string) error
}

// contextManagerCloser is implemented by context managers that own resources
// such as database handles. It remains optional so external managers do not
// need to add a no-op lifecycle method.
type contextManagerCloser interface {
	Close() error
}

func closeContextManager(manager ContextManager) error {
	if closer, ok := manager.(contextManagerCloser); ok {
		return closer.Close()
	}
	return nil
}

// AssembleRequest is the input to Assemble.
type AssembleRequest struct {
	SessionKey string // session identifier
	Budget     int    // context window in tokens
	MaxTokens  int    // max response tokens
}

// AssembleResponse is the output of Assemble.
type AssembleResponse struct {
	History []providers.Message // assembled conversation history for BuildMessages
	Summary string              // conversation summary embedded into system prompt by BuildMessages
}

// CompactRequest is the input to Compact.
type CompactRequest struct {
	SessionKey string                // session identifier
	Reason     ContextCompressReason // proactive_budget | llm_retry | summarize
	Budget     int                   // context window budget (used for retry aggressive compaction)
}

// IngestRequest is the input to Ingest.
type IngestRequest struct {
	SessionKey string            // session identifier
	Message    providers.Message // the message just persisted
}

// ContextManagerFactory constructs a ContextManager from config.
// al provides access to the AgentLoop's runtime resources (provider, model, workspace, etc.)
// cfg is the raw JSON configuration from config.json (may be nil).
type ContextManagerFactory func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error)

// contextManagerContextFactory is the internal context-aware form used by
// built-in managers whose construction can perform bounded I/O. The public
// registration API remains source-compatible for external managers.
type contextManagerContextFactory func(
	ctx context.Context,
	cfg json.RawMessage,
	al *AgentLoop,
) (ContextManager, error)

type contextManagerResolver func(
	ctx context.Context,
	name string,
	cfg json.RawMessage,
	al *AgentLoop,
) (ContextManager, error)

type contextManagerRegistration struct {
	factory        ContextManagerFactory
	contextFactory contextManagerContextFactory
}

var (
	cmRegistryMu sync.RWMutex
	cmRegistry   = map[string]contextManagerRegistration{}
)

// RegisterContextManager registers a named ContextManager factory.
func RegisterContextManager(name string, factory ContextManagerFactory) error {
	return registerContextManager(name, factory, nil)
}

func registerContextManagerWithContext(
	name string,
	factory ContextManagerFactory,
	contextFactory contextManagerContextFactory,
) error {
	if contextFactory == nil {
		return fmt.Errorf("context manager %q context factory is nil", name)
	}
	return registerContextManager(name, factory, contextFactory)
}

func registerContextManager(
	name string,
	factory ContextManagerFactory,
	contextFactory contextManagerContextFactory,
) error {
	if name == "" {
		return fmt.Errorf("context manager name is required")
	}
	if factory == nil {
		return fmt.Errorf("context manager %q factory is nil", name)
	}

	cmRegistryMu.Lock()
	defer cmRegistryMu.Unlock()

	if _, exists := cmRegistry[name]; exists {
		return fmt.Errorf("context manager %q is already registered", name)
	}
	cmRegistry[name] = contextManagerRegistration{
		factory: factory, contextFactory: contextFactory,
	}
	return nil
}

func lookupContextManager(name string) (ContextManagerFactory, bool) {
	cmRegistryMu.RLock()
	defer cmRegistryMu.RUnlock()

	registration, ok := cmRegistry[name]
	return registration.factory, ok
}

func lookupContextManagerWithContext(
	name string,
) (contextManagerContextFactory, bool) {
	cmRegistryMu.RLock()
	defer cmRegistryMu.RUnlock()

	registration, ok := cmRegistry[name]
	if !ok || registration.contextFactory == nil {
		return nil, false
	}
	return registration.contextFactory, true
}
