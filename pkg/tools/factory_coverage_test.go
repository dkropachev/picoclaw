package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type factoryCoverageTool struct {
	name string
}

func (tool factoryCoverageTool) Name() string { return tool.name }

func (tool factoryCoverageTool) Description() string { return "coverage tool" }

func (tool factoryCoverageTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool factoryCoverageTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("coverage")
}

type factoryCoverageCloser struct {
	calls      atomic.Int64
	label      string
	order      *[]string
	closeErr   error
	panicValue any
}

func (closer *factoryCoverageCloser) Close() error {
	closer.calls.Add(1)
	if closer.order != nil {
		*closer.order = append(*closer.order, closer.label)
	}
	if closer.panicValue != nil {
		panic(closer.panicValue)
	}
	return closer.closeErr
}

func factoryCoverageDescriptor(name string) ToolDescriptor {
	return ToolDescriptor{
		Name: name, Description: "coverage tool",
		Parameters: map[string]any{"type": "object", "required": []string{"path"}},
	}
}

func TestToolFactoryCoverageConstructionAndNilReceivers(t *testing.T) {
	if _, err := NewToolFactory(ToolDescriptor{}, ToolTraits{}, func(ToolBuildContext) (Tool, error) {
		return &factoryCoverageTool{name: "unused"}, nil
	}); err == nil {
		t.Fatal("factory accepted an invalid descriptor")
	}
	if _, err := NewToolFactory(factoryCoverageDescriptor("bad_traits"), ToolTraits{
		Risk: ToolRiskClass("invalid"),
	}, func(ToolBuildContext) (Tool, error) {
		return &factoryCoverageTool{name: "bad_traits"}, nil
	}); err == nil {
		t.Fatal("factory accepted invalid traits")
	}
	if _, err := NewToolFactory(factoryCoverageDescriptor("shared"), ToolTraits{
		Sharing: ToolSharingImmutableShared,
	}, func(ToolBuildContext) (Tool, error) {
		return &factoryCoverageTool{name: "shared"}, nil
	}); err == nil || !strings.Contains(err.Error(), "per-owner factory") {
		t.Fatalf("immutable-sharing factory error = %v", err)
	}
	if _, err := NewToolFactory(factoryCoverageDescriptor("missing_build"), ToolTraits{}, nil); err == nil {
		t.Fatal("factory accepted a nil build function")
	}

	var builds atomic.Int64
	factory, err := NewToolFactory(
		factoryCoverageDescriptor("constructed"),
		ToolTraits{Risk: ToolRiskReadOnly, Parallel: ToolParallelSafe},
		func(ToolBuildContext) (Tool, error) {
			builds.Add(1)
			return &factoryCoverageTool{name: "constructed"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := factory.Descriptor()
	descriptor.Parameters["type"] = "mutated"
	descriptor.Parameters["required"].([]string)[0] = "mutated"
	second := factory.Descriptor()
	if second.Parameters["type"] != "object" || second.Parameters["required"].([]string)[0] != "path" {
		t.Fatalf("factory descriptor was not frozen: %#v", second.Parameters)
	}
	if got := factory.Traits(); got.Risk != ToolRiskReadOnly || got.Parallel != ToolParallelSafe ||
		got.Sharing != ToolSharingPerOwner {
		t.Fatalf("normalized traits = %#v", got)
	}
	tool, err := factory.New(ToolBuildContext{})
	if err != nil || tool.Name() != "constructed" || builds.Load() != 1 {
		t.Fatalf("factory New = tool:%#v builds:%d error:%v", tool, builds.Load(), err)
	}

	var nilFactory *toolFactory
	if got := nilFactory.Descriptor(); !reflect.DeepEqual(got, ToolDescriptor{}) {
		t.Fatalf("nil factory descriptor = %#v", got)
	}
	if got := nilFactory.Traits(); got != (ToolTraits{}) {
		t.Fatalf("nil factory traits = %#v", got)
	}
	if _, err := nilFactory.New(ToolBuildContext{}); err == nil {
		t.Fatal("nil factory New succeeded")
	}
	if _, err := (&toolFactory{}).New(ToolBuildContext{}); err == nil {
		t.Fatal("unconfigured factory New succeeded")
	}
}

func TestToolFactoryCoverageBuildContextMissingCapabilities(t *testing.T) {
	if _, err := (ToolBuildContext{}).Resolve("dependency"); err == nil {
		t.Fatal("inactive context resolved a dependency")
	}
	if _, err := (ToolBuildContext{}).Service("service", func() (any, error) {
		return "value", nil
	}); err == nil {
		t.Fatal("inactive context created a service")
	}
	if registry := destinationRegistryForBuild(ToolBuildContext{}); registry != nil {
		t.Fatalf("inactive context exposed registry %#v", registry)
	}

	missing, deactivateMissing := activateToolBuildContext(ToolBuildContext{})
	if _, err := missing.Resolve("dependency"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing resolver error = %v", err)
	}
	if _, err := missing.Service("service", func() (any, error) { return "value", nil }); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing service cache error = %v", err)
	}
	if registry := destinationRegistryForBuild(missing); registry != nil {
		t.Fatalf("missing destination registry = %#v", registry)
	}
	deactivateMissing()

	registry := NewToolRegistry()
	active, deactivateActive := activateToolBuildContext(ToolBuildContext{registry: registry})
	if got := destinationRegistryForBuild(active); got != registry {
		t.Fatalf("destination registry = %#v, want %#v", got, registry)
	}
	deactivateActive()
	if got := destinationRegistryForBuild(active); got != nil {
		t.Fatalf("sealed context exposed destination registry %#v", got)
	}
}

func TestToolFactoryCoverageInstanceTrackerBoundaries(t *testing.T) {
	var absent Tool
	if _, err := toolInstanceIdentity(absent); err == nil {
		t.Fatal("nil tool received an identity")
	}
	var nilPointer *factoryCoverageTool
	if _, err := toolInstanceIdentity(nilPointer); err == nil {
		t.Fatal("typed nil tool received an identity")
	}
	if _, err := toolInstanceIdentity(factoryCoverageTool{name: "value"}); err == nil ||
		!strings.Contains(err.Error(), "non-nil pointer") {
		t.Fatalf("value tool identity error = %v", err)
	}

	tool := &factoryCoverageTool{name: "pointer"}
	identity, err := toolInstanceIdentity(tool)
	if err != nil || identity.pointer == 0 || identity.typeOf != reflect.TypeOf(tool) {
		t.Fatalf("pointer identity = %#v, %v", identity, err)
	}
	tracker := newToolInstanceTracker()
	if err := tracker.reserve(identity, tool); err != nil {
		t.Fatal(err)
	}
	if err := tracker.reserve(identity, tool); err == nil || !strings.Contains(err.Error(), "another owner") {
		t.Fatalf("duplicate reservation error = %v", err)
	}
	tracker.release(toolInstanceKey{})
	var nilTracker *toolInstanceTracker
	nilTracker.release(identity)
	tracker.release(identity)
	if err := tracker.reserve(identity, tool); err != nil {
		t.Fatalf("released identity could not be reserved: %v", err)
	}
	tracker.release(identity)
	if len(tracker.issued) != 0 {
		t.Fatalf("tracker retained identities: %#v", tracker.issued)
	}
	if err := tracker.reserveImmutableShared(identity, tool); err != nil {
		t.Fatal(err)
	}
	if err := tracker.reserveImmutableShared(identity, tool); err != nil {
		t.Fatalf("second immutable share failed: %v", err)
	}
	if err := tracker.reserve(identity, tool); err == nil {
		t.Fatal("exclusive reservation bypassed immutable shares")
	}
	tracker.release(identity)
	if err := tracker.reserve(identity, tool); err == nil {
		t.Fatal("exclusive release removed immutable shares")
	}
	tracker.releaseImmutableShared(identity)
	if err := tracker.reserve(identity, tool); err == nil {
		t.Fatal("one immutable release removed two shares")
	}
	tracker.releaseImmutableShared(identity)
	if err := tracker.reserve(identity, tool); err != nil {
		t.Fatalf("all immutable shares did not release identity: %v", err)
	}
	if err := tracker.reserveImmutableShared(identity, tool); err == nil {
		t.Fatal("immutable share bypassed exclusive reservation")
	}
	tracker.releaseImmutableShared(identity)
	if err := tracker.reserve(identity, tool); err == nil {
		t.Fatal("immutable release removed an exclusive reservation")
	}
	tracker.releaseImmutableShared(toolInstanceKey{})
	nilTracker.releaseImmutableShared(identity)
	tracker.release(identity)
	tracker.issued[identity] = toolInstanceLease{
		value: &factoryCoverageTool{name: "different"}, immutableShares: 1,
	}
	if err := tracker.reserveImmutableShared(identity, tool); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed immutable identity error = %v", err)
	}
	tracker.issued[identity] = toolInstanceLease{
		value: tool, immutableShares: ^uint64(0),
	}
	if err := tracker.reserveImmutableShared(identity, tool); err == nil ||
		!strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("immutable lease overflow error = %v", err)
	}
	delete(tracker.issued, identity)
	if len(tracker.issued) != 0 {
		t.Fatalf("tracker retained mixed leases: %#v", tracker.issued)
	}
}

func TestToolFactoryCoverageServiceCacheAndTransactionBoundaries(t *testing.T) {
	var nilCache *toolServiceCache
	if snapshot := nilCache.snapshot(); snapshot != nil {
		t.Fatalf("nil cache snapshot = %#v", snapshot)
	}
	cache := newToolServiceCache()
	cache.values["base"] = "cached"
	snapshot := cache.snapshot()
	snapshot["base"] = "mutated"
	if got := cache.snapshot()["base"]; got != "cached" {
		t.Fatalf("cache snapshot aliased source: %#v", got)
	}

	var nilTransaction *toolServiceTransaction
	if _, err := nilTransaction.service("service", func() (any, error) { return "value", nil }); err == nil {
		t.Fatal("nil transaction created a service")
	}
	if err := nilTransaction.commit(cache); err != nil {
		t.Fatalf("nil transaction commit error = %v", err)
	}
	if err := newToolServiceTransaction(nil).commit(nil); err != nil {
		t.Fatalf("nil cache commit error = %v", err)
	}
	if created := nilTransaction.detachCreated(); created != nil {
		t.Fatalf("nil transaction created values = %#v", created)
	}
	if reservations := nilTransaction.detachReservations(); reservations != nil {
		t.Fatalf("nil transaction reservations = %#v", reservations)
	}
	nilTransaction.track("ignored")
	if err := nilTransaction.closeCreated(); err != nil {
		t.Fatalf("nil transaction close error = %v", err)
	}

	transaction := newToolServiceTransaction(map[string]any{"base": "cached"})
	if _, err := transaction.service("", func() (any, error) { return "unused", nil }); err == nil {
		t.Fatal("empty service key was accepted")
	}
	if got, err := transaction.service("base", func() (any, error) {
		panic("base value should bypass creator")
	}); err != nil || got != "cached" {
		t.Fatalf("base service = %#v, %v", got, err)
	}
	if _, err := transaction.service("missing", nil); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing service error = %v", err)
	}

	var creates atomic.Int64
	got, serviceErr := transaction.service("overlay", func() (any, error) {
		creates.Add(1)
		return "created", nil
	})
	if serviceErr != nil || got != "created" {
		t.Fatalf("created service = %#v, %v", got, serviceErr)
	}
	got, serviceErr = transaction.service("overlay", func() (any, error) {
		creates.Add(1)
		return "wrong", nil
	})
	if serviceErr != nil || got != "created" || creates.Load() != 1 {
		t.Fatalf("cached service = %#v creates:%d error:%v", got, creates.Load(), serviceErr)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, inflightErr := transaction.service("inflight", func() (any, error) {
			close(entered)
			<-release
			return "ready", nil
		})
		firstDone <- inflightErr
	}()
	<-entered
	if _, err := transaction.service("inflight", func() (any, error) {
		return "duplicate", nil
	}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("concurrent service error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	panicCalls := 0
	if _, err := transaction.service("panic", func() (any, error) {
		panicCalls++
		panic("creator panic")
	}); err == nil || !strings.Contains(err.Error(), "creator panic") {
		t.Fatalf("service panic error = %v", err)
	}
	got, serviceErr = transaction.service("panic", func() (any, error) {
		panicCalls++
		return "recovered", nil
	})
	if serviceErr != nil || got != "recovered" || panicCalls != 2 {
		t.Fatalf("service retry = %#v calls:%d error:%v", got, panicCalls, serviceErr)
	}

	if _, err := transaction.service("nil", func() (any, error) { return nil, nil }); err == nil {
		t.Fatal("nil service value was accepted")
	}
	var typedNil *factoryCoverageCloser
	if _, err := transaction.service("typed_nil", func() (any, error) { return typedNil, nil }); err == nil {
		t.Fatal("typed nil service value was accepted")
	}
	if _, err := transaction.service("invalid", func() (any, error) { return []string{"mutable"}, nil }); err == nil ||
		!strings.Contains(err.Error(), "immutable scalar") {
		t.Fatalf("mutable service error = %v", err)
	}
	if err := transaction.closeCreated(); err != nil {
		t.Fatal(err)
	}
}

func TestToolFactoryCoverageServiceErrorsAndReservations(t *testing.T) {
	sentinel := errors.New("creator failed")

	scalarTransaction := newToolServiceTransaction(nil)
	value, serviceErr := scalarTransaction.service("partial_scalar", func() (any, error) {
		return "partial", sentinel
	})
	if value != nil || !errors.Is(serviceErr, sentinel) {
		t.Fatalf("partial scalar = %#v, %v", value, serviceErr)
	}
	if created := scalarTransaction.detachCreated(); !reflect.DeepEqual(created, []any{"partial"}) {
		t.Fatalf("tracked partial scalar = %#v", created)
	}

	invalidTransaction := newToolServiceTransaction(nil)
	if _, err := invalidTransaction.service("partial_mutable", func() (any, error) {
		return []string{"mutable"}, sentinel
	}); !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "immutable scalar") {
		t.Fatalf("partial mutable error = %v", err)
	}

	pointerTransaction := newToolServiceTransaction(nil)
	pointer := &factoryCoverageCloser{}
	t.Cleanup(func() { _ = pointerTransaction.cleanupAndRelease() })
	if _, err := pointerTransaction.service("partial_pointer", func() (any, error) {
		return pointer, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("partial pointer error = %v", err)
	}
	if err := pointerTransaction.cleanupAndRelease(); err != nil {
		t.Fatal(err)
	}
	if pointer.calls.Load() != 1 {
		t.Fatalf("partial pointer close calls = %d", pointer.calls.Load())
	}

	successTransaction := newToolServiceTransaction(nil)
	service := &factoryCoverageCloser{}
	t.Cleanup(func() { _ = successTransaction.cleanupAndRelease() })
	got, serviceErr := successTransaction.service("pointer", func() (any, error) { return service, nil })
	if serviceErr != nil || got != service {
		t.Fatalf("pointer service = %#v, %v", got, serviceErr)
	}
	if err := successTransaction.cleanupAndRelease(); err != nil {
		t.Fatal(err)
	}
	if service.calls.Load() != 1 {
		t.Fatalf("pointer service close calls = %d", service.calls.Load())
	}

	reserved := &factoryCoverageCloser{}
	identity, _, identityErr := serviceInstanceIdentity(reserved)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if err := globalOwnedToolInstances.reserve(identity, reserved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { globalOwnedToolInstances.release(identity) })
	collisionTransaction := newToolServiceTransaction(nil)
	if _, err := collisionTransaction.service("collision", func() (any, error) {
		return reserved, sentinel
	}); !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "another owner") {
		t.Fatalf("error-value reservation collision = %v", err)
	}
}

func TestToolFactoryCoverageConcurrentServiceCommit(t *testing.T) {
	cache := newToolServiceCache()
	left := newToolServiceTransaction(nil)
	right := newToolServiceTransaction(nil)
	if _, err := left.service("shared", func() (any, error) { return "left", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := right.service("shared", func() (any, error) { return "right", nil }); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, transaction := range []*toolServiceTransaction{left, right} {
		go func() {
			<-start
			results <- transaction.commit(cache)
		}()
	}
	close(start)
	errorsSeen := 0
	for range 2 {
		if err := <-results; err != nil {
			errorsSeen++
			if !strings.Contains(err.Error(), "changed during construction") {
				t.Fatalf("commit conflict error = %v", err)
			}
		}
	}
	if errorsSeen != 1 {
		t.Fatalf("concurrent commit conflicts = %d, want 1", errorsSeen)
	}
	committed := cache.snapshot()["shared"]
	if committed != "left" && committed != "right" {
		t.Fatalf("committed service = %#v", committed)
	}

	same := newToolServiceTransaction(nil)
	if _, err := same.service("shared", func() (any, error) { return committed, nil }); err != nil {
		t.Fatal(err)
	}
	if err := same.commit(cache); err != nil {
		t.Fatalf("same-identity commit error = %v", err)
	}
	if err := same.commit(cache); err != nil {
		t.Fatalf("empty repeated commit error = %v", err)
	}
	if err := left.closeCreated(); err != nil {
		t.Fatal(err)
	}
	if err := right.closeCreated(); err != nil {
		t.Fatal(err)
	}
	if err := same.closeCreated(); err != nil {
		t.Fatal(err)
	}
}

func TestToolFactoryCoverageCleanupAndCloseErrors(t *testing.T) {
	order := []string{}
	closeErr := errors.New("close failed")
	first := &factoryCoverageCloser{label: "first", order: &order}
	duplicate := &factoryCoverageCloser{label: "duplicate", order: &order}
	failing := &factoryCoverageCloser{label: "failing", order: &order, closeErr: closeErr}
	panicking := &factoryCoverageCloser{label: "panicking", order: &order, panicValue: "boom"}
	var typedNil *factoryCoverageCloser
	err := closeOwnerCreatedValues([]any{
		"not a closer", typedNil, first, duplicate, duplicate, failing, panicking,
	})
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "panic: boom") {
		t.Fatalf("joined close error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"panicking", "failing", "duplicate", "first"}) {
		t.Fatalf("close order = %#v", order)
	}
	if duplicate.calls.Load() != 1 {
		t.Fatalf("duplicate closer calls = %d", duplicate.calls.Load())
	}

	tracker := newToolInstanceTracker()
	quarantined := &factoryCoverageTool{name: "quarantined"}
	identity, identityErr := toolInstanceIdentity(quarantined)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if err := tracker.reserve(identity, quarantined); err != nil {
		t.Fatal(err)
	}
	transaction := newToolServiceTransaction(nil)
	transaction.reservations = append(transaction.reservations, toolInstanceReservation{
		tracker: tracker, identity: identity,
	})
	transaction.track(&factoryCoverageCloser{closeErr: closeErr})
	if err := transaction.cleanupAndRelease(); !errors.Is(err, closeErr) {
		t.Fatalf("cleanup close error = %v", err)
	}
	if err := tracker.reserve(identity, quarantined); err == nil {
		t.Fatal("failed cleanup released quarantined reservation")
	}
	for _, reservation := range transaction.detachReservations() {
		reservation.tracker.release(reservation.identity)
	}
	if err := tracker.reserve(identity, quarantined); err != nil {
		t.Fatalf("manual quarantine release failed: %v", err)
	}
	tracker.release(identity)

	released := &factoryCoverageTool{name: "released"}
	releasedIdentity, identityErr := toolInstanceIdentity(released)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if err := tracker.reserve(releasedIdentity, released); err != nil {
		t.Fatal(err)
	}
	var nilTransaction *toolServiceTransaction
	if err := nilTransaction.cleanupAndRelease(toolInstanceReservation{
		tracker: tracker, identity: releasedIdentity,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.reserve(releasedIdentity, released); err != nil {
		t.Fatalf("extra reservation was not released: %v", err)
	}
	tracker.release(releasedIdentity)
}

func TestToolFactoryCoverageServiceIdentityKinds(t *testing.T) {
	pointer := &factoryCoverageCloser{}
	identity, tracked, err := serviceInstanceIdentity(pointer)
	if err != nil || !tracked || identity.pointer == 0 || identity.typeOf != reflect.TypeOf(pointer) {
		t.Fatalf("pointer service identity = %#v tracked:%t error:%v", identity, tracked, err)
	}

	for _, value := range []any{
		true, "immutable", int(-1), int8(-1), int16(-1), int32(-1), int64(-1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float32(1), float64(1),
	} {
		identity, tracked, err := serviceInstanceIdentity(value)
		if err != nil || tracked || identity != (toolInstanceKey{}) {
			t.Errorf("scalar %T identity = %#v tracked:%t error:%v", value, identity, tracked, err)
		}
	}

	for _, value := range []any{
		struct{}{},
		[1]string{"mutable"},
		[]string{"mutable"},
		map[string]string{"key": "value"},
		make(chan struct{}), func() {}, complex(1, 2), uintptr(1),
	} {
		if _, tracked, err := serviceInstanceIdentity(value); err == nil || tracked ||
			!strings.Contains(err.Error(), "immutable scalar") {
			t.Errorf("mutable %T identity = tracked:%t error:%v", value, tracked, err)
		}
	}
}
