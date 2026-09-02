//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"testing"
	"time"
)

func TestPRWorkspaceAggregateLoaderFailureStageMatrix(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	tables := []string{
		"pr_workspaces",
		"pr_provider_snapshots",
		"pr_charter_revisions",
		"pr_stage_runs",
		"pr_findings",
		"pr_finding_events",
		"pr_conversations",
		"pr_messages",
		"pr_corrections",
		"pr_repository_lessons",
		"pr_nudge_rounds",
		"pr_nudge_rewards",
		"pr_deferred_groups",
		"pr_deferred_group_items",
		"pr_repair_attempts",
		"pr_validation_runs",
		"pr_gate_runs",
		"pr_publications",
		"pr_operation_intents",
		"pr_ingress_watermarks",
		"pr_activity",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			clock := newMutableClock(now)
			store := openPRWorkspaceTestStore(t, clock)
			aggregate := createPRWorkspaceForTest(t, store, now)
			if _, err := store.db.ExecContext(t.Context(), `DROP TABLE `+table); err != nil {
				t.Fatal(err)
			}
			conn, err := store.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := loadPRWorkspaceAggregate(
				context.Background(), conn, aggregate.Workspace.ID,
			); err == nil {
				t.Fatalf("aggregate load succeeded without %s", table)
			}
		})
	}
	t.Run("current provider missing", func(t *testing.T) {
		clock := newMutableClock(now)
		store := openPRWorkspaceTestStore(t, clock)
		aggregate := createPRWorkspaceForTest(t, store, now)
		if _, err := store.db.ExecContext(
			t.Context(), `DELETE FROM pr_provider_snapshots WHERE workspace_id = ?`, aggregate.Workspace.ID,
		); err != nil {
			t.Fatal(err)
		}
		conn, err := store.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := loadPRWorkspaceAggregate(t.Context(), conn, aggregate.Workspace.ID); err == nil {
			t.Fatal("aggregate load succeeded without current provider snapshot")
		}
	})
}
