package prworkspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScopeDispositionStrictRelaxedAndInfrastructureBoundary(t *testing.T) {
	charter := Charter{Type: PRTypeFeature, Goal: "Add notification inbox"}
	adjacent := AgentFinding{
		Title: "Wire unread count", Message: "Expose count", File: "web/sidebar.tsx",
		ScopeDistance: ScopeNecessaryAdjacent, ChangeSize: ChangeSizeS,
		TypeCompatible: true, ScopeConfidence: 0.91, ScopeExplanation: "Needed for navigation",
	}
	scope := agentFindingScope(adjacent)
	require.Equal(t, FindingDeferred, decideFindingDisposition(
		scope, adjacent, charter, DefaultScopeDispositionPolicy(),
	))
	relaxed := ScopeDispositionPolicy{
		Default: ScopeDispositionRule{Mode: ScopeDispositionRelaxed, Prompt: "Keep UI work relevant."},
		ByType:  map[PRType]ScopeDispositionRule{},
	}
	require.Equal(t, FindingInScope, decideFindingDisposition(scope, adjacent, charter, relaxed))
	exactLarge := adjacent
	exactLarge.ScopeDistance, exactLarge.ChangeSize = ScopeExact, ChangeSizeM
	require.Equal(t, FindingOpen, decideFindingDisposition(
		agentFindingScope(exactLarge), exactLarge, charter, DefaultScopeDispositionPolicy(),
	))

	ci := adjacent
	ci.File, ci.Title = ".github/workflows/ci.yml", "Update CI pipeline"
	require.Equal(t, FindingDeferred, decideFindingDisposition(agentFindingScope(ci), ci, charter, relaxed))
	ci.ScopeDistance, ci.ChangeSize, ci.ScopeConfidence = ScopeExact, ChangeSizeXS, 1
	require.Equal(t, FindingDeferred, decideFindingDisposition(agentFindingScope(ci), ci, charter, relaxed),
		"an S0/XS classification must not bypass the infrastructure exception")
	charter.IncludedAreas = []string{"CI/CD workflow needed to validate notifications"}
	require.Equal(t, FindingInScope, decideFindingDisposition(agentFindingScope(ci), ci, charter, relaxed))
}

func TestScopeDispositionEvidenceBindsTypeModeAndPrompt(t *testing.T) {
	firstRevision, firstPrompt := scopeDispositionEvidence(
		ScopeDispositionRule{Mode: ScopeDispositionRelaxed, Prompt: "Prefer UI relevance"}, PRTypeFeature,
	)
	secondRevision, secondPrompt := scopeDispositionEvidence(
		ScopeDispositionRule{Mode: ScopeDispositionStrict, Prompt: "Prefer UI relevance"}, PRTypeFeature,
	)
	require.True(t, strings.HasPrefix(firstRevision, "sha256:"))
	require.True(t, strings.HasPrefix(firstPrompt, "sha256:"))
	require.Equal(t, firstPrompt, secondPrompt)
	require.NotEqual(t, firstRevision, secondRevision)
}
