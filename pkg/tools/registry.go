package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type ToolEntry struct {
	Tool   Tool
	IsCore bool
	TTL    int

	descriptor         *ToolDescriptor
	traits             ToolTraits
	factory            ToolFactory
	immutableShared    bool
	visibilityRevision uint64
}

type ToolRegistry struct {
	tools map[string]*ToolEntry
	// privateConstruction contains owner-built dependency products. The
	// constructionCatalog contains dormant frozen specs only; neither map is
	// callable, discoverable, or outwardly projected.
	privateConstruction       map[string]*ToolEntry
	constructionCatalog       map[string]*ToolEntry
	mu                        sync.RWMutex
	mediaApplyMu              sync.Mutex
	version                   atomic.Uint64 // incremented on Register/RegisterHidden for cache invalidation
	mediaStore                media.MediaStore
	mediaGen                  uint64
	allowlist                 map[string]struct{}
	exactRegistrationCap      map[string]struct{}
	owner                     ToolOwner
	owned                     bool
	services                  *toolServiceCache
	ownedValues               []any
	reservations              []toolInstanceReservation
	compatibilityReservations []toolInstanceReservation
	diagnosticOwnerCap        logger.DiagnosticPolicy
	closed                    bool
}

type toolRegistryDiagnosticCapContextKey struct{}

// CoreToolSnapshotEntry preserves the exact registry key used to execute a
// core tool. Tool.Name can be mutable or panic, so catalog callers must not
// derive executable targets from it.
type CoreToolSnapshotEntry struct {
	Name       string
	Tool       Tool
	Descriptor *ToolDescriptor
}

func (entry CoreToolSnapshotEntry) ParameterSchema() map[string]any {
	if entry.Descriptor != nil {
		return cloneToolDescriptor(*entry.Descriptor).Parameters
	}
	if entry.Tool == nil {
		return nil
	}
	return entry.Tool.Parameters()
}

type mediaStoreAware interface {
	SetMediaStore(store media.MediaStore)
}

func toolEntryDescription(entry *ToolEntry) string {
	if entry.descriptor != nil {
		return entry.descriptor.Description
	}
	return entry.Tool.Description()
}

func toolEntryNameDescription(entry *ToolEntry) (string, string) {
	if entry.descriptor != nil {
		return entry.descriptor.Name, entry.descriptor.Description
	}
	return entry.Tool.Name(), entry.Tool.Description()
}

func NewToolRegistry() *ToolRegistry {
	return NewToolRegistryWithDiagnosticPolicy(logger.DiagnosticPolicy{})
}

// NewToolRegistryWithDiagnosticPolicy creates an explicitly policy-capped
// unowned registry. Its immutable diagnostic capability is the maximum any
// request may exercise. The zero policy is deliberately accepted and is the
// safe compatibility default used by NewToolRegistry.
func NewToolRegistryWithDiagnosticPolicy(
	diagnosticPolicy logger.DiagnosticPolicy,
) *ToolRegistry {
	return &ToolRegistry{
		tools:               make(map[string]*ToolEntry),
		privateConstruction: make(map[string]*ToolEntry),
		constructionCatalog: make(map[string]*ToolEntry),
		services:            newToolServiceCache(),
		diagnosticOwnerCap:  diagnosticPolicy,
	}
}

func NewOwnedToolRegistry(owner ToolOwner) (*ToolRegistry, error) {
	return NewOwnedToolRegistryWithDiagnosticPolicy(
		owner,
		logger.DiagnosticPolicy{},
	)
}

// NewOwnedToolRegistryWithDiagnosticPolicy creates an owner-scoped registry
// with one immutable diagnostic cap. There is intentionally no mutator or
// accessor for this capability: descendants receive it only through registry
// construction paths.
func NewOwnedToolRegistryWithDiagnosticPolicy(
	owner ToolOwner,
	diagnosticPolicy logger.DiagnosticPolicy,
) (*ToolRegistry, error) {
	if err := owner.validate(); err != nil {
		return nil, err
	}
	return &ToolRegistry{
		tools:               make(map[string]*ToolEntry),
		privateConstruction: make(map[string]*ToolEntry),
		constructionCatalog: make(map[string]*ToolEntry),
		owner:               owner,
		owned:               true,
		services:            newToolServiceCache(),
		diagnosticOwnerCap:  diagnosticPolicy,
	}, nil
}

// diagnosticPolicyForContext computes the immediate per-call capability. A
// request can only narrow its registry owner, and argument suppression is an
// unconditional false cap.
func (r *ToolRegistry) diagnosticPolicyForContext(
	ctx context.Context,
	suppressed bool,
) logger.DiagnosticPolicy {
	effectiveSuppressed := suppressed || ToolLogDetailsSuppressed(ctx)
	if r == nil || effectiveSuppressed {
		return logger.DiagnosticPolicy{}
	}
	effective := r.diagnosticOwnerCap.Meet(logger.DiagnosticPolicyFromContext(ctx))
	if inherited, ok := toolRegistryDiagnosticCapFromContext(ctx); ok {
		effective = effective.Meet(inherited)
	}
	return effective
}

// withToolRegistryDiagnosticCap carries one immutable, false-dominating
// registry/request meet into nested tool work without replacing the live
// logger request binding. A later request revoke therefore remains visible,
// while a nested caller cannot widen a false outer cap by swapping registries.
func withToolRegistryDiagnosticCap(
	ctx context.Context,
	current logger.DiagnosticPolicy,
) context.Context {
	if inherited, ok := toolRegistryDiagnosticCapFromContext(ctx); ok {
		current = inherited.Meet(current)
	}
	return context.WithValue(ctx, toolRegistryDiagnosticCapContextKey{}, current)
}

func toolRegistryDiagnosticCapFromContext(
	ctx context.Context,
) (logger.DiagnosticPolicy, bool) {
	if ctx == nil {
		return logger.DiagnosticPolicy{}, false
	}
	policy, ok := ctx.Value(toolRegistryDiagnosticCapContextKey{}).(logger.DiagnosticPolicy)
	return policy, ok
}

func (r *ToolRegistry) Owner() (ToolOwner, bool) {
	if r == nil {
		return ToolOwner{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.owner, r.owned
}

func (r *ToolRegistry) Traits(name string) (ToolTraits, bool) {
	if r == nil {
		return ToolTraits{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok || entry == nil {
		return ToolTraits{}, false
	}
	return entry.traits, true
}

// Close releases strict owner-created resources and compatibility-source
// instance leases after the caller has quiesced registry and retained Tool
// use. It never closes caller-owned compatibility tools or explicitly
// immutable-shared entries. Plain legacy unowned registries remain a no-op;
// dormant factory-dependency sources are lifecycle-managed even before a
// compatibility tool is published.
func (r *ToolRegistry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed || !r.owned && len(r.compatibilityReservations) == 0 &&
		len(r.constructionCatalog) == 0 {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	var created []any
	var reservations []toolInstanceReservation
	if r.owned {
		created = append([]any(nil), r.ownedValues...)
		reservations = append([]toolInstanceReservation(nil), r.reservations...)
	}
	compatibilityReservations := append(
		[]toolInstanceReservation(nil),
		r.compatibilityReservations...,
	)
	r.ownedValues = nil
	r.reservations = nil
	r.compatibilityReservations = nil
	r.tools = make(map[string]*ToolEntry)
	r.privateConstruction = make(map[string]*ToolEntry)
	r.constructionCatalog = make(map[string]*ToolEntry)
	if r.owned && r.services != nil {
		r.services.mu.Lock()
		r.services.values = make(map[string]any)
		r.services.mu.Unlock()
	}
	r.mu.Unlock()

	closeErr := closeOwnerCreatedValues(created)
	if closeErr == nil {
		for index := len(reservations) - 1; index >= 0; index-- {
			reservations[index].release()
		}
	}
	for index := len(compatibilityReservations) - 1; index >= 0; index-- {
		compatibilityReservations[index].release()
	}
	return closeErr
}

// SetAllowlist restricts registrations to the provided runtime tool names.
// A nil slice means "allow all". An empty-but-non-nil slice means "allow none".
// It cannot widen an immutable exact registration cap installed by selected
// owner construction.
func (r *ToolRegistry) SetAllowlist(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}

	if names == nil {
		r.allowlist = nil
		return
	}

	allowlist := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.ToLower(strings.TrimSpace(name))
		if trimmed == "" {
			continue
		}
		allowlist[trimmed] = struct{}{}
	}
	r.allowlist = allowlist
}

func (r *ToolRegistry) Register(tool Tool) {
	r.registerLegacy(tool, true)
}

// RegisterHidden saves hidden tools (visible only via TTL).
func (r *ToolRegistry) RegisterHidden(tool Tool) {
	r.registerLegacy(tool, false)
}

func (r *ToolRegistry) registerLegacy(tool Tool, core bool) {
	r.mu.Lock()
	locked := true
	defer func() {
		if locked {
			r.mu.Unlock()
		}
	}()
	unlock := func() {
		r.mu.Unlock()
		locked = false
	}
	if r.closed {
		unlock()
		return
	}
	name := tool.Name()
	if !r.toolAllowedLocked(name) {
		unlock()
		logger.DebugSafeCF(
			logger.ComponentTools,
			logger.DiagnosticMessageToolRegistrationSkipped,
			logger.NewSafeFields(
				logger.SafeObservation(
					logger.ObservationPrefixIdentityTool,
					logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
				),
				logger.SafeBool(logger.FieldCore, core),
			),
		)
		return
	}
	if r.privateConstruction[name] != nil || r.constructionCatalog[name] != nil {
		unlock()
		logger.WarnSafeCF(
			logger.ComponentTools,
			logger.DiagnosticMessageToolRegistrationCollision,
			logger.NewSafeFields(
				logger.SafeObservation(
					logger.ObservationPrefixIdentityTool,
					logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
				),
				logger.SafeBool(logger.FieldCore, core),
			),
		)
		return
	}
	_, overwritten := r.tools[name]
	r.tools[name] = &ToolEntry{
		Tool:   tool,
		IsCore: core,
		TTL:    0,
		traits: conservativeLegacyToolTraits(),
	}
	if aware, ok := tool.(mediaStoreAware); ok && r.mediaStore != nil {
		aware.SetMediaStore(r.mediaStore)
	}
	r.version.Add(1)
	unlock()

	fields := logger.NewSafeFields(
		logger.SafeObservation(
			logger.ObservationPrefixIdentityTool,
			logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
		),
		logger.SafeBool(logger.FieldCore, core),
	)
	if overwritten {
		logger.WarnSafeCF(
			logger.ComponentTools,
			logger.DiagnosticMessageToolRegistrationOverwritten,
			fields,
		)
	}
	logger.DebugSafeCF(
		logger.ComponentTools,
		logger.DiagnosticMessageToolRegistered,
		fields,
	)
}

// Unregister removes a tool from the registry if it is present.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}

	name = strings.TrimSpace(name)
	if name == "" {
		r.mu.Unlock()
		return
	}
	if _, exists := r.tools[name]; !exists {
		r.mu.Unlock()
		return
	}
	delete(r.tools, name)
	r.version.Add(1)
	r.mu.Unlock()

	logger.DebugSafeCF(
		logger.ComponentTools,
		logger.DiagnosticMessageToolUnregistered,
		logger.NewSafeFields(logger.SafeObservation(
			logger.ObservationPrefixIdentityTool,
			logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
		)),
	)
}

// SetMediaStore injects a MediaStore into all registered tools that can
// consume it, and remembers it for future registrations.
func (r *ToolRegistry) SetMediaStore(store media.MediaStore) {
	r.mediaApplyMu.Lock()
	defer r.mediaApplyMu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.mediaStore = store
	r.mediaGen++
	awareTools := make([]mediaStoreAware, 0, len(r.tools)+len(r.privateConstruction))
	for _, entry := range r.tools {
		if aware, ok := entry.Tool.(mediaStoreAware); ok {
			awareTools = append(awareTools, aware)
		}
	}
	for _, entry := range r.privateConstruction {
		if aware, ok := entry.Tool.(mediaStoreAware); ok {
			awareTools = append(awareTools, aware)
		}
	}
	r.mu.Unlock()

	for _, aware := range awareTools {
		aware.SetMediaStore(store)
	}
}

// PromoteTools atomically sets the TTL for multiple non-core tools.
// This prevents a concurrent TickTTL from decrementing between promotions.
func (r *ToolRegistry) PromoteTools(names []string, ttl int) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	promoted := 0
	for _, name := range names {
		if entry, exists := r.tools[name]; exists {
			if !entry.IsCore {
				if entry.TTL != ttl {
					entry.TTL = ttl
					entry.visibilityRevision++
				}
				promoted++
			}
		}
	}
	r.mu.Unlock()

	logger.DebugSafeCF(
		logger.ComponentTools,
		logger.DiagnosticMessageToolPromotionCompleted,
		logger.NewSafeFields(
			logger.SafeInt(logger.FieldRequestedCount, len(names)),
			logger.SafeInt(logger.FieldPromotedCount, promoted),
		),
	)
}

// TickTTL decreases TTL only for non-core tools
func (r *ToolRegistry) TickTTL() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for _, entry := range r.tools {
		if !entry.IsCore && entry.TTL > 0 {
			entry.TTL--
			entry.visibilityRevision++
		}
	}
}

// Version returns the current registry version (atomically).
func (r *ToolRegistry) Version() uint64 {
	return r.version.Load()
}

func (r *ToolRegistry) toolAllowedLocked(name string) bool {
	if r.exactRegistrationCap != nil {
		if _, ok := r.exactRegistrationCap[name]; !ok {
			return false
		}
	}
	if r.allowlist == nil {
		return true
	}
	if r.exactRegistrationCap == nil && isToolDiscoveryToolName(name) {
		// Discovery tools are part of the MCP control plane: they must remain
		// available whenever configured so deferred MCP tools can still be
		// unlocked. Per-agent allowlists still apply to the hidden MCP tools
		// themselves during RegisterHidden.
		return true
	}
	_, ok := r.allowlist[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// AllowsRegistration reports whether the registry's allowlist accepts name.
// It does not inspect whether name is already occupied.
func (r *ToolRegistry) AllowsRegistration(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.toolAllowedLocked(name)
}

// HasRegistered reports whether a tool name is present in the registry,
// including hidden tools whose TTL is currently zero.
func (r *ToolRegistry) HasRegistered(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// GetRegistered returns a registered tool regardless of whether a hidden
// tool's TTL currently makes it callable.
func (r *ToolRegistry) GetRegistered(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return entry.Tool, true
}

// GetCoreTool returns one exact core registry entry. Hidden tools remain
// excluded even while discovery temporarily promotes their TTL.
func (r *ToolRegistry) GetCoreTool(name string) (Tool, bool) {
	entry, ok := r.GetCoreToolSnapshot(name)
	return entry.Tool, ok
}

func (r *ToolRegistry) GetCoreToolSnapshot(name string) (CoreToolSnapshotEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok || entry == nil || !entry.IsCore || entry.Tool == nil {
		return CoreToolSnapshotEntry{}, false
	}
	snapshot := CoreToolSnapshotEntry{Name: name, Tool: entry.Tool}
	if entry.descriptor != nil {
		descriptor := cloneToolDescriptor(*entry.descriptor)
		snapshot.Descriptor = &descriptor
	}
	return snapshot, true
}

// HiddenToolSnapshot holds a consistent snapshot of hidden tools and the
// registry version at which it was taken. Used by BM25SearchTool cache.
type HiddenToolSnapshot struct {
	Docs    []HiddenToolDoc
	Version uint64
}

// HiddenToolDoc is a lightweight representation of a hidden tool for search indexing.
type HiddenToolDoc struct {
	Name        string
	Description string
}

// SnapshotHiddenTools returns all non-core tools and the current registry
// version under a single read-lock, guaranteeing consistency between the
// two values.
func (r *ToolRegistry) SnapshotHiddenTools() HiddenToolSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	docs := make([]HiddenToolDoc, 0, len(r.tools))
	for name, entry := range r.tools {
		if !entry.IsCore {
			docs = append(docs, HiddenToolDoc{
				Name:        name,
				Description: toolEntryDescription(entry),
			})
		}
	}
	return HiddenToolSnapshot{
		Docs:    docs,
		Version: r.version.Load(),
	}
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	// Hidden tools with expired TTL are not callable.
	if !entry.IsCore && entry.TTL <= 0 {
		return nil, false
	}
	return entry.Tool, true
}

func (r *ToolRegistry) executableEntry(name string) (Tool, *ToolDescriptor, media.MediaStore, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok || entry == nil || isTypedNil(entry.Tool) || !entry.IsCore && entry.TTL <= 0 {
		return nil, nil, nil, false
	}
	if entry.descriptor == nil {
		return entry.Tool, nil, r.mediaStore, true
	}
	descriptor := cloneToolDescriptor(*entry.descriptor)
	return entry.Tool, &descriptor, r.mediaStore, true
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", nil)
}

// ExecuteWithContext executes a tool with channel/chatID context and optional async callback.
// If the tool implements AsyncExecutor and a non-nil callback is provided,
// ExecuteAsync is called instead of Execute — the callback is a parameter,
// never stored as mutable state on the tool.
func (r *ToolRegistry) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback AsyncCallback,
) *ToolResult {
	return r.executeWithContext(
		ctx,
		name,
		args,
		channel,
		chatID,
		asyncCallback,
		false,
	)
}

// executeWithContext keeps functional execution unchanged while every central
// diagnostic is projected through fixed, bounded observations. Suppression is
// an additional false cap for raw argument previews. Direct callers retain
// exclusive ownership of args until this synchronous call returns; model-loop
// callers already supply a detached graph.
func (r *ToolRegistry) executeWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback AsyncCallback,
	suppressLogDetails bool,
) *ToolResult {
	effectiveSuppressed := suppressLogDetails || ToolLogDetailsSuppressed(ctx)
	r.logToolExecutionStart(ctx, name, args, effectiveSuppressed)

	tool, descriptor, mediaStore, ok := r.executableEntry(name)
	if !ok {
		logger.ErrorSafeCF(
			logger.ComponentTool,
			logger.DiagnosticMessageToolNotFound,
			logger.NewSafeFields(logger.SafeObservation(
				logger.ObservationPrefixIdentityTool,
				logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
			)),
		)
		return ErrorResult(
			fmt.Sprintf("tool %q not found", name),
		).WithError(fmt.Errorf("tool not found"))
	}

	// Validate arguments against the tool's declared schema.
	var parameters map[string]any
	if descriptor != nil {
		parameters = descriptor.Parameters
	} else {
		parameters = tool.Parameters()
	}
	if err := validateToolArgs(parameters, args); err != nil {
		logger.WarnSafeCF(
			logger.ComponentTool,
			logger.DiagnosticMessageToolArgumentValidationFailed,
			logger.NewSafeFields(
				logger.SafeObservation(
					logger.ObservationPrefixIdentityTool,
					logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
				),
				logger.SafeObservation(
					logger.ObservationPrefixError,
					logger.ObserveErrorType(logger.ErrorClassValidation, err),
				),
			),
		)
		return ErrorResult(fmt.Sprintf("invalid arguments for tool %q: %s", name, err)).
			WithError(fmt.Errorf("%w: argument validation failed: %w", workflows.ErrToolCallNotDispatched, err))
	}
	diagnosticCap := r.diagnosticPolicyForContext(ctx, effectiveSuppressed)

	return executeResolvedToolWithContext(
		ctx,
		name,
		args,
		tool,
		mediaStore,
		channel,
		chatID,
		asyncCallback,
		diagnosticCap,
		effectiveSuppressed,
	)
}

func (r *ToolRegistry) logToolExecutionStart(
	ctx context.Context,
	name string,
	args map[string]any,
	suppressLogDetails bool,
) {
	effectiveSuppressed := suppressLogDetails || ToolLogDetailsSuppressed(ctx)
	normalizedArguments := normalizeToolArgumentsForDiagnostics(args)
	identity := logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name)
	argumentCount := len(args)
	logger.InfoSafeCF(
		logger.ComponentTool,
		logger.DiagnosticMessageToolExecutionStarted,
		logger.NewSafeFields(
			logger.SafeObservation(logger.ObservationPrefixIdentityTool, identity),
			logger.SafeObservation(
				logger.ObservationPrefixToolArguments,
				logger.ObserveJSONValue(
					logger.ObservationDomainToolArguments,
					normalizedArguments,
				),
			),
			logger.SafeInt(logger.FieldArgumentCount, argumentCount),
			logger.SafeBool(logger.FieldSuppressed, effectiveSuppressed),
		),
	)
	logger.DebugSensitiveCF(
		r.diagnosticPolicyForContext(ctx, effectiveSuppressed),
		logger.ComponentTool,
		logger.DiagnosticMessageToolArguments,
		logger.NewSafeFields(
			logger.SafeObservation(logger.ObservationPrefixIdentityTool, identity),
			logger.SafeInt(logger.FieldArgumentCount, argumentCount),
			logger.SafeBool(logger.FieldSuppressed, effectiveSuppressed),
		),
		logger.SensitivityToolArguments,
		logger.ObservationDomainToolArguments,
		normalizedArguments,
	)
}

func executeResolvedToolWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	tool Tool,
	mediaStore media.MediaStore,
	channel, chatID string,
	asyncCallback AsyncCallback,
	diagnosticCap logger.DiagnosticPolicy,
	suppressLogDetails bool,
) *ToolResult {
	effectiveSuppressed := suppressLogDetails || ToolLogDetailsSuppressed(ctx)
	ctx, revokeDiagnosticPolicy := logger.NarrowDiagnosticPolicy(ctx, diagnosticCap)
	defer revokeDiagnosticPolicy()
	ctx = withToolRegistryDiagnosticCap(ctx, diagnosticCap)
	// Inject channel/chatID into ctx so tools read them via ToolChannel(ctx)/ToolChatID(ctx).
	// Always inject — tools validate what they require.
	ctx = WithToolContext(ctx, channel, chatID)
	if effectiveSuppressed {
		ctx = WithToolLogDetailsSuppressed(ctx)
	}

	// If tool implements AsyncExecutor and callback is provided, use ExecuteAsync.
	// The callback is a call parameter, not mutable state on the tool instance.
	var result *ToolResult
	start := time.Now()

	// Use recover to catch any panics during tool execution
	// This prevents tool crashes from killing the entire agent
	func() {
		defer func() {
			if re := recover(); re != nil {
				errMsg := fmt.Sprintf("Tool '%s' crashed", name)
				if !effectiveSuppressed {
					errMsg = fmt.Sprintf("Tool '%s' crashed with panic: %v", name, re)
				}
				logger.ErrorSafeCF(
					logger.ComponentTool,
					logger.DiagnosticMessageToolExecutionPanic,
					logger.NewSafeFields(
						logger.SafeObservation(
							logger.ObservationPrefixIdentityTool,
							logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
						),
						logger.SafeObservation(
							logger.ObservationPrefixPanic,
							logger.ObservePanic(re),
						),
					),
				)
				result = &ToolResult{
					ForLLM:  errMsg,
					ForUser: errMsg,
					IsError: true,
					Err:     fmt.Errorf("panic: %v", re),
				}
			}
		}()

		if asyncExec, ok := tool.(AsyncExecutor); ok && asyncCallback != nil {
			result = asyncExec.ExecuteAsync(ctx, args, asyncCallback)
		} else {
			result = tool.Execute(ctx, args)
		}
	}()

	// Handle nil result (should not happen, but defensive)
	if result == nil {
		result = &ToolResult{
			ForLLM:  fmt.Sprintf("Tool '%s' returned nil result unexpectedly", name),
			ForUser: fmt.Sprintf("Tool '%s' returned nil result unexpectedly", name),
			IsError: true,
			Err:     fmt.Errorf("nil result from tool"),
		}
	}

	result = normalizeToolResult(result, name, mediaStore, channel, chatID)

	duration := time.Since(start)
	resultObservation := logger.ObserveText(logger.ObservationDomainToolResult, result.ForLLM)
	commonFields := []logger.SafeField{
		logger.SafeObservation(
			logger.ObservationPrefixIdentityTool,
			logger.ObserveIdentity(logger.ObservationDomainIdentityTool, name),
		),
		logger.SafeObservation(logger.ObservationPrefixToolResult, resultObservation),
		logger.SafeInt64(logger.FieldDurationMilliseconds, duration.Milliseconds()),
	}

	// Log based on result type
	if result.IsError {
		logger.ErrorSafeCF(
			logger.ComponentTool,
			logger.DiagnosticMessageToolExecutionFailed,
			logger.NewSafeFields(append(
				commonFields,
				logger.SafeObservation(
					logger.ObservationPrefixError,
					logger.ObserveErrorType(logger.ErrorClassUnknown, result.Err),
				),
			)...),
		)
	} else if result.Async {
		logger.InfoSafeCF(
			logger.ComponentTool,
			logger.DiagnosticMessageToolAsyncStarted,
			logger.NewSafeFields(append(
				commonFields,
				logger.SafeBool(logger.FieldAsync, true),
			)...),
		)
	} else {
		logger.InfoSafeCF(
			logger.ComponentTool,
			logger.DiagnosticMessageToolExecutionCompleted,
			logger.NewSafeFields(commonFields...),
		)
	}

	return result
}

// sortedToolNames returns tool names in sorted order for deterministic iteration.
// This is critical for KV cache stability: non-deterministic map iteration would
// produce different system prompts and tool definitions on each call, invalidating
// the LLM's prefix cache even when no tools have changed.
func (r *ToolRegistry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}

		if entry.descriptor != nil {
			definitions = append(definitions, toolDescriptorSchema(*entry.descriptor))
		} else {
			definitions = append(definitions, ToolToSchema(entry.Tool))
		}
	}
	return definitions
}

// ToProviderDefs converts tool definitions to provider-compatible format.
// This is the format expected by LLM provider APIs.
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]providers.ToolDefinition, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}

		var schema map[string]any
		var metadata PromptMetadata
		if entry.descriptor != nil {
			schema = toolDescriptorSchema(*entry.descriptor)
			metadata = entry.descriptor.PromptMetadata
		} else {
			schema = ToolToSchema(entry.Tool)
			metadata = promptMetadataForTool(entry.Tool)
		}

		// Safely extract nested values with type checks
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
			PromptLayer:  metadata.Layer,
			PromptSlot:   metadata.Slot,
			PromptSource: metadata.Source,
		})
	}
	return definitions
}

func promptMetadataForTool(tool Tool) PromptMetadata {
	metadata := PromptMetadata{
		Layer:  ToolPromptLayerCapability,
		Slot:   ToolPromptSlotTooling,
		Source: ToolPromptSourceRegistry,
	}
	if provider, ok := tool.(PromptMetadataProvider); ok {
		provided := provider.PromptMetadata()
		if provided.Layer != "" {
			metadata.Layer = provided.Layer
		}
		if provided.Slot != "" {
			metadata.Slot = provided.Slot
		}
		if provided.Source != "" {
			metadata.Source = provided.Source
		}
	}
	return metadata
}

// List returns a list of all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.sortedToolNames()
}

// Clone creates an independent copy of the registry containing the same tool
// entries but intentionally shares every Tool instance. It preserves legacy
// behavior and is unsafe as an ownership/isolation boundary; new owner-scoped
// code must use InstantiateForOwner or InstantiateForOwnerSelection. Calling
// Clone on an owned registry fails closed with an empty unowned view, avoiding
// duplicated resource leases. This compatibility path is used to give
// subagents a snapshot of the parent agent's tools without sharing the same registry —
// tools registered on the parent after cloning (e.g. spawn, spawn_status)
// will NOT be visible to the clone, preventing recursive subagent spawning.
// The version counter is reset to 0 in the clone as it's a new independent registry.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	owned := r.owned
	diagnosticPolicy := r.diagnosticOwnerCap
	r.mu.RUnlock()
	if owned {
		// Owned registries carry live resource/reservation leases. Shallow
		// cloning cannot duplicate that ownership safely, so fail closed.
		return NewToolRegistryWithDiagnosticPolicy(diagnosticPolicy)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := &ToolRegistry{
		tools:               make(map[string]*ToolEntry, len(r.tools)),
		privateConstruction: make(map[string]*ToolEntry),
		constructionCatalog: make(map[string]*ToolEntry),
		mediaStore:          r.mediaStore,
		mediaGen:            r.mediaGen,
		services:            newToolServiceCache(),
		diagnosticOwnerCap:  diagnosticPolicy,
	}
	clone.services.values = r.services.snapshot()
	if r.allowlist != nil {
		clone.allowlist = make(map[string]struct{}, len(r.allowlist))
		for name := range r.allowlist {
			clone.allowlist[name] = struct{}{}
		}
	}
	if r.exactRegistrationCap != nil {
		clone.exactRegistrationCap = make(map[string]struct{}, len(r.exactRegistrationCap))
		for name := range r.exactRegistrationCap {
			clone.exactRegistrationCap[name] = struct{}{}
		}
	}
	for name, entry := range r.tools {
		var descriptor *ToolDescriptor
		if entry.descriptor != nil {
			frozen := cloneToolDescriptor(*entry.descriptor)
			descriptor = &frozen
		}
		clone.tools[name] = &ToolEntry{
			Tool: entry.Tool, IsCore: entry.IsCore, TTL: entry.TTL,
			descriptor: descriptor, traits: entry.traits, factory: entry.factory,
			immutableShared: entry.immutableShared, visibilityRevision: entry.visibilityRevision,
		}
	}
	return clone
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries returns human-readable summaries of all registered tools.
// Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	summaries := make([]string, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !entry.IsCore && entry.TTL <= 0 {
			continue
		}

		toolName, description := toolEntryNameDescription(entry)
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", toolName, description))
	}
	return summaries
}

// GetAll returns all registered tools (both core and non-core with TTL > 0).
// Used by SubTurn to inherit parent's tool set.
func (r *ToolRegistry) GetAll() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	tools := make([]Tool, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		// Include core tools and non-core tools with active TTL
		if entry.IsCore || entry.TTL > 0 {
			tools = append(tools, entry.Tool)
		}
	}
	return tools
}

// VisitCoreTools visits live core registry entries without materializing a
// full snapshot. The callback runs under the registry read lock and must not
// mutate the registry.
func (r *ToolRegistry) VisitCoreTools(
	ctx context.Context,
	visit func(CoreToolSnapshotEntry) bool,
) error {
	if r == nil || visit == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, entry := range r.tools {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry == nil || !entry.IsCore || entry.Tool == nil {
			continue
		}
		snapshot := CoreToolSnapshotEntry{Name: name, Tool: entry.Tool}
		if entry.descriptor != nil {
			descriptor := cloneToolDescriptor(*entry.descriptor)
			snapshot.Descriptor = &descriptor
		}
		if !visit(snapshot) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
