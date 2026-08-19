package localci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileEvidenceStoreExactSuccessCacheAndExpiry(t *testing.T) {
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.cacheTTL = time.Hour
	plan := validTestPlan(t)
	if err = store.PutPlan(context.Background(), plan); err != nil {
		t.Fatalf("PutPlan() error = %v", err)
	}
	execution := validTestExecution(t, plan, now, StatusPassed)
	if err = store.PutExecution(context.Background(), execution); err != nil {
		t.Fatalf("PutExecution() error = %v", err)
	}
	if err = store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err != nil {
		t.Fatalf("PromotePassing() error = %v", err)
	}
	cached, found, err := store.LookupPassing(context.Background(), execution.ResultKey)
	if err != nil || !found || cached.Digest != execution.Digest {
		t.Fatalf("LookupPassing() = (%#v, %v, %v)", cached, found, err)
	}
	now = now.Add(time.Hour)
	if _, found, err = store.LookupPassing(context.Background(), execution.ResultKey); err != nil || found {
		t.Fatalf("expired LookupPassing() = (found %v, error %v)", found, err)
	}
}

func TestFileEvidenceStoreImmutableAttestationReplayAndConflict(t *testing.T) {
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	plan := validTestPlan(t)
	if err = store.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	execution := validTestExecution(t, plan, now, StatusFailed)
	if err = store.PutExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	attestation, err := finalizeAttestation(Attestation{
		ID:              "lcatt_replay",
		OwnerID:         "attempt_replay",
		ExecutionDigest: execution.Digest,
		ResultKey:       execution.ResultKey,
		Status:          execution.Status,
		CreatedAt:       now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutAttestation(context.Background(), attestation); err != nil {
		t.Fatal(err)
	}
	if err = store.PutAttestation(context.Background(), attestation); err != nil {
		t.Fatalf("exact PutAttestation() replay error = %v", err)
	}
	changed := attestation
	changed.OwnerID = "attempt_changed"
	changed, err = finalizeAttestation(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutAttestation(context.Background(), changed); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf("changed PutAttestation() error = %v, want conflict", err)
	}
}

func TestFileEvidenceStoreRejectsTamperAndNeverPromotesFailure(t *testing.T) {
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	plan := validTestPlan(t)
	if err = store.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	execution := validTestExecution(t, plan, now, StatusFailed)
	if err = store.PutExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if err = store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err == nil {
		t.Fatal("PromotePassing(failure) error = nil")
	}
	path := store.objectPath("executions", execution.Digest)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"status":"failed"`, `"status":"passed","unknown":true`, 1)
	if err = os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.GetExecution(context.Background(), execution.Digest); !errors.Is(err, ErrEvidenceCorrupt) {
		t.Fatalf("GetExecution(tampered) error = %v, want corrupt", err)
	}
}

func TestFileEvidenceStoreConcurrentExactWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	store, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := validTestPlan(t)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for index := range 16 {
		wait.Add(1)
		go func(writer *FileEvidenceStore) {
			defer wait.Done()
			errorsSeen <- writer.PutPlan(context.Background(), plan)
		}([]*FileEvidenceStore{store, second}[index%2])
	}
	wait.Wait()
	close(errorsSeen)
	for err = range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent PutPlan() error = %v", err)
		}
	}
}

func TestFileEvidenceStorePersistsExactManifestDiscovery(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", testExplicitPlan)
	resolved, err := DiscoverPair(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := discoveryCacheKey(strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	store, err := OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutResolvedPlan(context.Background(), key, resolved); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := reopened.GetResolvedPlan(context.Background(), key)
	if err != nil || !found || loaded.Effective.Digest != resolved.Effective.Digest ||
		loaded.Changed != resolved.Changed {
		t.Fatalf("GetResolvedPlan() = (%#v, %v, %v), want exact persisted plan", loaded, found, err)
	}
}

func TestFileEvidenceStoreRejectsDiscoveryIndexUnderDifferentKey(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", testExplicitPlan)
	resolved, err := DiscoverPair(context.Background(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := discoveryCacheKey(strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := discoveryCacheKey(strings.Repeat("c", 64), strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutResolvedPlan(context.Background(), key, resolved); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.objectPath("discovery", key))
	if err != nil {
		t.Fatal(err)
	}
	otherPath := store.objectPath("discovery", otherKey)
	if err = os.MkdirAll(filepath.Dir(otherPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(otherPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.GetResolvedPlan(context.Background(), otherKey); !errors.Is(err, ErrEvidenceCorrupt) {
		t.Fatalf("GetResolvedPlan(copied index) error = %v, want corrupt", err)
	}
}

func validTestPlan(t *testing.T) Plan {
	t.Helper()
	plan, err := normalizePlan(Plan{
		DefinitionDigest: digestParts("test-definition", []byte("definition")),
		DependencyDigest: digestParts("test-dependency", []byte("dependency")),
		Complete:         true,
		Steps: []Step{{
			ID:             "ci_test",
			Name:           "test",
			Kind:           StepTest,
			Origin:         OriginExplicit,
			Source:         ".picoclaw/ci.yml",
			Argv:           []string{"true"},
			TimeoutSeconds: 30,
			Required:       true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func validTestExecution(t *testing.T, plan Plan, now time.Time, status Status) Execution {
	t.Helper()
	evidence := CandidateEvidence{
		Repository:              "github.com/example/repository",
		ParentCommit:            strings.Repeat("1", 40),
		Tree:                    strings.Repeat("2", 40),
		CandidateDigest:         strings.Repeat("3", 64),
		ParentManifestDigest:    strings.Repeat("4", 64),
		CandidateManifestDigest: strings.Repeat("5", 64),
		DependencyDigest:        plan.DependencyDigest,
		PlanDigest:              plan.Digest,
		EnvironmentDigest:       strings.Repeat("6", 64),
		Limits:                  limitEvidence(DefaultLimits()),
	}
	key, err := resultCacheKey(evidence)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := finalizeExecution(Execution{
		ResultKey:   key,
		Evidence:    evidence,
		Status:      status,
		StartedAt:   now,
		CompletedAt: now.Add(time.Second),
		Steps: []StepResult{{
			StepID:         "ci_test",
			Status:         status,
			ExitCode:       map[bool]int{true: 0, false: 1}[status == StatusPassed],
			OutputDigest:   digestParts("picoclaw-local-ci-output-v1", nil),
			DurationMillis: 1000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return execution
}
