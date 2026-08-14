package prworkspace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteHTTPResultProjectsEmptyAggregateCollectionsAsArrays(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeHTTPResult(recorder, Aggregate{Workspace: Workspace{ID: "prw_projection"}}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"charters", "stage_runs", "findings", "messages", "corrections",
		"repository_lessons", "nudge_rounds", "deferred_groups",
		"repair_attempts", "validation_runs", "gates", "publications", "activity",
	} {
		if got := string(payload[field]); got != "[]" {
			t.Errorf("%s = %s, want []", field, got)
		}
	}
}

func TestWriteHTTPErrorProjectsCurrentAggregateCollectionsAsArrays(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeHTTPResult(recorder, Aggregate{Workspace: Workspace{ID: "prw_projection"}}, ErrConflict)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var payload struct {
		Current struct {
			Charters json.RawMessage `json:"charters"`
			Findings json.RawMessage `json:"findings"`
			Gates    json.RawMessage `json:"gates"`
		} `json:"current"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]json.RawMessage{
		"charters": payload.Current.Charters,
		"findings": payload.Current.Findings,
		"gates":    payload.Current.Gates,
	} {
		if got := string(value); got != "[]" {
			t.Errorf("current.%s = %s, want []", field, got)
		}
	}
}
