package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/media"
)

type transactionTestFactory struct {
	descriptor ToolDescriptor
	traits     ToolTraits
	newCalls   *atomic.Int64
}

func (factory *transactionTestFactory) Descriptor() ToolDescriptor {
	return cloneToolDescriptor(factory.descriptor)
}

func (factory *transactionTestFactory) Traits() ToolTraits { return factory.traits }

func (factory *transactionTestFactory) New(ToolBuildContext) (Tool, error) {
	if factory.newCalls != nil {
		factory.newCalls.Add(1)
	}
	return nil, errors.New("transaction must not construct a tool")
}

type transactionMediaTool struct {
	mu         sync.RWMutex
	name       string
	desc       string
	params     map[string]any
	store      media.MediaStore
	setHook    func(*transactionMediaTool, media.MediaStore)
	setCalls   atomic.Int64
	closeCalls atomic.Int64
}

func newTransactionMediaTool(name string) *transactionMediaTool {
	return &transactionMediaTool{
		name: name, desc: name,
		params: map[string]any{"type": "object"},
	}
}

func (tool *transactionMediaTool) Name() string {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return tool.name
}

func (tool *transactionMediaTool) Description() string {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return tool.desc
}

func (tool *transactionMediaTool) Parameters() map[string]any {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return tool.params
}

func (*transactionMediaTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("transaction")
}

func (tool *transactionMediaTool) SetMediaStore(store media.MediaStore) {
	tool.mu.Lock()
	tool.store = store
	hook := tool.setHook
	tool.mu.Unlock()
	tool.setCalls.Add(1)
	if hook != nil {
		hook(tool, store)
	}
}

func (tool *transactionMediaTool) mediaStore() media.MediaStore {
	tool.mu.RLock()
	defer tool.mu.RUnlock()
	return tool.store
}

func (tool *transactionMediaTool) Close() error {
	tool.closeCalls.Add(1)
	return nil
}

func transactionFactoryForTool(
	t *testing.T,
	tool Tool,
	newCalls *atomic.Int64,
) ToolFactory {
	t.Helper()
	descriptor, err := safeToolDescriptor(tool)
	if err != nil {
		t.Fatal(err)
	}
	return &transactionTestFactory{
		descriptor: descriptor,
		traits: ToolTraits{
			Sharing: ToolSharingPerOwner,
		},
		newCalls: newCalls,
	}
}

func transactionInstall(
	t *testing.T,
	tool Tool,
	hidden bool,
	expected Tool,
	newCalls *atomic.Int64,
) FactoryBackedInstall {
	t.Helper()
	return FactoryBackedInstall{
		Live: tool, Factory: transactionFactoryForTool(t, tool, newCalls),
		Hidden: hidden, Expected: expected,
	}
}

func TestInstallFactoryBackedTransactionEmptyAndStructuralValidation(t *testing.T) {
	admissions, err := InstallFactoryBackedTransaction(nil)
	if err != nil || admissions == nil || len(admissions) != 0 {
		t.Fatalf("empty transaction = %#v, %v", admissions, err)
	}

	validRegistry := NewToolRegistry()
	validLive := newMockTool("transaction_valid", "transaction_valid")
	valid := transactionInstall(t, validLive, false, nil, nil)
	var nilLive *mockRegistryTool
	var nilFactory *transactionTestFactory
	var nilExpected *mockRegistryTool
	owned, ownedErr := NewOwnedToolRegistry(
		factoryTestOwner(ToolOwnerScopeRegistry, "transaction-validation"),
	)
	if ownedErr != nil {
		t.Fatal(ownedErr)
	}
	defer func() { _ = owned.Close() }()
	closed := NewToolRegistry()
	closed.mu.Lock()
	closed.closed = true
	closed.mu.Unlock()
	zero := &ToolRegistry{}

	tests := []struct {
		name    string
		batches []FactoryBackedBatch
	}{
		{name: "nil registry", batches: []FactoryBackedBatch{{
			Installs: []FactoryBackedInstall{valid},
		}}},
		{name: "duplicate registry batch", batches: []FactoryBackedBatch{
			{Registry: validRegistry}, {Registry: validRegistry},
		}},
		{name: "empty owned registry batch", batches: []FactoryBackedBatch{{Registry: owned}}},
		{name: "empty closed registry batch", batches: []FactoryBackedBatch{{Registry: closed}}},
		{name: "empty zero registry batch", batches: []FactoryBackedBatch{{Registry: zero}}},
		{name: "typed nil live", batches: []FactoryBackedBatch{{
			Registry: validRegistry,
			Installs: []FactoryBackedInstall{{Live: nilLive, Factory: valid.Factory}},
		}}},
		{name: "typed nil factory", batches: []FactoryBackedBatch{{
			Registry: validRegistry,
			Installs: []FactoryBackedInstall{{Live: validLive, Factory: nilFactory}},
		}}},
		{name: "typed nil expected", batches: []FactoryBackedBatch{{
			Registry: validRegistry,
			Installs: []FactoryBackedInstall{{
				Live: validLive, Factory: valid.Factory, Expected: nilExpected,
			}},
		}}},
		{name: "value live", batches: []FactoryBackedBatch{{
			Registry: validRegistry,
			Installs: []FactoryBackedInstall{{
				Live:    factoryValueTool{},
				Factory: transactionFactoryForTool(t, factoryValueTool{}, nil),
			}},
		}}},
		{name: "owned registry", batches: []FactoryBackedBatch{{
			Registry: owned, Installs: []FactoryBackedInstall{valid},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, callErr := InstallFactoryBackedTransaction(test.batches); callErr == nil || got != nil {
				t.Fatalf("validation result = %#v, %v", got, callErr)
			}
		})
	}

	duplicateLive := newMockTool("transaction_duplicate", "transaction_duplicate")
	duplicate := transactionInstall(t, duplicateLive, false, nil, nil)
	if got, duplicateErr := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: NewToolRegistry(),
		Installs: []FactoryBackedInstall{duplicate, duplicate},
	}}); duplicateErr == nil || got != nil {
		t.Fatalf("duplicate name result = %#v, %v", got, duplicateErr)
	}

	valueExpectedRegistry := NewToolRegistry()
	valueExpectedRegistry.Register(factoryValueTool{})
	replacement := newMockTool("value_tool", "value tool")
	valueExpected := transactionInstall(t, replacement, false, factoryValueTool{}, nil)
	if got, expectedErr := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: valueExpectedRegistry, Installs: []FactoryBackedInstall{valueExpected},
	}}); expectedErr == nil || got != nil {
		t.Fatalf("value expected result = %#v, %v", got, expectedErr)
	}

	deniedValueExpected := NewToolRegistry()
	deniedValueExpected.SetAllowlist([]string{})
	if got, expectedErr := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: deniedValueExpected, Installs: []FactoryBackedInstall{valueExpected},
	}}); expectedErr == nil || got != nil {
		t.Fatalf("denied value expected result = %#v, %v", got, expectedErr)
	}
}

func TestInstallFactoryBackedTransactionAdmissionInsertAndMedia(t *testing.T) {
	left := NewToolRegistry()
	right := NewToolRegistry()
	leftStore := media.NewFileMediaStore()
	rightStore := media.NewFileMediaStore()
	left.SetMediaStore(leftStore)
	right.SetMediaStore(rightStore)
	left.SetAllowlist([]string{"transaction_left", "transaction_hidden"})
	right.SetAllowlist([]string{})

	leftLive := newTransactionMediaTool("transaction_left")
	hiddenLive := newTransactionMediaTool("transaction_hidden")
	deniedLive := &coveragePanicMediaTool{
		mockRegistryTool: newMockTool("transaction_denied", "transaction_denied"),
	}
	var newCalls atomic.Int64
	beforeLeft, beforeRight := left.Version(), right.Version()
	admissions, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{
		{Registry: left, Installs: []FactoryBackedInstall{
			transactionInstall(t, leftLive, false, nil, &newCalls),
			transactionInstall(t, hiddenLive, true, nil, &newCalls),
		}},
		{Registry: right, Installs: []FactoryBackedInstall{
			transactionInstall(t, deniedLive, false, newMockTool("wrong", "wrong"), &newCalls),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAdmissions := []FactoryBackedAdmission{
		{BatchIndex: 0, InstallIndex: 0, Name: "transaction_left", Admitted: true},
		{BatchIndex: 0, InstallIndex: 1, Name: "transaction_hidden", Admitted: true},
		{BatchIndex: 1, InstallIndex: 0, Name: "transaction_denied"},
	}
	if len(admissions) != len(wantAdmissions) {
		t.Fatalf("admissions = %#v", admissions)
	}
	for index := range wantAdmissions {
		if admissions[index] != wantAdmissions[index] {
			t.Fatalf("admission %d = %#v, want %#v", index, admissions[index], wantAdmissions[index])
		}
	}
	if newCalls.Load() != 0 || deniedLive.closeCalls.Load() != 0 {
		t.Fatalf("factory/denied side effects = new:%d close:%d", newCalls.Load(), deniedLive.closeCalls.Load())
	}
	if left.Version() != beforeLeft+2 || right.Version() != beforeRight {
		t.Fatalf("versions = left:%d right:%d", left.Version(), right.Version())
	}
	leftEntry := left.tools["transaction_left"]
	hiddenEntry := left.tools["transaction_hidden"]
	if leftEntry == nil || leftEntry.Tool != leftLive || !leftEntry.IsCore || leftEntry.TTL != 0 ||
		hiddenEntry == nil || hiddenEntry.Tool != hiddenLive || hiddenEntry.IsCore || hiddenEntry.TTL != 0 {
		t.Fatalf("installed entries = left:%#v hidden:%#v", leftEntry, hiddenEntry)
	}
	if leftLive.mediaStore() != leftStore || hiddenLive.mediaStore() != leftStore ||
		leftLive.setCalls.Load() != 1 || hiddenLive.setCalls.Load() != 1 ||
		deniedLive.closeCalls.Load() != 0 {
		t.Fatal("media injection or denied admission side effects are incorrect")
	}
	if right.HasRegistered("transaction_denied") {
		t.Fatal("allowlist-denied install was published")
	}
	if closeErr := left.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if leftLive.closeCalls.Load() != 0 || hiddenLive.closeCalls.Load() != 0 {
		t.Fatal("compatibility registry closed caller-owned live tools")
	}
}

func TestInstallFactoryBackedTransactionDeniedSkipsLiveAndCatalog(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetAllowlist([]string{})
	dormant := newMockTool("transaction_denied_panic", "transaction_denied_panic")
	dormantFactory := transactionFactoryForTool(t, dormant, nil)
	if err := registry.RegisterFactoryDependency(dormantFactory); err != nil {
		t.Fatal(err)
	}
	catalog := registry.constructionCatalog["transaction_denied_panic"]
	version := registry.Version()
	panicLive := &factoryMetadataTool{panicNow: true}
	admissions, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: registry,
		Installs: []FactoryBackedInstall{{
			Live: panicLive,
			Factory: &transactionTestFactory{
				descriptor: coverageFactoryDescriptor("transaction_denied_panic"),
				traits:     ToolTraits{Sharing: ToolSharingPerOwner},
			},
			Expected: newMockTool("irrelevant", "irrelevant"),
		}},
	}})
	if err != nil || len(admissions) != 1 || admissions[0].Admitted ||
		admissions[0].Replaced {
		t.Fatalf("denied panicking live admission = %#v, %v", admissions, err)
	}
	if registry.Version() != version ||
		registry.constructionCatalog["transaction_denied_panic"] != catalog ||
		registry.HasRegistered("transaction_denied_panic") {
		t.Fatal("denied install changed the dormant catalog or registry version")
	}
}

func TestInstallFactoryBackedTransactionExactReplacementRetainsOldLease(t *testing.T) {
	source := NewToolRegistry()
	oldLive := newTransactionMediaTool("transaction_replace")
	oldFactory := transactionFactoryForTool(t, oldLive, nil)
	if err := source.RegisterFactoryBacked(oldLive, oldFactory); err != nil {
		t.Fatal(err)
	}
	oldEntry := source.tools["transaction_replace"]
	oldReservationCount := len(source.compatibilityReservations)
	newLive := newTransactionMediaTool("transaction_replace")
	newFactory := transactionFactoryForTool(t, newLive, nil)
	before := source.Version()
	admissions, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: source,
		Installs: []FactoryBackedInstall{{
			Live: newLive, Factory: newFactory, Hidden: true, Expected: oldLive,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(admissions) != 1 || !admissions[0].Admitted || !admissions[0].Replaced {
		t.Fatalf("replacement admission = %#v", admissions)
	}
	entry := source.tools["transaction_replace"]
	if entry == nil || entry == oldEntry || entry.Tool != newLive || entry.IsCore ||
		source.Version() != before+1 ||
		len(source.compatibilityReservations) != oldReservationCount+1 {
		t.Fatalf(
			"replacement state = entry:%#v version:%d leases:%d",
			entry,
			source.Version(),
			len(source.compatibilityReservations),
		)
	}

	for label, probe := range map[string]Tool{"old": oldLive, "new": newLive} {
		registry := NewToolRegistry()
		factory := oldFactory
		if label == "new" {
			factory = newFactory
		}
		if registerErr := registry.RegisterFactoryBacked(probe, factory); registerErr == nil {
			t.Fatalf("%s live pointer lost its source lease", label)
		}
	}
	if closeErr := source.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if oldLive.closeCalls.Load() != 0 || newLive.closeCalls.Load() != 0 {
		t.Fatal("replacement or close closed a caller-owned wrapper")
	}
	for label, probe := range map[string]Tool{"old": oldLive, "new": newLive} {
		registry := NewToolRegistry()
		factory := oldFactory
		if label == "new" {
			factory = newFactory
		}
		if registerErr := registry.RegisterFactoryBacked(probe, factory); registerErr != nil {
			t.Fatalf("%s lease survived source close: %v", label, registerErr)
		}
		_ = registry.Close()
	}
}

func TestInstallFactoryBackedTransactionInitialOccupantCASFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected func() Tool
	}{
		{name: "insert into occupied slot"},
		{name: "wrong replacement pointer", expected: func() Tool {
			return newMockTool("transaction_initial_cas", "transaction_initial_cas")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewToolRegistry()
			occupant := newMockTool("transaction_initial_cas", "transaction_initial_cas")
			occupantFactory := transactionFactoryForTool(t, occupant, nil)
			if err := registry.RegisterFactoryBacked(occupant, occupantFactory); err != nil {
				t.Fatal(err)
			}
			candidate := newMockTool("transaction_initial_cas", "transaction_initial_cas")
			candidateFactory := transactionFactoryForTool(t, candidate, nil)
			var expected Tool
			if test.expected != nil {
				expected = test.expected()
			}
			entry := registry.tools["transaction_initial_cas"]
			version := registry.Version()
			got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
				Registry: registry,
				Installs: []FactoryBackedInstall{{
					Live: candidate, Factory: candidateFactory, Expected: expected,
				}},
			}})
			if err == nil || got != nil {
				t.Fatalf("initial CAS result = %#v, %v", got, err)
			}
			if registry.tools["transaction_initial_cas"] != entry ||
				registry.Version() != version {
				t.Fatal("initial CAS failure changed the occupant or version")
			}
			probe := NewToolRegistry()
			if registerErr := probe.RegisterFactoryBacked(
				candidate,
				candidateFactory,
			); registerErr != nil {
				t.Fatalf("initial CAS failure reserved the candidate: %v", registerErr)
			}
			_ = probe.Close()
			_ = registry.Close()
		})
	}
}

func TestInstallFactoryBackedTransactionReplacementRejectsCatalogAmbiguity(t *testing.T) {
	registry := NewToolRegistry()
	old := newMockTool("transaction_ambiguous", "transaction_ambiguous")
	oldFactory := transactionFactoryForTool(t, old, nil)
	if err := registry.RegisterFactoryBacked(old, oldFactory); err != nil {
		t.Fatal(err)
	}
	replacement := newMockTool("transaction_ambiguous", "transaction_ambiguous")
	replacementFactory := transactionFactoryForTool(t, replacement, nil)
	descriptor, traits, metadataErr := snapshotFactoryMetadata(replacementFactory)
	if metadataErr != nil {
		t.Fatal(metadataErr)
	}
	frozen := cloneToolDescriptor(descriptor)
	registry.mu.Lock()
	registry.constructionCatalog[descriptor.Name] = &ToolEntry{
		descriptor: &frozen, traits: traits, factory: replacementFactory,
	}
	registry.mu.Unlock()
	oldEntry := registry.tools[descriptor.Name]
	version := registry.Version()
	got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: registry,
		Installs: []FactoryBackedInstall{{
			Live: replacement, Factory: replacementFactory, Expected: old,
		}},
	}})
	if err == nil || got != nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous replacement result = %#v, %v", got, err)
	}
	if registry.tools[descriptor.Name] != oldEntry || registry.Version() != version {
		t.Fatal("ambiguous replacement changed the public occupant or version")
	}
	_ = registry.Close()
}

func TestInstallFactoryBackedTransactionRollbackFences(t *testing.T) {
	tests := []struct {
		name       string
		seed       func(*testing.T, *ToolRegistry) Tool
		mutate     func(*ToolRegistry, string, *transactionMediaTool)
		wantErr    string
		wantClosed bool
	}{
		{
			name: "late collision",
			mutate: func(registry *ToolRegistry, name string, _ *transactionMediaTool) {
				registry.Register(newMockTool(name, name))
			},
			wantErr: "changed during installation",
		},
		{
			name: "allowlist",
			mutate: func(registry *ToolRegistry, _ string, _ *transactionMediaTool) {
				registry.SetAllowlist([]string{})
			},
			wantErr: "allowlist admission changed",
		},
		{
			name: "media generation",
			mutate: func(registry *ToolRegistry, _ string, _ *transactionMediaTool) {
				registry.mu.Lock()
				registry.mediaGen++
				registry.mu.Unlock()
			},
			wantErr: "media generation changed",
		},
		{
			name: "close",
			seed: func(t *testing.T, registry *ToolRegistry) Tool {
				seed := newMockTool("transaction_close_seed", "transaction_close_seed")
				if err := registry.RegisterFactoryBacked(seed, transactionFactoryForTool(t, seed, nil)); err != nil {
					t.Fatal(err)
				}
				return seed
			},
			mutate: func(registry *ToolRegistry, _ string, _ *transactionMediaTool) {
				_ = registry.Close()
			},
			wantErr: "closed during installation", wantClosed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left := NewToolRegistry()
			right := NewToolRegistry()
			seed := Tool(nil)
			if test.seed != nil {
				seed = test.seed(t, right)
			}
			leftLive := newTransactionMediaTool("transaction_atomic_left_" + strings.ReplaceAll(test.name, " ", "_"))
			rightName := "transaction_atomic_right_" + strings.ReplaceAll(test.name, " ", "_")
			rightLive := newTransactionMediaTool(rightName)
			rightLive.setHook = func(tool *transactionMediaTool, _ media.MediaStore) {
				test.mutate(right, rightName, tool)
			}
			leftBefore, rightBefore := left.Version(), right.Version()
			got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{
				{Registry: left, Installs: []FactoryBackedInstall{
					transactionInstall(t, leftLive, false, nil, nil),
				}},
				{Registry: right, Installs: []FactoryBackedInstall{
					transactionInstall(t, rightLive, false, nil, nil),
				}},
			})
			if err == nil || got != nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("rollback result = %#v, %v", got, err)
			}
			if left.HasRegistered(leftLive.Name()) {
				t.Fatal("cross-registry rollback partially published the left install")
			}
			if left.Version() != leftBefore {
				t.Fatalf("rolled-back left version = %d, want %d", left.Version(), leftBefore)
			}
			if !test.wantClosed && right.Version() == rightBefore && test.name == "late collision" {
				t.Fatal("late collision test did not mutate its registry")
			}
			probe := NewToolRegistry()
			if registerErr := probe.RegisterFactoryBacked(
				leftLive,
				transactionFactoryForTool(t, leftLive, nil),
			); registerErr != nil {
				t.Fatalf("rollback retained the first candidate lease: %v", registerErr)
			}
			_ = probe.Close()
			if seed != nil {
				_ = seed
			}
		})
	}
}

func TestInstallFactoryBackedTransactionReplacementVisibilityCASRollback(t *testing.T) {
	registry := NewToolRegistry()
	defer func() { _ = registry.Close() }()
	old := newMockTool("transaction_visibility", "transaction_visibility")
	if err := registry.RegisterHiddenFactoryBacked(old, transactionFactoryForTool(t, old, nil)); err != nil {
		t.Fatal(err)
	}
	registry.PromoteTools([]string{"transaction_visibility"}, 3)
	oldEntry := registry.tools["transaction_visibility"]
	oldVersion := registry.Version()
	candidate := newTransactionMediaTool("transaction_visibility")
	candidate.setHook = func(*transactionMediaTool, media.MediaStore) {
		registry.PromoteTools([]string{"transaction_visibility"}, 2)
	}
	got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: registry,
		Installs: []FactoryBackedInstall{
			transactionInstall(t, candidate, true, old, nil),
		},
	}})
	if err == nil || got != nil || !strings.Contains(err.Error(), "visibility changed") {
		t.Fatalf("visibility CAS result = %#v, %v", got, err)
	}
	if registry.tools["transaction_visibility"] != oldEntry ||
		registry.tools["transaction_visibility"].Tool != old ||
		registry.Version() != oldVersion {
		t.Fatal("visibility CAS failure replaced the prior occupant or version")
	}
	probe := NewToolRegistry()
	if registerErr := probe.RegisterFactoryBacked(
		candidate,
		transactionFactoryForTool(t, candidate, nil),
	); registerErr != nil {
		t.Fatalf("visibility rollback leaked candidate lease: %v", registerErr)
	}
	_ = probe.Close()
}

func TestInstallFactoryBackedTransactionLateOccupantCASRollback(t *testing.T) {
	registry := NewToolRegistry()
	old := newMockTool("transaction_occupant_cas", "transaction_occupant_cas")
	if err := registry.RegisterFactoryBacked(old, transactionFactoryForTool(t, old, nil)); err != nil {
		t.Fatal(err)
	}
	candidate := newTransactionMediaTool("transaction_occupant_cas")
	factory := transactionFactoryForTool(t, candidate, nil)
	interloper := newMockTool("transaction_occupant_cas", "transaction_occupant_cas")
	candidate.setHook = func(*transactionMediaTool, media.MediaStore) {
		registry.mu.Lock()
		registry.tools["transaction_occupant_cas"] = &ToolEntry{
			Tool: interloper, IsCore: true, traits: conservativeLegacyToolTraits(),
		}
		registry.mu.Unlock()
	}
	version := registry.Version()
	got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: registry,
		Installs: []FactoryBackedInstall{{
			Live: candidate, Factory: factory, Expected: old,
		}},
	}})
	if err == nil || got != nil || !strings.Contains(err.Error(), "occupant changed") {
		t.Fatalf("late occupant CAS result = %#v, %v", got, err)
	}
	if registry.tools["transaction_occupant_cas"].Tool != interloper ||
		registry.Version() != version {
		t.Fatal("late occupant CAS rollback overwrote external state or changed version")
	}
	candidate.mu.Lock()
	candidate.setHook = nil
	candidate.mu.Unlock()
	probe := NewToolRegistry()
	if registerErr := probe.RegisterFactoryBacked(candidate, factory); registerErr != nil {
		t.Fatalf("late occupant CAS rollback leaked candidate lease: %v", registerErr)
	}
	_ = probe.Close()
	_ = registry.Close()
}

func TestInstallFactoryBackedTransactionMediaFailureAndDescriptorMutation(t *testing.T) {
	t.Run("setter panic", func(t *testing.T) {
		registry := NewToolRegistry()
		candidate := &coveragePanicMediaTool{
			mockRegistryTool: newMockTool("transaction_media_panic", "transaction_media_panic"),
		}
		factory := transactionFactoryForTool(t, candidate, nil)
		got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
			Registry: registry,
			Installs: []FactoryBackedInstall{{Live: candidate, Factory: factory}},
		}})
		if err == nil || got != nil || registry.HasRegistered("transaction_media_panic") ||
			candidate.closeCalls.Load() != 0 {
			t.Fatalf("setter panic result = %#v, %v", got, err)
		}
		probe := NewToolRegistry()
		if registerErr := probe.RegisterFactoryBacked(candidate, factory); registerErr == nil {
			// The lease was available; this registration reaches the intentional setter panic.
			t.Fatal("panicking media candidate unexpectedly registered")
		} else if strings.Contains(registerErr.Error(), "reused an instance") {
			t.Fatalf("setter panic leaked candidate lease: %v", registerErr)
		}
	})

	t.Run("setter mutates descriptor", func(t *testing.T) {
		registry := NewToolRegistry()
		candidate := newTransactionMediaTool("transaction_media_mutation")
		factory := transactionFactoryForTool(t, candidate, nil)
		candidate.setHook = func(tool *transactionMediaTool, _ media.MediaStore) {
			tool.mu.Lock()
			tool.desc = "mutated by setter"
			tool.mu.Unlock()
		}
		got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
			Registry: registry,
			Installs: []FactoryBackedInstall{{Live: candidate, Factory: factory}},
		}})
		if err == nil || got != nil || !strings.Contains(err.Error(), "changed its descriptor") ||
			registry.HasRegistered("transaction_media_mutation") {
			t.Fatalf("descriptor mutation result = %#v, %v", got, err)
		}
		candidate.mu.Lock()
		candidate.desc = candidate.name
		candidate.setHook = nil
		candidate.mu.Unlock()
		probe := NewToolRegistry()
		if registerErr := probe.RegisterFactoryBacked(candidate, factory); registerErr != nil {
			t.Fatalf("descriptor rollback leaked candidate lease: %v", registerErr)
		}
		_ = probe.Close()
	})
}

func TestInstallFactoryBackedTransactionSerializesQueuedMediaUpdate(t *testing.T) {
	registry := NewToolRegistry()
	firstStore := media.NewFileMediaStore()
	queuedStore := media.NewFileMediaStore()
	registry.SetMediaStore(firstStore)
	candidate := newTransactionMediaTool("transaction_media_serialized")
	enteredSetter := make(chan struct{})
	releaseSetter := make(chan struct{})
	var first atomic.Bool
	candidate.setHook = func(*transactionMediaTool, media.MediaStore) {
		if first.CompareAndSwap(false, true) {
			close(enteredSetter)
			<-releaseSetter
		}
	}
	factory := transactionFactoryForTool(t, candidate, nil)
	transactionDone := make(chan error, 1)
	go func() {
		_, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
			Registry: registry,
			Installs: []FactoryBackedInstall{{Live: candidate, Factory: factory}},
		}})
		transactionDone <- err
	}()
	select {
	case <-enteredSetter:
	case <-time.After(3 * time.Second):
		t.Fatal("transaction did not enter media setter")
	}
	updateStarted := make(chan struct{})
	updateDone := make(chan struct{})
	go func() {
		close(updateStarted)
		registry.SetMediaStore(queuedStore)
		close(updateDone)
	}()
	<-updateStarted
	select {
	case <-updateDone:
		t.Fatal("queued media update bypassed the transaction media lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSetter)
	select {
	case err := <-transactionDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("transaction did not finish after releasing media setter")
	}
	select {
	case <-updateDone:
	case <-time.After(3 * time.Second):
		t.Fatal("queued media update did not finish")
	}
	if !registry.HasRegistered("transaction_media_serialized") ||
		candidate.mediaStore() != queuedStore || candidate.setCalls.Load() != 2 {
		t.Fatalf("queued media propagation = registered:%v store:%p calls:%d",
			registry.HasRegistered("transaction_media_serialized"),
			candidate.mediaStore(), candidate.setCalls.Load())
	}
	_ = registry.Close()
}

func TestInstallFactoryBackedTransactionLateCatalogCASRollback(t *testing.T) {
	registry := NewToolRegistry()
	candidate := newTransactionMediaTool("transaction_catalog_cas")
	factory := transactionFactoryForTool(t, candidate, nil)
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		t.Fatal(err)
	}
	descriptor, traits, metadataErr := snapshotFactoryMetadata(factory)
	if metadataErr != nil {
		t.Fatal(metadataErr)
	}
	originalCatalog := registry.constructionCatalog[descriptor.Name]
	candidate.setHook = func(*transactionMediaTool, media.MediaStore) {
		frozen := cloneToolDescriptor(descriptor)
		registry.mu.Lock()
		registry.constructionCatalog[descriptor.Name] = &ToolEntry{
			descriptor: &frozen, traits: traits, factory: factory,
		}
		registry.mu.Unlock()
	}
	version := registry.Version()
	got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: registry,
		Installs: []FactoryBackedInstall{{Live: candidate, Factory: factory}},
	}})
	if err == nil || got != nil || !strings.Contains(err.Error(), "changed during public registration") {
		t.Fatalf("late catalog CAS result = %#v, %v", got, err)
	}
	if registry.HasRegistered(descriptor.Name) || registry.Version() != version ||
		registry.constructionCatalog[descriptor.Name] == originalCatalog {
		t.Fatal("late catalog CAS did not preserve the externally changed catalog state")
	}
	candidate.mu.Lock()
	candidate.setHook = nil
	candidate.mu.Unlock()
	probe := NewToolRegistry()
	if registerErr := probe.RegisterFactoryBacked(candidate, factory); registerErr != nil {
		t.Fatalf("late catalog rollback leaked candidate lease: %v", registerErr)
	}
	_ = probe.Close()
}

func TestInstallFactoryBackedTransactionPartialReservationRollback(t *testing.T) {
	first := newTransactionMediaTool("transaction_reservation_first")
	second := newTransactionMediaTool("transaction_reservation_second")
	firstFactory := transactionFactoryForTool(t, first, nil)
	secondFactory := transactionFactoryForTool(t, second, nil)
	foreign := NewToolRegistry()
	if err := foreign.RegisterFactoryBacked(second, secondFactory); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	target := NewToolRegistry()
	got, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: target,
		Installs: []FactoryBackedInstall{
			{Live: first, Factory: firstFactory},
			{Live: second, Factory: secondFactory},
		},
	}})
	if err == nil || got != nil || target.Count() != 0 || target.Version() != 0 {
		t.Fatalf("partial reservation result = %#v, %v", got, err)
	}
	probe := NewToolRegistry()
	if registerErr := probe.RegisterFactoryBacked(first, firstFactory); registerErr != nil {
		t.Fatalf("partial reservation failure retained first lease: %v", registerErr)
	}
	_ = probe.Close()
}

func TestInstallFactoryBackedTransactionDormantPromotionAndPrivateCollision(t *testing.T) {
	registry := NewToolRegistry()
	live := newMockTool("transaction_dormant", "transaction_dormant")
	factory := transactionFactoryForTool(t, live, nil)
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		t.Fatal(err)
	}
	before := registry.Version()
	admissions, err := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: registry,
		Installs: []FactoryBackedInstall{{Live: live, Factory: factory}},
	}})
	if err != nil || len(admissions) != 1 || !admissions[0].Admitted ||
		registry.constructionCatalog["transaction_dormant"] != nil ||
		registry.tools["transaction_dormant"] == nil || registry.Version() != before+1 {
		t.Fatalf("dormant promotion = %#v, %v", admissions, err)
	}
	_ = registry.Close()

	private := NewToolRegistry()
	privateLive := newMockTool("transaction_private", "transaction_private")
	privateFactory := transactionFactoryForTool(t, privateLive, nil)
	private.privateConstruction["transaction_private"] = &ToolEntry{Tool: privateLive}
	if got, privateErr := InstallFactoryBackedTransaction([]FactoryBackedBatch{{
		Registry: private,
		Installs: []FactoryBackedInstall{{Live: privateLive, Factory: privateFactory}},
	}}); privateErr == nil || got != nil || private.tools["transaction_private"] != nil {
		t.Fatalf("private collision result = %#v, %v", got, privateErr)
	}
}

func TestInstallFactoryBackedTransactionCanonicalLockOrderStress(t *testing.T) {
	for iteration := 0; iteration < 25; iteration++ {
		left := NewToolRegistry()
		right := NewToolRegistry()
		leftA := newMockTool("transaction_left_a", "transaction_left_a")
		rightA := newMockTool("transaction_right_a", "transaction_right_a")
		leftB := newMockTool("transaction_left_b", "transaction_left_b")
		rightB := newMockTool("transaction_right_b", "transaction_right_b")
		forward := []FactoryBackedBatch{
			{Registry: left, Installs: []FactoryBackedInstall{transactionInstall(t, leftA, false, nil, nil)}},
			{Registry: right, Installs: []FactoryBackedInstall{transactionInstall(t, rightA, false, nil, nil)}},
		}
		reverse := []FactoryBackedBatch{
			{Registry: right, Installs: []FactoryBackedInstall{transactionInstall(t, rightB, false, nil, nil)}},
			{Registry: left, Installs: []FactoryBackedInstall{transactionInstall(t, leftB, false, nil, nil)}},
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := InstallFactoryBackedTransaction(forward)
			results <- err
		}()
		go func() {
			<-start
			_, err := InstallFactoryBackedTransaction(reverse)
			results <- err
		}()
		close(start)
		for completed := 0; completed < 2; completed++ {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("iteration %d transaction error: %v", iteration, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("iteration %d reversed registry order deadlocked", iteration)
			}
		}
		if left.Count() != 2 || right.Count() != 2 || left.Version() != 2 || right.Version() != 2 {
			t.Fatalf(
				"iteration %d state = left:%d/%d right:%d/%d",
				iteration,
				left.Count(),
				left.Version(),
				right.Count(),
				right.Version(),
			)
		}
		_ = left.Close()
		_ = right.Close()
	}
}
