package catalog

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestNewReviewSnapshotReturnsOnlyClosedLogicalScope(t *testing.T) {
	cfg := config.DefaultConfig()
	options := logicalCatalogTestOptions(t, cfg)
	cfg.Agents.Defaults.Workspace = filepath.Join(options.Home, "workspace")
	agentWorkspace := filepath.Join(options.Home, "agents", "reviewer")
	cfg.Agents.List = []config.AgentConfig{
		{ID: "reviewer", Workspace: agentWorkspace},
		{ID: "shared", Workspace: agentWorkspace},
	}

	review, scopeFingerprint, err := NewReviewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	full, fullFingerprint, err := NewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if scopeFingerprint == "" || scopeFingerprint == fullFingerprint ||
		len(review.Entries()) != 8 || len(full.Entries()) <= len(review.Entries()) {
		t.Fatalf(
			"review/full snapshots = %d/%d fingerprints %q/%q",
			len(review.Entries()), len(full.Entries()), scopeFingerprint, fullFingerprint,
		)
	}
	bindings := review.Bindings()
	entries := review.Entries()
	if len(bindings) != len(entries) || !slices.IsSortedFunc(bindings, compareStoreBindings) {
		t.Fatalf("review bindings = %#v", bindings)
	}
	for index, binding := range bindings {
		if binding.ID != entries[index].ID || binding.Domain != entries[index].Domain ||
			strings.Contains(binding.ID.String(), options.Home) || strings.Contains(binding.Domain, options.Home) {
			t.Errorf("binding/entry %d = %#v / %#v", index, binding, entries[index])
		}
	}
	bindings[0] = database.StoreBinding{ID: "forged/id", Domain: "forged"}
	if review.Bindings()[0].ID == "forged/id" {
		t.Fatal("Bindings retained caller mutation")
	}
	if review.Contains("global/auth") || review.Contains("workspace/workflows") ||
		!review.Contains("workspace/repository-reviews") {
		t.Fatalf("review scope membership = %#v", review.Entries())
	}
}

func compareStoreBindings(left, right database.StoreBinding) int {
	return strings.Compare(left.ID.String(), right.ID.String())
}

func TestReviewSnapshotPreservesRequiredPolicyAndBindsUnselectedChanges(t *testing.T) {
	cfg := config.DefaultConfig()
	options := logicalCatalogTestOptions(t, cfg)
	cfg.Agents.Defaults.Workspace = filepath.Join(options.Home, "workspace")
	first, firstFingerprint, err := NewReviewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(first.RequiredStores(), StoreID("workspace/local-ci")) {
		t.Fatal("disabled event ingress made local CI required")
	}

	cfg.Workflows.Enabled = !cfg.Workflows.Enabled
	unselected, unselectedFingerprint, err := NewReviewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if unselectedFingerprint == firstFingerprint ||
		!slices.Equal(unselected.Entries(), first.Entries()) {
		t.Fatalf("unselected policy change = %q/%q %#v/%#v",
			firstFingerprint, unselectedFingerprint, first.Entries(), unselected.Entries())
	}

	cfg.Events.Ingress.Enabled = true
	required, requiredFingerprint, err := NewReviewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if requiredFingerprint == unselectedFingerprint ||
		!slices.Contains(required.RequiredStores(), StoreID("workspace/local-ci")) {
		t.Fatalf("required review policy = %q %#v", requiredFingerprint, required.RequiredStores())
	}
}

func TestReviewSnapshotErrorsAreBoundedAndAllOrNothing(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret-store.db") + "\x00"
	logical, fingerprint, err := NewReviewSnapshot(Options{
		Home: secret, Config: config.DefaultConfig(),
	}, "missing")
	if logical != nil || fingerprint != "" || err == nil ||
		strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), ".db") {
		t.Fatalf("invalid review snapshot = %#v, %q, %v", logical, fingerprint, err)
	}

	for _, test := range []struct {
		name        string
		bindings    []storecatalog.ReviewBinding
		fingerprint string
	}{
		{name: "empty bindings", fingerprint: "sha256:" + strings.Repeat("a", 64)},
		{name: "invalid binding", bindings: []storecatalog.ReviewBinding{{
			ID: "bad id", Domain: "reviews",
		}}, fingerprint: "sha256:" + strings.Repeat("a", 64)},
		{name: "empty fingerprint", bindings: []storecatalog.ReviewBinding{{
			ID: "workspace/reviews", Domain: "reviews",
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logical, fingerprint, err := newReviewCatalogSnapshot(test.bindings, test.fingerprint)
			if logical != nil || fingerprint != "" || err == nil {
				t.Fatalf("invalid review logical snapshot = %#v, %q, %v", logical, fingerprint, err)
			}
		})
	}
}

func TestCatalogBindingsAreSortedDetachedAndNilSafe(t *testing.T) {
	var nilCatalog *Catalog
	if nilCatalog.Bindings() != nil {
		t.Fatal("nil Catalog.Bindings returned data")
	}
	catalog, err := newLogicalCatalog([]Entry{
		{ID: "workspace/z", Domain: "z"},
		{ID: "global/a", Domain: "a", Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []database.StoreBinding{
		{ID: "global/a", Domain: "a"},
		{ID: "workspace/z", Domain: "z"},
	}
	bindings := catalog.Bindings()
	if !slices.Equal(bindings, want) {
		t.Fatalf("Bindings = %#v, want %#v", bindings, want)
	}
	bindings[0] = database.StoreBinding{ID: "forged/id", Domain: "forged"}
	if !slices.Equal(catalog.Bindings(), want) {
		t.Fatal("Bindings retained caller mutation")
	}
}
