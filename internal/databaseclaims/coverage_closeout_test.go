package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type coverageClaimHandle struct {
	closed *int
	err    error
}

type coverageInvalidClaimHandle struct{}

func (*coverageInvalidClaimHandle) close() error { return nil }
func (*coverageInvalidClaimHandle) valid() bool  { return false }

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
				ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) { return nil, canary }
			},
		},
		{
			name: "strict catalog revalidation",
			mutate: func(ops *claimAcquireOps) {
				ops.revalidate = func(*storecatalog.Catalog) (*storecatalog.Catalog, error) {
					return nil, canary
				}
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
		ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) {
			return []memberObservation{{
				StoreID: "global/auth", Role: memberMain, Path: "/opaque",
				Type: memberRegular, ClaimIdentity: hexA,
			}}, nil
		}
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
		original := ops.observe
		ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
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
		original := ops.revalidate
		ops.revalidate = func(projected *storecatalog.Catalog) (*storecatalog.Catalog, error) {
			catalog, err := original(projected)
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

	t.Run("final strict catalog", func(t *testing.T) {
		ops := base()
		calls := 0
		original := ops.revalidate
		ops.revalidate = func(catalog *storecatalog.Catalog) (*storecatalog.Catalog, error) {
			calls++
			if calls == 2 {
				return nil, canary
			}
			return original(catalog)
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil || !errors.Is(err, canary) {
			t.Fatalf("final strict catalog = %#v, %v", lease, err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func([]memberObservation) ([]memberObservation, error)
	}{
		{name: "final observation error", mutate: func([]memberObservation) ([]memberObservation, error) {
			return nil, canary
		}},
		{name: "final observation drift", mutate: func(observed []memberObservation) ([]memberObservation, error) {
			changed := cloneObservations(observed)
			return append(changed, memberObservation{ClaimIdentity: hexA}), nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := base()
			calls := 0
			original := ops.observe
			ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
				calls++
				observed, observeErr := original(catalog)
				if observeErr == nil && calls == 4 {
					return test.mutate(observed)
				}
				return observed, observeErr
			}
			if lease, err := acquireWithOps(options, fence, ops); lease != nil || err == nil {
				t.Fatalf("final observation = %#v, %v", lease, err)
			}
		})
	}

	t.Run("final observation was not locked", func(t *testing.T) {
		ops := base()
		observed := []memberObservation{{
			StoreID: "global/auth", Role: memberMain, Path: "/opaque",
			Type: memberRegular, ClaimIdentity: hexA,
		}}
		calls := 0
		ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) {
			calls++
			if calls == 2 {
				observed[0].ClaimIdentity = hexB
			}
			return observed, nil
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("unlocked final observation = %#v, %v", lease, err)
		}
	})

	t.Run("invalid final claim handle", func(t *testing.T) {
		ops := base()
		ops.acquire = func(string, string) (claimHandle, error) {
			return &coverageInvalidClaimHandle{}, nil
		}
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("invalid final claim handle = %#v, %v", lease, err)
		}
	})

	t.Run("missing claim handle", func(t *testing.T) {
		ops := base()
		ops.acquire = func(string, string) (claimHandle, error) { return nil, nil }
		if lease, err := acquireWithOps(options, fence, ops); lease != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("missing claim handle = %#v, %v", lease, err)
		}
	})
}

func TestObservationHelperFailureBoundaries(t *testing.T) {
	hexA, hexB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if observations, err := observeCatalog(nil); observations != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("observeCatalog(nil) = %#v, %v", observations, err)
	}
	directory := secureTestDir(t)
	identity, objectType, exists, observeErr := observeMember(directory, true)
	if observeErr != nil || !exists || !identity.Valid() || objectType != memberDirectory {
		t.Fatalf("observeMember(directory) = %#v, %q, %t, %v", identity, objectType, exists, observeErr)
	}
	if identity, exists, err := regularPhysicalIdentity(directory); identity.Valid() || exists || err == nil {
		t.Fatalf("regularPhysicalIdentity(directory) = %#v, %t, %v", identity, exists, err)
	}
	first, second := filepath.Join(directory, "first.db"), filepath.Join(directory, "second.db")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	observations, err := observeStores([]storecatalog.Spec{
		{ID: "global/first", Path: first},
		{ID: "global/second", Path: second},
	})
	if err != nil || len(observations) != 2 ||
		observationMemberKey(observations[0]) >= observationMemberKey(observations[1]) {
		t.Fatalf("sorted observations = %#v, %v", observations, err)
	}
	for _, test := range []struct {
		err  error
		code database.ErrorCode
	}{
		{err: fileidentity.ErrUnsupported, code: database.CodeUnsupported},
		{err: errors.New("identity canary")},
	} {
		err := physicalIdentityError(test.err)
		if err == nil || test.code != "" && database.CodeOf(err) != test.code {
			t.Fatalf("physicalIdentityError(%v) = %v", test.err, err)
		}
	}
	if allObservationClaimsHeld(map[string]struct{}{}, []string{hexA}, nil) {
		t.Fatal("missing lexical claim reported held")
	}
	if allObservationClaimsHeld(
		map[string]struct{}{hexA: {}}, nil, []memberObservation{{ClaimIdentity: hexB}},
	) {
		t.Fatal("missing physical claim reported held")
	}
	if claimHandlesValid([]claimHandle{nil}) ||
		claimHandlesValid([]claimHandle{&coverageInvalidClaimHandle{}}) {
		t.Fatal("invalid claim handle reported valid")
	}
}

func TestRefreshTransitionFaultBoundaries(t *testing.T) {
	canary := errors.New("refresh transition canary")
	withLease := func(t *testing.T, run func(*Lease, claimRefreshOps)) {
		t.Helper()
		lease := acquireMigrationLease(t)
		lease.mu.Lock()
		defer lease.mu.Unlock()
		ops := defaultClaimRefreshOps()
		ops.acquire = lease.acquireClaim
		run(lease, ops)
	}

	t.Run("empty operations", func(t *testing.T) {
		withLease(t, func(lease *Lease, _ claimRefreshOps) {
			if err := lease.refreshLockedWithOps("", claimRefreshOps{}); database.CodeOf(err) !=
				database.CodeIntegrity {
				t.Fatalf("empty refresh operations = %v", err)
			}
		})
	})

	t.Run("first observation", func(t *testing.T) {
		withLease(t, func(lease *Lease, ops claimRefreshOps) {
			ops.observe = func(*storecatalog.Catalog) ([]memberObservation, error) { return nil, canary }
			if err := lease.refreshLockedWithOps("", ops); !errors.Is(err, canary) {
				t.Fatalf("first observation = %v", err)
			}
		})
	})

	t.Run("final strict catalog", func(t *testing.T) {
		withLease(t, func(lease *Lease, ops claimRefreshOps) {
			calls := 0
			original := ops.revalidate
			ops.revalidate = func(catalog *storecatalog.Catalog) (*storecatalog.Catalog, error) {
				calls++
				if calls == 2 {
					return nil, canary
				}
				return original(catalog)
			}
			if err := lease.refreshLockedWithOps("", ops); !errors.Is(err, canary) {
				t.Fatalf("final strict catalog = %v", err)
			}
		})
	})

	t.Run("final observation drift", func(t *testing.T) {
		withLease(t, func(lease *Lease, ops claimRefreshOps) {
			calls := 0
			original := ops.observe
			ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
				calls++
				observations, err := original(catalog)
				if err == nil && calls == 2 {
					return append(observations, memberObservation{ClaimIdentity: strings.Repeat("c", 64)}), nil
				}
				return observations, err
			}
			if err := lease.refreshLockedWithOps("", ops); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("final observation drift = %v", err)
			}
		})
	})

	t.Run("final assignment transition", func(t *testing.T) {
		withLease(t, func(lease *Lease, ops claimRefreshOps) {
			member := filepath.Join(lease.home, "auth.db-wal")
			if err := os.WriteFile(member, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			original := ops.observe
			calls := 0
			ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
				observations, err := original(catalog)
				calls++
				if err == nil && calls == 2 && len(observations) != 0 {
					lease.assignments[observations[0].Identity.String()] = "changed-assignment"
				}
				return observations, err
			}
			err := lease.refreshLockedWithOps("", ops)
			if database.CodeOf(err) != database.CodeIntegrity ||
				!strings.Contains(err.Error(), "changed catalog assignment") {
				t.Fatalf("final assignment transition = %v", err)
			}
		})
	})

	t.Run("missing acquired handle", func(t *testing.T) {
		withLease(t, func(lease *Lease, ops claimRefreshOps) {
			if err := os.WriteFile(filepath.Join(lease.home, "auth.db-wal"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			ops.acquire = func(string, string) (claimHandle, error) { return nil, nil }
			if err := lease.refreshLockedWithOps("", ops); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("missing acquired handle = %v", err)
			}
		})
	})

	t.Run("unlocked final observation", func(t *testing.T) {
		withLease(t, func(lease *Lease, ops claimRefreshOps) {
			if err := os.WriteFile(filepath.Join(lease.home, "auth.db-wal"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			original := ops.observe
			var shared []memberObservation
			calls := 0
			ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
				calls++
				if calls == 1 {
					var err error
					shared, err = original(catalog)
					return shared, err
				}
				shared[0].ClaimIdentity = strings.Repeat("d", 64)
				return shared, nil
			}
			if err := lease.refreshLockedWithOps("", ops); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("unlocked final observation = %v", err)
			}
		})
	})

	for _, test := range []struct {
		name   string
		mutate func(*Lease, *claimRefreshOps)
	}{
		{name: "invalid retained handle", mutate: func(lease *Lease, _ *claimRefreshOps) {
			lease.claims = append(lease.claims, &coverageInvalidClaimHandle{})
		}},
		{name: "lost guarded authority", mutate: func(_ *Lease, ops *claimRefreshOps) {
			ops.guardedAuthority = func() bool { return false }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			withLease(t, func(lease *Lease, ops claimRefreshOps) {
				test.mutate(lease, &ops)
				if err := lease.refreshLockedWithOps("", ops); database.CodeOf(err) != database.CodeIntegrity {
					t.Fatalf("final authority = %v", err)
				}
			})
		})
	}
}

func TestClaimRootTransitionFaultBoundaries(t *testing.T) {
	canary := errors.New("claim root canary")
	t.Run("success", func(t *testing.T) {
		cache := secureTestDir(t)
		ops := claimRootOps{
			stable:   func() (string, error) { return cache, nil },
			absolute: func(path string) (string, error) { return path, nil },
			prepare:  func(string, string) error { return nil },
		}
		root, err := prepareClaimRootWithOps(ops)
		if err != nil || root != filepath.Join(cache, claimApplicationDirectory, claimDirectoryName) {
			t.Fatalf("prepareClaimRootWithOps(success) = %q, %v", root, err)
		}
	})
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
