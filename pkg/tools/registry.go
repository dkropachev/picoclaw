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
	closed                    bool
}

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
	return &ToolRegistry{
		tools:               make(map[string]*ToolEntry),
		privateConstruction: make(map[string]*ToolEntry),
		constructionCatalog: make(map[string]*ToolEntry),
		services:            newToolServiceCache(),
	}
}

func NewOwnedToolRegistry(owner ToolOwner) (*ToolRegistry, error) {
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
	}, nil
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
	r.registerLegacy(
		tool,
		true,
		"Skipped core tool registration by agent allowlist",
		"Tool registration overwrites existing tool",
		"Registered core tool",
	)
}

// RegisterHidden saves hidden tools (visible only via TTL).
func (r *ToolRegistry) RegisterHidden(tool Tool) {
	r.registerLegacy(
		tool,
		false,
		"Skipped hidden tool registration by agent allowlist",
		"Hidden tool registration overwrites existing tool",
		"Registered hidden tool",
	)
}

func (r *ToolRegistry) registerLegacy(
	tool Tool,
	core bool,
	skippedMessage, overwriteMessage, registeredMessage string,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	name := tool.Name()
	if !r.toolAllowedLocked(name) {
		logger.DebugCF(
			"tools",
			skippedMessage,
			map[string]any{"name": name},
		)
		return
	}
	if r.privateConstruction[name] != nil || r.constructionCatalog[name] != nil {
		logger.WarnCF(
			"tools",
			"Tool registration collides with a private factory dependency",
			map[string]any{"name": name},
		)
		return
	}
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", overwriteMessage, map[string]any{"name": name})
	}
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
	logger.DebugCF("tools", registeredMessage, map[string]any{"name": name})
}

// Unregister removes a tool from the registry if it is present.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if _, exists := r.tools[name]; !exists {
		return
	}
	delete(r.tools, name)
	r.version.Add(1)
	logger.DebugCF("tools", "Unregistered tool", map[string]any{"name": name})
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
	defer r.mu.Unlock()
	if r.closed {
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
	logger.DebugCF(
		"tools",
		"PromoteTools completed",
		map[string]any{"requested": len(names), "promoted": promoted, "ttl": ttl},
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

// executeWithContext keeps ordinary registry logging unchanged while allowing
// narrow controller loops to suppress all model-authored or result-derived
// values. The tool name and timing remain observable in that profile.
func (r *ToolRegistry) executeWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback AsyncCallback,
	suppressLogDetails bool,
) *ToolResult {
	logToolExecutionStart(name, args, suppressLogDetails)

	tool, descriptor, mediaStore, ok := r.executableEntry(name)
	if !ok {
		logger.ErrorCF("tool", "Tool not found",
			map[string]any{
				"tool": name,
			})
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
		fields := map[string]any{"tool": name}
		if !suppressLogDetails {
			fields["error"] = err.Error()
		}
		logger.WarnCF("tool", "Tool argument validation failed", fields)
		return ErrorResult(fmt.Sprintf("invalid arguments for tool %q: %s", name, err)).
			WithError(fmt.Errorf("%w: argument validation failed: %w", workflows.ErrToolCallNotDispatched, err))
	}

	return executeResolvedToolWithContext(
		ctx,
		name,
		args,
		tool,
		mediaStore,
		channel,
		chatID,
		asyncCallback,
		suppressLogDetails,
	)
}

func logToolExecutionStart(name string, args map[string]any, suppressLogDetails bool) {
	startFields := map[string]any{"tool": name}
	if !suppressLogDetails {
		startFields["args"] = args
	}
	logger.InfoCF("tool", "Tool execution started", startFields)
}

func executeResolvedToolWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	tool Tool,
	mediaStore media.MediaStore,
	channel, chatID string,
	asyncCallback AsyncCallback,
	suppressLogDetails bool,
) *ToolResult {
	// Inject channel/chatID into ctx so tools read them via ToolChannel(ctx)/ToolChatID(ctx).
	// Always inject — tools validate what they require.
	ctx = WithToolContext(ctx, channel, chatID)
	if suppressLogDetails {
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
				fields := map[string]any{"tool": name}
				if !suppressLogDetails {
					logger.RecoverPanicNoExit(re)
					errMsg = fmt.Sprintf("Tool '%s' crashed with panic: %v", name, re)
					fields["panic"] = fmt.Sprintf("%v", re)
				}
				logger.ErrorCF("tool", "Tool execution panic recovered", fields)
				result = &ToolResult{
					ForLLM:  errMsg,
					ForUser: errMsg,
					IsError: true,
					Err:     fmt.Errorf("panic: %v", re),
				}
			}
		}()

		if asyncExec, ok := tool.(AsyncExecutor); ok && asyncCallback != nil {
			logger.DebugCF("tool", "Executing async tool via ExecuteAsync",
				map[string]any{
					"tool": name,
				})
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

	// Log based on result type
	if result.IsError {
		fields := map[string]any{
			"tool":     name,
			"duration": duration.Milliseconds(),
		}
		if !suppressLogDetails {
			fields["error"] = result.ForLLM
		}
		logger.ErrorCF("tool", "Tool execution failed", fields)
	} else if result.Async {
		logger.InfoCF("tool", "Tool started (async)",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
			})
	} else {
		logger.InfoCF("tool", "Tool execution completed",
			map[string]any{
				"tool":          name,
				"duration_ms":   duration.Milliseconds(),
				"result_length": len(result.ContentForLLM()),
			})
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
	r.mu.RUnlock()
	if owned {
		// Owned registries carry live resource/reservation leases. Shallow
		// cloning cannot duplicate that ownership safely, so fail closed.
		return NewToolRegistry()
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
