package databaseclaims

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type coverageClaimHandle struct {
	closed *int
	err    error
}

func (claim *coverageClaimHandle) close() error {
	if claim.closed != nil {
		*claim.closed++
	}
	return claim.err
}

func TestAcquireTransitionFaultBoundaries(t *testing.T) {
	home := secureTestDir(t)
	options := testOptions(t, home, &config.Config{})
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	canary := errors.New("claim transition canary")
	hexA := strings.Repeat("a", 64)
	hexB := strings.Repeat("b", 64)
	base := func() claimAcquireOps {
		ops := defaultClaimAcquireOps()
		ops.claimRoot = func() (string, error) { return "test-root", nil }
		ops.acquire = func(string, string) (claimHandle, error) {
			return &coverageClaimHandle{}, nil
		}
		return ops
	}

	if lease, err := acquireWithOps(options, fence, claimAcquireOps{}); lease != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("acquireWithOps(empty) = %#v, %v", lease, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*claimAcquireOps)
	}{
		{
			name: "projected claim IDs",
			mutate: func(ops *claimAcquireOps) {
				ops.claimIDs = func(*storecatalog.Catalog) ([]storecatalog.ClaimID, error) {
					return nil, canary
				}
			},
		},
		{
			name: "projected lexical identities",
			mutate: func(ops *claimAcquireOps) {
				ops.lexical = func([]storecatalog.ClaimID) ([]string, error) { return nil, canary }
			},
		},
		{
			name: "claim root",
			mutate: func(ops *claimAcquireOps) {
				ops.claimRoot = func() (string, error) { return "", canary }
			},
		},
		{
			name: "projected physical identities",
			mutate: func(ops *claimAcquireOps) {
				ops.physical = func(*storecatalog.Catalog) ([]string, error) { return nil, canary }
			},
		},
		{
			name: "strict catalog build",
			mutate: func(ops *claimAcquireOps) {
				ops.build = func(storecatalog.Options) (*storecatalog.Catalog, error) { return nil, canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := base()
			test.mutate(&ops)
			lease, err := acquireWithOps(options, fence, ops)
			if lease != nil || !errors.Is(err, canary) {
				t.Fatalf("acquireWithOps() = %#v, %v", lease, err)
			}
		})
	}

	t.Run("invalid acquired identity", func(t *testing.T) {
		ops := base()
		ops.lexical = func([]storecatalog.ClaimID) ([]string, error) { return []string{"invalid"}, nil }
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("invalid identity acquire = %#v, %v", lease, err)
		}
	})

	t.Run("fence expires during acquisition", func(t *testing.T) {
		ops := base()
		calls := 0
		ops.authorizes = func(*database.Fence, string) bool {
			calls++
			return calls == 1
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("expired acquisition fence = %#v, %v", lease, err)
		}
	})

	t.Run("duplicate lexical and physical identity", func(t *testing.T) {
		ops := base()
		ops.lexical = func([]storecatalog.ClaimID) ([]string, error) {
			return []string{hexA, hexA}, nil
		}
		ops.physical = func(*storecatalog.Catalog) ([]string, error) { return []string{hexA}, nil }
		acquires := 0
		ops.acquire = func(string, string) (claimHandle, error) {
			acquires++
			return &coverageClaimHandle{}, nil
		}
		lease, err := acquireWithOps(options, fence, ops)
		if err != nil || lease == nil || acquires != 1 {
			t.Fatalf("deduplicated acquire = %#v, %v; calls=%d", lease, err, acquires)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("built claim IDs", func(t *testing.T) {
		ops := base()
		calls := 0
		original := ops.claimIDs
		ops.claimIDs = func(catalog *storecatalog.Catalog) ([]storecatalog.ClaimID, error) {
			calls++
			if calls == 2 {
				return nil, canary
			}
			return original(catalog)
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil || !errors.Is(err, canary) {
			t.Fatalf("built claim IDs = %#v, %v", lease, err)
		}
	})

	t.Run("built lexical identities", func(t *testing.T) {
		ops := base()
		calls := 0
		original := ops.lexical
		ops.lexical = func(ids []storecatalog.ClaimID) ([]string, error) {
			calls++
			if calls == 2 {
				return nil, canary
			}
			return original(ids)
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil || !errors.Is(err, canary) {
			t.Fatalf("built lexical identities = %#v, %v", lease, err)
		}
	})

	t.Run("built physical identities", func(t *testing.T) {
		ops := base()
		calls := 0
		original := ops.physical
		ops.physical = func(catalog *storecatalog.Catalog) ([]string, error) {
			calls++
			if calls == 2 {
				return nil, canary
			}
			return original(catalog)
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil || !errors.Is(err, canary) {
			t.Fatalf("built physical identities = %#v, %v", lease, err)
		}
	})

	t.Run("catalog changes after acquisition", func(t *testing.T) {
		ops := base()
		calls := 0
		original := ops.lexical
		ops.lexical = func(ids []storecatalog.ClaimID) ([]string, error) {
			calls++
			if calls == 2 {
				return []string{hexB}, nil
			}
			return original(ids)
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("changed catalog acquire = %#v, %v", lease, err)
		}
	})

	t.Run("fence expires after build", func(t *testing.T) {
		ops := base()
		built := false
		original := ops.build
		ops.build = func(options storecatalog.Options) (*storecatalog.Catalog, error) {
			catalog, err := original(options)
			built = err == nil
			return catalog, err
		}
		ops.authorizes = func(*database.Fence, string) bool { return !built }
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("expired final fence = %#v, %v", lease, err)
		}
	})

	t.Run("lease claim limit", func(t *testing.T) {
		ops := base()
		identities := make([]string, maxLeaseClaims+1)
		for index := range identities {
			identities[index] = fmt.Sprintf("%064x", index)
		}
		ops.lexical = func([]storecatalog.ClaimID) ([]string, error) { return identities, nil }
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("oversized lease acquire = %#v, %v", lease, err)
		}
	})
}

func TestClaimRootTransitionFaultBoundaries(t *testing.T) {
	canary := errors.New("claim root canary")
	if root, err := prepareClaimRootWithOps(claimRootOps{}); root != "" ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("empty root operations = %q, %v", root, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*claimRootOps)
	}{
		{
			name: "stable cache",
			mutate: func(ops *claimRootOps) {
				ops.stable = func() (string, error) { return "", canary }
			},
		},
		{
			name: "absolute cache",
			mutate: func(ops *claimRootOps) {
				ops.absolute = func(string) (string, error) { return "", canary }
			},
		},
		{
			name: "platform root",
			mutate: func(ops *claimRootOps) {
				ops.prepare = func(string, string) error { return canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultClaimRootOps()
			test.mutate(&ops)
			root, err := prepareClaimRootWithOps(ops)
			if root != "" || err == nil {
				t.Fatalf("prepareClaimRootWithOps() = %q, %v", root, err)
			}
		})
	}
}
