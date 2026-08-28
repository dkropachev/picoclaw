package repoaudit

import (
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryReviewRecoveryExportedHelpersCoverCanonicalBoundaries(t *testing.T) {
	first := FileRef{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1, Category: "code", Mode: "100644"}
	second := FileRef{Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2, Category: "code", Mode: "100644"}
	canonical, err := CanonicalRepositoryReviewCampaignScope([]FileRef{second, first})
	if err != nil || !reflect.DeepEqual(canonical, []FileRef{first, second}) {
		t.Fatalf("canonical scope=%#v err=%v", canonical, err)
	}
	left, err := RepositoryReviewCampaignScopeDigest([]FileRef{second, first})
	if err != nil {
		t.Fatal(err)
	}
	right, err := RepositoryReviewCampaignScopeDigest([]FileRef{first, second})
	if err != nil || left == "" || left != right {
		t.Fatalf("scope digests left=%q right=%q err=%v", left, right, err)
	}
	for name, files := range map[string][]FileRef{
		"duplicate": {first, first},
		"noncanonical": {{
			Path: " a.go", BlobSHA: first.BlobSHA, SizeBytes: first.SizeBytes,
			Category: first.Category, Mode: first.Mode,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalRepositoryReviewCampaignScope(files); err == nil {
				t.Fatal("invalid exact scope was accepted")
			}
			if _, err := RepositoryReviewCampaignScopeDigest(files); err == nil {
				t.Fatal("invalid exact scope digest was accepted")
			}
		})
	}
}

func TestRepositoryReviewRecoveryIdentityAndCandidateHelpers(t *testing.T) {
	line := 7
	raw := FindingCandidate{
		Severity: " HIGH ", Title: " title ", Symbol: " Save ", File: " pkg/save.go ",
		Line: &line, Message: " message ", Evidence: " evidence ", Impact: " impact ",
		Validation: Validation{Status: " CONFIRMED ", Summary: " summary "},
		MatchHints: MatchHints{
			Component: " component ", RelatedSymbols: []string{" Save ", "Save"},
		},
		FixEffort: FixEffort{
			Quick:   FixEffortEstimate{Class: " TINY ", Rationale: " quick "},
			Quality: FixEffortEstimate{Class: " SMALL ", Rationale: " quality "},
		},
	}
	candidate := NormalizeRepositoryReviewFindingCandidate(raw)
	if candidate.Severity != "high" || candidate.Title != "title" || candidate.Symbol != "Save" ||
		candidate.File != "pkg/save.go" || candidate.Validation.Status != "confirmed" ||
		candidate.MatchHints.Component != "component" ||
		!reflect.DeepEqual(candidate.MatchHints.RelatedSymbols, []string{"Save", "Save"}) ||
		candidate.FixEffort.Quick.Class != "tiny" || candidate.FixEffort.Quality.Rationale != "quality" {
		t.Fatalf("normalized candidate=%#v", candidate)
	}

	file := FileRef{Path: candidate.File, BlobSHA: strings.Repeat("a", 40), SizeBytes: 10}
	contextRecord := FindingContext{
		Repository: "owner/repo", CommitSHA: strings.Repeat("b", 40),
		InventoryHash: "inventory", ProfileHash: "profile", RunID: "wr_legacy",
		Model: "model", Reviewer: "reviewer", Files: []FileRef{file}, RawDigest: "sha256:raw",
	}
	contextRecord.ID = stableID("rctx_", contextBindingDigest(contextRecord))
	if !ValidateRepositoryReviewLegacyContextIdentity(contextRecord) {
		t.Fatal("valid legacy context identity was rejected")
	}
	tagged := contextRecord
	tagged.CampaignID = NewRepositoryReviewCampaignID()
	if !ValidateRepositoryReviewLegacyContextIdentity(tagged) {
		t.Fatal("campaign tag changed immutable legacy context identity")
	}
	for _, invalid := range []FindingContext{{}, func() FindingContext {
		value := contextRecord
		value.ID = "wrong"
		return value
	}()} {
		if ValidateRepositoryReviewLegacyContextIdentity(invalid) {
			t.Fatalf("invalid legacy context identity accepted: %#v", invalid)
		}
	}

	outside := candidate
	outside.File = "outside.go"
	if ValidateRepositoryReviewLegacyFindingIdentity(Finding{}, contextRecord, outside) {
		t.Fatal("out-of-scope legacy finding identity was accepted")
	}
}
