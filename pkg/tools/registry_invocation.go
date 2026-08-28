package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var (
	ErrToolCatalogUnavailable = errors.New("tool catalog unavailable")
	ErrToolInvocationStale    = errors.New("prepared tool invocation is stale")
	ErrToolInvocationUsed     = errors.New("prepared tool invocation already used")
)

// ModelToolCatalog is an opaque snapshot of the callable registry entries that
// produced one provider-visible base definition set. Adaptation/profile code
// may narrow its definitions, but model dispatch must prepare through this same
// catalog rather than performing a fresh name lookup.
type ModelToolCatalog struct {
	registry    *ToolRegistry
	version     uint64
	mediaGen    uint64
	definitions []providers.ToolDefinition
	entries     map[string]modelToolCatalogEntry
}

type modelToolCatalogEntry struct {
	name               string
	entry              *ToolEntry
	tool               Tool
	descriptor         ToolDescriptor
	descriptorIdentity *ToolDescriptor
	traits             ToolTraits
	core               bool
	visibilityRevision uint64
	mediaStore         media.MediaStore
}

type modelToolCatalogSeed struct {
	name               string
	entry              *ToolEntry
	tool               Tool
	descriptor         *ToolDescriptor
	traits             ToolTraits
	visibilityRevision uint64
	core               bool
	ttl                int
}

// SnapshotModelToolCatalog freezes one exact callable registry generation.
// Legacy tool methods are invoked outside the registry lock and the captured
// entries are then validated before publication.
func (r *ToolRegistry) SnapshotModelToolCatalog() (*ModelToolCatalog, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: registry is nil", ErrToolCatalogUnavailable)
	}

	r.mediaApplyMu.Lock()
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		r.mediaApplyMu.Unlock()
		return nil, fmt.Errorf("%w: registry is closed", ErrToolCatalogUnavailable)
	}
	version := r.version.Load()
	mediaGen := r.mediaGen
	mediaStore := r.mediaStore
	names := r.sortedToolNames()
	seeds := make([]modelToolCatalogSeed, 0, len(names))
	for _, name := range names {
		entry := r.tools[name]
		if entry == nil || isTypedNil(entry.Tool) || !entry.IsCore && entry.TTL <= 0 {
			continue
		}
		seed := modelToolCatalogSeed{
			name:               name,
			entry:              entry,
			tool:               entry.Tool,
			traits:             entry.traits,
			visibilityRevision: entry.visibilityRevision,
			core:               entry.IsCore,
			ttl:                entry.TTL,
		}
		if entry.descriptor != nil {
			descriptor := cloneToolDescriptor(*entry.descriptor)
			seed.descriptor = &descriptor
		}
		seeds = append(seeds, seed)
	}
	r.mu.RUnlock()
	r.mediaApplyMu.Unlock()

	entries := make(map[string]modelToolCatalogEntry, len(seeds))
	definitions := make([]providers.ToolDefinition, 0, len(seeds))
	for _, seed := range seeds {
		descriptor := ToolDescriptor{}
		if seed.descriptor != nil {
			descriptor = cloneToolDescriptor(*seed.descriptor)
		} else {
			var err error
			descriptor, err = safeToolDescriptorFromTool(seed.tool)
			if err != nil {
				return nil, fmt.Errorf("%w: describe tool %q: %v", ErrToolCatalogUnavailable, seed.name, err)
			}
		}
		if descriptor.Name != seed.name {
			return nil, fmt.Errorf(
				"%w: descriptor name %q does not match registry key %q",
				ErrToolCatalogUnavailable,
				descriptor.Name,
				seed.name,
			)
		}
		traits, err := seed.traits.normalized()
		if err != nil {
			return nil, fmt.Errorf("%w: normalize traits for %q: %v", ErrToolCatalogUnavailable, seed.name, err)
		}
		definition := providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        descriptor.Name,
				Description: descriptor.Description,
				Parameters:  cloneToolDescriptor(descriptor).Parameters,
			},
			PromptLayer:  descriptor.PromptMetadata.Layer,
			PromptSlot:   descriptor.PromptMetadata.Slot,
			PromptSource: descriptor.PromptMetadata.Source,
		}
		definitions = append(definitions, definition)
		entries[seed.name] = modelToolCatalogEntry{
			name:               seed.name,
			entry:              seed.entry,
			tool:               seed.tool,
			descriptor:         descriptor,
			descriptorIdentity: seed.entry.descriptor,
			traits:             traits,
			core:               seed.core,
			visibilityRevision: seed.visibilityRevision,
			mediaStore:         mediaStore,
		}
	}

	r.mediaApplyMu.Lock()
	r.mu.RLock()
	if r.closed || r.version.Load() != version || r.mediaGen != mediaGen {
		r.mu.RUnlock()
		r.mediaApplyMu.Unlock()
		return nil, fmt.Errorf("%w: registry changed while snapshotting", ErrToolCatalogUnavailable)
	}
	for _, seed := range seeds {
		current := r.tools[seed.name]
		captured := entries[seed.name]
		if current != seed.entry || current == nil || isTypedNil(current.Tool) ||
			!sameToolBinding(current.Tool, captured.tool) ||
			current.descriptor != captured.descriptorIdentity || current.traits != captured.traits ||
			current.visibilityRevision != seed.visibilityRevision ||
			current.IsCore != seed.core || current.TTL != seed.ttl ||
			!current.IsCore && current.TTL <= 0 {
			r.mu.RUnlock()
			r.mediaApplyMu.Unlock()
			return nil, fmt.Errorf("%w: tool %q changed while snapshotting", ErrToolCatalogUnavailable, seed.name)
		}
	}
	r.mu.RUnlock()
	r.mediaApplyMu.Unlock()
	return &ModelToolCatalog{
		registry:    r,
		version:     version,
		mediaGen:    mediaGen,
		definitions: definitions,
		entries:     entries,
	}, nil
}

func safeToolDescriptorFromTool(tool Tool) (descriptor ToolDescriptor, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			descriptor = ToolDescriptor{}
			err = fmt.Errorf("tool descriptor panic")
		}
	}()
	return toolDescriptorFromTool(tool)
}

// ProviderDefinitions returns a detached, sorted definition slice.
func (catalog *ModelToolCatalog) ProviderDefinitions() []providers.ToolDefinition {
	if catalog == nil || len(catalog.definitions) == 0 {
		return nil
	}
	definitions := make([]providers.ToolDefinition, len(catalog.definitions))
	for index, definition := range catalog.definitions {
		definitions[index] = definition
		definitions[index].Function.Parameters = cloneToolDescriptor(ToolDescriptor{
			Parameters: definition.Function.Parameters,
		}).Parameters
	}
	return definitions
}

// Contains reports exact registry-key membership in the captured callable set.
func (catalog *ModelToolCatalog) Contains(name string) bool {
	if catalog == nil {
		return false
	}
	_, ok := catalog.entries[name]
	return ok
}

// Names returns the sorted exact registry keys in the captured callable set.
func (catalog *ModelToolCatalog) Names() []string {
	if catalog == nil || len(catalog.entries) == 0 {
		return nil
	}
	names := make([]string, 0, len(catalog.entries))
	for name := range catalog.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PreparedToolInvocation binds validated arguments to one exact catalog entry.
// Its fields are deliberately private; policy receives detached accessors only.
type PreparedToolInvocation struct {
	catalog            *ModelToolCatalog
	entry              modelToolCatalogEntry
	executionArguments map[string]any
	policyArguments    map[string]any
	used               atomic.Bool
}

// ClaimedToolInvocation is the single-use result of the final registry and
// visibility check. Claiming is the dispatch linearization point; callers may
// emit pre-effect telemetry after claim and then execute this exact captured
// tool without another name lookup.
type ClaimedToolInvocation struct {
	invocation *PreparedToolInvocation
	dispatched atomic.Bool
}

func (catalog *ModelToolCatalog) PrepareInvocation(
	name string,
	arguments map[string]any,
) (*PreparedToolInvocation, error) {
	if catalog == nil || catalog.registry == nil {
		return nil, fmt.Errorf("%w: catalog is nil", ErrToolCatalogUnavailable)
	}
	entry, ok := catalog.entries[name]
	if !ok {
		return nil, fmt.Errorf("%w: tool %q was not in the captured catalog", workflows.ErrToolCallNotDispatched, name)
	}
	executionArguments, err := DetachToolArguments(arguments)
	if err != nil {
		return nil, fmt.Errorf("%w: detach arguments for %q: %v", workflows.ErrToolCallNotDispatched, name, err)
	}
	if validationErr := validateToolArgs(entry.descriptor.Parameters, executionArguments); validationErr != nil {
		return nil, fmt.Errorf(
			"%w: argument validation failed for %q: %v",
			workflows.ErrToolCallNotDispatched,
			name,
			validationErr,
		)
	}
	if staleErr := catalog.validateEntry(entry); staleErr != nil {
		return nil, staleErr
	}
	policyArguments, _ := DetachToolArguments(executionArguments)
	return &PreparedToolInvocation{
		catalog:            catalog,
		entry:              entry,
		executionArguments: executionArguments,
		policyArguments:    policyArguments,
	}, nil
}

func (catalog *ModelToolCatalog) validateEntry(entry modelToolCatalogEntry) error {
	r := catalog.registry
	r.mu.RLock()
	defer r.mu.RUnlock()
	return catalog.validateEntryLocked(entry)
}

func (catalog *ModelToolCatalog) validateEntryLocked(entry modelToolCatalogEntry) error {
	r := catalog.registry
	current := r.tools[entry.name]
	if r.closed || r.mediaGen != catalog.mediaGen ||
		current != entry.entry || current == nil || isTypedNil(current.Tool) ||
		!sameToolBinding(current.Tool, entry.tool) || current.descriptor != entry.descriptorIdentity ||
		current.traits != entry.traits || current.IsCore != entry.core ||
		current.visibilityRevision != entry.visibilityRevision ||
		!current.IsCore && current.TTL <= 0 {
		return fmt.Errorf("%w: tool %q changed", ErrToolInvocationStale, entry.name)
	}
	return nil
}

func sameToolBinding(left, right Tool) bool {
	if isTypedNil(left) || isTypedNil(right) || reflect.TypeOf(left) != reflect.TypeOf(right) {
		return false
	}
	typeOf := reflect.TypeOf(left)
	if typeOf.Comparable() {
		return reflect.ValueOf(left).Interface() == reflect.ValueOf(right).Interface()
	}
	if typeOf.Kind() == reflect.Pointer || typeOf.Kind() == reflect.Map ||
		typeOf.Kind() == reflect.Slice || typeOf.Kind() == reflect.Func ||
		typeOf.Kind() == reflect.Chan {
		return samePointerIdentity(left, right)
	}
	// Public registry mutation replaces the enclosing *ToolEntry. A
	// non-comparable value receiver has no separately observable identity, so
	// exact entry identity is the strongest compatible fence.
	return true
}

func (invocation *PreparedToolInvocation) Name() string {
	if invocation == nil {
		return ""
	}
	return invocation.entry.name
}

func (invocation *PreparedToolInvocation) Traits() ToolTraits {
	if invocation == nil {
		return ToolTraits{}
	}
	return invocation.entry.traits
}

// PolicyArguments returns a new detached copy; mutation cannot affect dispatch.
func (invocation *PreparedToolInvocation) PolicyArguments() (map[string]any, error) {
	if invocation == nil {
		return nil, fmt.Errorf("%w: invocation is nil", ErrToolInvocationStale)
	}
	return DetachToolArguments(invocation.policyArguments)
}

// ValidateOfferedDefinition verifies that the exact execution arguments also
// satisfy the authoritative successful-provider schema. Registry validation
// alone is insufficient when outward adaptation narrows a schema.
func (invocation *PreparedToolInvocation) ValidateOfferedDefinition(
	definition providers.ToolDefinition,
) error {
	if invocation == nil {
		return fmt.Errorf("%w: invocation is nil", ErrToolInvocationStale)
	}
	if definition.Function.Name != invocation.entry.name {
		return fmt.Errorf("%w: offered name does not match prepared tool", workflows.ErrToolCallNotDispatched)
	}
	if err := validateToolArgs(definition.Function.Parameters, invocation.executionArguments); err != nil {
		return fmt.Errorf(
			"%w: offered argument validation failed for %q: %v",
			workflows.ErrToolCallNotDispatched,
			invocation.entry.name,
			err,
		)
	}
	return nil
}

// DispatchPrepared consumes and executes the exact prepared entry after one
// final registry/visibility recheck. It never performs a fresh name lookup.
func (r *ToolRegistry) DispatchPrepared(
	ctx context.Context,
	invocation *PreparedToolInvocation,
	channel, chatID string,
	asyncCallback AsyncCallback,
	suppressLogDetails bool,
) (*ToolResult, error) {
	claim, err := r.ClaimPrepared(ctx, invocation)
	if err != nil {
		return nil, err
	}
	return r.DispatchClaimed(
		ctx,
		claim,
		channel,
		chatID,
		asyncCallback,
		suppressLogDetails,
	)
}

// ClaimPrepared consumes a prepared invocation and performs its final exact
// registry/visibility/media/context check without starting the tool effect.
func (r *ToolRegistry) ClaimPrepared(
	ctx context.Context,
	invocation *PreparedToolInvocation,
) (*ClaimedToolInvocation, error) {
	if invocation == nil || invocation.catalog == nil || invocation.catalog.registry != r {
		return nil, fmt.Errorf("%w: invocation does not belong to registry", ErrToolInvocationStale)
	}
	if !invocation.used.CompareAndSwap(false, true) {
		return nil, ErrToolInvocationUsed
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", ErrToolInvocationStale)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mediaApplyMu.Lock()
	r.mu.RLock()
	validationErr := invocation.catalog.validateEntryLocked(invocation.entry)
	if validationErr == nil {
		validationErr = ctx.Err()
	}
	r.mu.RUnlock()
	r.mediaApplyMu.Unlock()
	if validationErr != nil {
		return nil, validationErr
	}
	return &ClaimedToolInvocation{invocation: invocation}, nil
}

// DispatchClaimed executes one exact claimed invocation. Registry lifecycle
// quiescence remains caller-owned; a claim is not a lease against Close.
func (r *ToolRegistry) DispatchClaimed(
	ctx context.Context,
	claim *ClaimedToolInvocation,
	channel, chatID string,
	asyncCallback AsyncCallback,
	suppressLogDetails bool,
) (*ToolResult, error) {
	if claim == nil || claim.invocation == nil || claim.invocation.catalog == nil ||
		claim.invocation.catalog.registry != r {
		return nil, fmt.Errorf("%w: claim does not belong to registry", ErrToolInvocationStale)
	}
	if !claim.dispatched.CompareAndSwap(false, true) {
		return nil, ErrToolInvocationUsed
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", ErrToolInvocationStale)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	invocation := claim.invocation
	effectiveSuppressed := suppressLogDetails || ToolLogDetailsSuppressed(ctx)

	r.logToolExecutionStart(
		ctx,
		invocation.entry.name,
		invocation.executionArguments,
		effectiveSuppressed,
	)
	diagnosticCap := r.diagnosticPolicyForContext(ctx, effectiveSuppressed)
	return executeResolvedToolWithContext(
		ctx,
		invocation.entry.name,
		invocation.executionArguments,
		invocation.entry.tool,
		invocation.entry.mediaStore,
		channel,
		chatID,
		asyncCallback,
		diagnosticCap,
		effectiveSuppressed,
	), nil
}
