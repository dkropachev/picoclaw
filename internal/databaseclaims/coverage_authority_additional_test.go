//go:build unix

package databaseclaims

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type expiringFenceClaim struct {
	fence *database.Fence
}

func (*expiringFenceClaim) close() error { return nil }

func (claim *expiringFenceClaim) valid() bool {
	return claim.fence.Close() == nil
}

func TestLeaseGuardRejectsFenceLostAfterClaimValidation(t *testing.T) {
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lease := &Lease{
		home:   home,
		fence:  fence,
		claims: []claimHandle{&expiringFenceClaim{fence: fence}},
	}
	release, err := lease.Guard()
	if release != nil || err == nil {
		t.Fatalf("Guard after claim-triggered fence expiry = release %t, %v", release != nil, err)
	}
}

func TestAcquireRejectsMainIdentityMaterializedAfterFinalCatalogCheck(t *testing.T) {
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	options := testOptions(t, home, &config.Config{})
	ops := defaultClaimAcquireOps()
	ops.claimRoot = func() (string, error) { return "coverage-root", nil }
	ops.acquire = func(string, string) (claimHandle, error) { return &coverageClaimHandle{}, nil }
	originalObserve := ops.observe
	observations := 0
	materialized := false
	ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		observed, observeErr := originalObserve(catalog)
		observations++
		if observeErr == nil && observations == 2 && !materialized {
			materialized = true
			if linkErr := os.Symlink("missing-main-target", filepath.Join(home, "auth.db")); linkErr != nil {
				t.Fatalf("materialize unsafe main identity: %v", linkErr)
			}
		}
		return observed, observeErr
	}
	lease, err := acquireWithOps(options, fence, ops)
	if lease != nil || err == nil {
		t.Fatalf("Acquire with late unsafe main identity = %#v, %v", lease, err)
	}
}
