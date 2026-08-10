package attention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPreparedPolicyPreservesLegacyCanonicalEnvelopeAndRevision(t *testing.T) {
	t.Parallel()
	snapshot := PolicySnapshot{
		Revision: "source-generation/private-v1",
		Global: []workflows.GateSpec{
			{
				ID: "policy", Kind: workflows.GateDeterministic,
				When: "true", Title: "Policy", Questions: []any{"Approve?"},
			},
			{ID: "off", Kind: workflows.GateZero},
		},
		Repository: &workflows.RepositoryGatePolicy{
			Mode: workflows.GatePolicyOverlay,
			Gates: []workflows.GateSpec{{
				ID: "policy", Kind: workflows.GateDeterministic,
				When: "false", Title: "Repository policy", Questions: []any{"Continue?"},
			}},
		},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatalf("PrepareSnapshot() error = %v", err)
	}

	resolution, err := workflows.ResolveGatePolicy(snapshot.Global, snapshot.Repository)
	if err != nil {
		t.Fatal(err)
	}
	legacyRevisionInput, err := json.Marshal(struct {
		Version    int                             `json:"version"`
		Revision   string                          `json:"source_revision"`
		Resolution *workflows.GatePolicyResolution `json:"resolution"`
	}{Version: 1, Revision: snapshot.Revision, Resolution: resolution})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(legacyRevisionInput)
	wantRevision := "sha256:" + hex.EncodeToString(digest[:])
	legacyEnvelope, err := json.Marshal(struct {
		Version          int                             `json:"version"`
		SourceRevision   string                          `json:"source_revision"`
		DecisionRevision string                          `json:"decision_revision"`
		Resolution       *workflows.GatePolicyResolution `json:"resolution"`
	}{
		Version: 1, SourceRevision: snapshot.Revision,
		DecisionRevision: wantRevision, Resolution: resolution,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.DecisionRevision(); got != wantRevision {
		t.Fatalf("DecisionRevision() = %q, want %q", got, wantRevision)
	}
	if got := prepared.Canonical(); !reflect.DeepEqual(got, legacyEnvelope) {
		t.Fatalf("Canonical() = %s, want legacy bytes %s", got, legacyEnvelope)
	}

	decoded, err := DecodePreparedPolicy(legacyEnvelope)
	if err != nil {
		t.Fatalf("DecodePreparedPolicy(legacy) error = %v", err)
	}
	if decoded.SourceRevision() != snapshot.Revision ||
		decoded.DecisionRevision() != wantRevision ||
		!reflect.DeepEqual(decoded.Resolution(), resolution) {
		t.Fatalf("decoded policy = %#v", decoded.Resolution())
	}
	canonical := decoded.Canonical()
	canonical[0] = '['
	if reflect.DeepEqual(decoded.Canonical(), canonical) {
		t.Fatal("Canonical() exposed mutable policy bytes")
	}
}

func TestPreparePolicyEnforcesSynchronousSingleCallback(t *testing.T) {
	t.Parallel()
	snapshot := PolicySnapshot{Revision: "revision", Global: []workflows.GateSpec{}}
	tests := []struct {
		name   string
		source PolicySource
	}{
		{
			name: "missing callback",
			source: PolicySourceFunc(func(context.Context, PolicySelector, PolicyUse) error {
				return nil
			}),
		},
		{
			name: "duplicate callback swallowed",
			source: PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
				_ = use(ctx, snapshot)
				_ = use(ctx, snapshot)
				return nil
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := PreparePolicy(
				context.Background(),
				test.source,
				PolicySelector{Repository: "acme/widgets", DecisionPoint: "review.ready"},
			); err == nil {
				t.Fatal("PreparePolicy() succeeded for invalid source callback contract")
			}
		})
	}
}

func TestPreparePolicyClosesRetainedCallbackAfterSourceReturns(t *testing.T) {
	t.Parallel()
	snapshot := PolicySnapshot{Revision: "revision", Global: []workflows.GateSpec{}}
	var retained PolicyUse
	source := PolicySourceFunc(func(
		_ context.Context,
		_ PolicySelector,
		use PolicyUse,
	) error {
		retained = use
		return nil
	})

	if _, err := PreparePolicy(context.Background(), source, PolicySelector{}); !errors.Is(
		err,
		ErrInvalidPolicySource,
	) {
		t.Fatalf("PreparePolicy() error = %v, want invalid source", err)
	}
	if retained == nil {
		t.Fatal("source did not retain callback")
	}

	const attempts = 16
	var rejected atomic.Int32
	var wait sync.WaitGroup
	wait.Add(attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			if errors.Is(retained(context.Background(), snapshot), ErrInvalidPolicySource) {
				rejected.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := rejected.Load(); got != attempts {
		t.Fatalf("rejected callbacks = %d, want %d", got, attempts)
	}
}

func TestRevalidatedPolicyClosesRetainedCallbackWithoutInvokingUse(t *testing.T) {
	t.Parallel()
	snapshot := PolicySnapshot{Revision: "revision", Global: []workflows.GateSpec{}}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var retained PolicyUse
	source := PolicySourceFunc(func(
		_ context.Context,
		_ PolicySelector,
		use PolicyUse,
	) error {
		retained = use
		return nil
	})
	var useCalls atomic.Int32
	err = withRevalidatedPolicy(
		context.Background(),
		source,
		PolicySelector{},
		prepared,
		func(context.Context) error {
			useCalls.Add(1)
			return nil
		},
	)
	if !errors.Is(err, ErrInvalidPolicySource) {
		t.Fatalf("withRevalidatedPolicy() error = %v, want invalid source", err)
	}
	if retained == nil {
		t.Fatal("source did not retain callback")
	}

	const attempts = 16
	var rejected atomic.Int32
	var wait sync.WaitGroup
	wait.Add(attempts)
	for range attempts {
		go func() {
			defer wait.Done()
			if errors.Is(retained(context.Background(), snapshot), ErrInvalidPolicySource) {
				rejected.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := rejected.Load(); got != attempts {
		t.Fatalf("rejected callbacks = %d, want %d", got, attempts)
	}
	if got := useCalls.Load(); got != 0 {
		t.Fatalf("use calls = %d, want 0", got)
	}
}
