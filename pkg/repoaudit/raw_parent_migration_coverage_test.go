package repoaudit

import (
	"strings"
	"testing"
)

func TestRawFindingMigrationErrorsPropagateThroughRepositoryMigration(t *testing.T) {
	state := RepositoryState{
		SchemaVersion: SchemaVersion,
		RawFindings: []RawReviewFinding{
			{ID: "rrf_collision"},
			{ID: "rrw_collision"},
		},
	}
	migrated, err := migrateRepositoryState(&state)
	if err == nil || migrated || !strings.Contains(err.Error(), "raw finding ID migration conflicts") {
		t.Fatalf("migration=(%v, %v)", migrated, err)
	}
}

func TestCompatibilityParentMigrationRejectsConflictingRawAlias(t *testing.T) {
	state := RepositoryState{RawFindings: []RawReviewFinding{{
		ID:                    "rrl_alias_conflict",
		AssignmentID:          "record-000-000",
		LegacyFindingID:       "rfn_other",
		DeduplicatedFindingID: "rfn_parent",
	}}}
	if migrated, err := migrateRepositoryReviewRawFindingIDs(&state); err == nil || migrated ||
		!strings.Contains(err.Error(), "raw alias conflicts") {
		t.Fatalf("migration=(%v, %v)", migrated, err)
	}
}

func TestCompatibilityParentMigrationRejectsAmbiguousRawParents(t *testing.T) {
	state := RepositoryState{RawFindings: []RawReviewFinding{
		{
			ID: "rrl_first", AssignmentID: "record-000-000",
			DeduplicatedFindingID: "rfn_shared",
		},
		{
			ID: "rrl_second", AssignmentID: "record-001-000",
			DeduplicatedFindingID: "rfn_shared",
		},
	}}
	if migrated, err := migrateRepositoryReviewRawFindingIDs(&state); err == nil || migrated ||
		!strings.Contains(err.Error(), "parent migration is ambiguous") {
		t.Fatalf("migration=(%v, %v)", migrated, err)
	}
}

func TestCompatibilityParentMigrationRejectsProjectionAndMappingCollisions(t *testing.T) {
	oldParentID := "rfn_parent"
	newRawID := "rrw_parent"
	newParentID := stableID("rdf_", newRawID)
	base := func() RepositoryState {
		return RepositoryState{
			RawFindings: []RawReviewFinding{{
				ID: "rrl_parent", AssignmentID: "record-000-000",
				DeduplicatedFindingID: oldParentID,
			}},
			DeduplicatedFindings: []DeduplicatedReviewFinding{{ID: oldParentID}},
			Findings:             []Finding{{ID: oldParentID}},
		}
	}

	t.Run("finding projection", func(t *testing.T) {
		state := base()
		state.Findings = append(state.Findings, Finding{ID: newParentID})
		if migrated, err := migrateRepositoryReviewRawFindingIDs(&state); err == nil || migrated ||
			!strings.Contains(err.Error(), "finding projection identity migration conflicts") {
			t.Fatalf("migration=(%v, %v)", migrated, err)
		}
	})

	t.Run("mapping job", func(t *testing.T) {
		state := base()
		state.MappingJobs = []RepositoryMappingJob{
			{ID: mappingJobID(oldParentID), ReviewFindingID: oldParentID},
			{ID: mappingJobID(newParentID), ReviewFindingID: newParentID},
		}
		if migrated, err := migrateRepositoryReviewRawFindingIDs(&state); err == nil || migrated ||
			!strings.Contains(err.Error(), "mapping job identity migration conflicts") {
			t.Fatalf("migration=(%v, %v)", migrated, err)
		}
	})
}

func TestCompatibilityParentMigrationRequiresBothParentProjections(t *testing.T) {
	oldParentID := "rfn_incomplete"
	base := func() RepositoryState {
		return RepositoryState{RawFindings: []RawReviewFinding{{
			ID: "rrl_incomplete", AssignmentID: "record-000-000",
			DeduplicatedFindingID: oldParentID,
		}}}
	}
	for name, mutate := range map[string]func(*RepositoryState){
		"missing deduplicated finding": func(state *RepositoryState) {
			state.Findings = []Finding{{ID: oldParentID}}
		},
		"missing finding projection": func(state *RepositoryState) {
			state.DeduplicatedFindings = []DeduplicatedReviewFinding{{ID: oldParentID}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := base()
			mutate(&state)
			if migrated, err := migrateRepositoryReviewRawFindingIDs(&state); err == nil || migrated ||
				!strings.Contains(err.Error(), "parent migration is incomplete") {
				t.Fatalf("migration=(%v, %v)", migrated, err)
			}
		})
	}
}
