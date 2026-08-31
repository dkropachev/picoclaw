package workflows

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"
)

func overwritePersistedWorkflowExecutionForTest(
	t *testing.T,
	workspace string,
	run *Run,
) {
	t.Helper()
	encoded, err := encodeWorkflowJSON(run.execution, maximumWorkflowRunPayloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := db.ExecContext(
		t.Context(),
		`UPDATE workflow_run_payloads SET execution_json=? WHERE run_id=?`,
		encoded,
		run.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		t.Fatalf("overwrite workflow execution rows=%d error=%v", changed, rowsErr)
	}
}

func persistedWorkflowPrivatePayloadForTest(
	t *testing.T,
	workspace, runID string,
) []byte {
	t.Helper()
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var execution, privateContext []byte
	if err := db.QueryRowContext(
		t.Context(),
		`SELECT execution_json, private_context_json FROM workflow_run_payloads WHERE run_id=?`,
		runID,
	).Scan(&execution, &privateContext); err != nil {
		t.Fatal(err)
	}
	payload := append(append([]byte(nil), execution...), privateContext...)
	rows, err := db.QueryContext(
		t.Context(),
		`SELECT questions_json, response_schema_json, gate_form_json,
		        gate_workflow_json, response_json, response_id
		 FROM workflow_human_tasks WHERE run_id=? ORDER BY task_key`,
		runID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var questions []byte
		var schema, form, workflow, response []byte
		var responseID string
		if err := rows.Scan(&questions, &schema, &form, &workflow, &response, &responseID); err != nil {
			t.Fatal(err)
		}
		payload = append(payload, questions...)
		payload = append(payload, schema...)
		payload = append(payload, form...)
		payload = append(payload, workflow...)
		payload = append(payload, response...)
		payload = append(payload, responseID...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return payload
}

func persistedWorkflowClaimSnapshotForTest(
	t *testing.T,
	workspace, runID, taskID string,
) []byte {
	t.Helper()
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var snapshot struct {
		RunStatus       string
		RunVersion      int64
		Execution       []byte
		TaskStatus      string
		TaskRevision    int64
		ResponseID      string
		Response        []byte
		RetrySeconds    sql.NullInt64
		RetryNanosecond sql.NullInt64
	}
	if err := db.QueryRowContext(
		t.Context(),
		`SELECT r.status,r.version,p.execution_json,t.status,t.revision,t.response_id,
		        t.response_json,t.retry_at_seconds,t.retry_at_nanosecond
		 FROM workflow_runs r
		 JOIN workflow_run_payloads p ON p.run_id=r.run_id
		 JOIN workflow_human_tasks t ON t.run_id=r.run_id
		 WHERE r.run_id=? AND t.task_id=?`,
		runID,
		taskID,
	).Scan(
		&snapshot.RunStatus,
		&snapshot.RunVersion,
		&snapshot.Execution,
		&snapshot.TaskStatus,
		&snapshot.TaskRevision,
		&snapshot.ResponseID,
		&snapshot.Response,
		&snapshot.RetrySeconds,
		&snapshot.RetryNanosecond,
	); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(encoded)
}
