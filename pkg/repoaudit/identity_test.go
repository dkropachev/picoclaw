package repoaudit

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestRepositoryLedgerIdentityVariantsAndRunFallback(t *testing.T) {
	for input, expected := range map[string][]string{
		"Owner/Repo.git":                      {"owner/repo", "Owner/Repo.git"},
		"git@github.com:Owner/Repo.git":       {"owner/repo", "git@github.com:Owner/Repo.git"},
		"github.com:Owner/Repo.git":           {"owner/repo", "github.com:Owner/Repo.git"},
		"ssh://git@github.com/Owner/Repo.git": {"owner/repo", "ssh://github.com/Owner/Repo.git"},
		"https://github.com/Owner/Repo.git":   {"owner/repo", "https://github.com/Owner/Repo.git"},
		"https://user:secret@github.com/Owner/Repo.git?token=x#fragment": {
			"owner/repo", "https://github.com/Owner/Repo.git",
		},
	} {
		if actual := RepositoryLedgerIdentities(input); !slices.Equal(actual, expected) {
			t.Fatalf("RepositoryLedgerIdentities(%q) = %#v, want %#v", input, actual, expected)
		}
	}

	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "a", 10)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit", "inventory", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "run-fallback",
		Observations: []Observation{{Model: "reviewer", ScopeFiles: []FileRef{file}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{
		"owner/repo", "https://github.com/owner/repo.git", "git@github.com:owner/repo.git",
	} {
		resolved, found, err := store.ResolveRepositoryState(identity, nil)
		if err != nil || !found || resolved.ID != recorded.State.ID {
			t.Fatalf("resolve %q = found %v id %q err %v", identity, found, resolved.ID, err)
		}
	}
	resolved, found, err := store.ResolveRepositoryState(
		"https://example.com/unrelated/repository.git", []string{"run-fallback"},
	)
	if err != nil || !found || resolved.Repository != "owner/repo" {
		t.Fatalf("run fallback = %#v found=%v err=%v", resolved, found, err)
	}
}

func TestRepositoryLedgerRunFallbackRejectsAmbiguity(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	for index, repository := range []string{"owner/one", "owner/two"} {
		file := repositoryAuditTestFile("service.go", string(rune('a'+index)), 10)
		plan, err := store.Plan(context.Background(), repository, "commit", "inventory", []FileRef{file}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Record(context.Background(), RecordRequest{
			Plan: plan, RunID: "shared-run",
			Observations: []Observation{{Model: "reviewer", ScopeFiles: []FileRef{file}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.ResolveRepositoryState("owner/missing", []string{"shared-run"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous fallback error = %v", err)
	}
}
