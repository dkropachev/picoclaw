package prworkspace

import "testing"

func TestClassifyChangeSizeUsesHighestExceededBoundary(t *testing.T) {
	policy := DefaultSizePolicy()
	tests := []struct {
		files, lines, modules int
		want                  ChangeSize
	}{
		{1, 20, 1, ChangeSizeXS},
		{2, 20, 1, ChangeSizeS},
		{1, 101, 1, ChangeSizeM},
		{1, 20, 4, ChangeSizeL},
		{-1, 0, 0, ChangeSizeL},
	}
	for _, test := range tests {
		if got := ClassifyChangeSize(test.files, test.lines, test.modules, policy); got != test.want {
			t.Fatalf("ClassifyChangeSize(%d,%d,%d) = %q, want %q", test.files, test.lines, test.modules, got, test.want)
		}
	}
}

func TestDecideScopeNeverBypassesExternalWork(t *testing.T) {
	if got := DecideScope(ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeS, TypeCompatible: true}); got != ScopeActionProceed {
		t.Fatalf("exact small action = %q", got)
	}
	if got := DecideScope(ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeM, TypeCompatible: true}); got != ScopeActionGate {
		t.Fatalf("exact large action = %q", got)
	}
	if got := DecideScope(ScopeAssessment{Distance: ScopeNecessaryAdjacent, Size: ChangeSizeXS, TypeCompatible: true}); got != ScopeActionReviseOrDefer {
		t.Fatalf("adjacent action = %q", got)
	}
	if got := DecideScope(ScopeAssessment{Distance: ScopeRelatedFollowup, Size: ChangeSizeXS, TypeCompatible: true}); got != ScopeActionDefer {
		t.Fatalf("follow-up action = %q", got)
	}
	if got := DecideScope(ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeXS}); got != ScopeActionReviseOrDefer {
		t.Fatalf("type mismatch action = %q", got)
	}
}

func TestHardCandidateScopeBlockerCannotBeGateApproved(t *testing.T) {
	for _, scope := range []ScopeAssessment{
		{Presence: WorkCandidatePresent, Distance: ScopeRelatedFollowup, Size: ChangeSizeXS, TypeCompatible: true},
		{Presence: WorkCandidatePresent, Distance: ScopeUnrelated, Size: ChangeSizeXS, TypeCompatible: true},
		{Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: false},
	} {
		if !HardCandidateScopeBlocker(scope) {
			t.Fatalf("hard candidate scope was gate-approvable: %#v", scope)
		}
	}
	for _, scope := range []ScopeAssessment{
		{Presence: WorkFollowUp, Distance: ScopeRelatedFollowup, Size: ChangeSizeXS, TypeCompatible: true},
		{Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeL, TypeCompatible: true},
		{Presence: WorkCandidatePresent, Distance: ScopeNecessaryAdjacent, Size: ChangeSizeS, TypeCompatible: true},
	} {
		if HardCandidateScopeBlocker(scope) {
			t.Fatalf("classifiable scope was treated as a hard candidate blocker: %#v", scope)
		}
	}
}

func TestDeterministicTypeCompatibility(t *testing.T) {
	if !DeterministicTypeCompatible(PRTypeDocumentation, ClassifyFile("docs/guide.md")) {
		t.Fatal("documentation change rejected")
	}
	if DeterministicTypeCompatible(PRTypeDocumentation, ClassifyFile("pkg/server.go")) {
		t.Fatal("runtime change admitted to documentation PR")
	}
	if !DeterministicTypeCompatible(PRTypeTest, ClassifyFile("pkg/server_test.go")) {
		t.Fatal("test change rejected")
	}
	if DeterministicTypeCompatible(PRTypeTest, ClassifyFile("pkg/server.go")) {
		t.Fatal("runtime change admitted to test PR")
	}
}
