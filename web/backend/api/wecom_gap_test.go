package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWeComFlowCanonicalLifecycleCoverage(t *testing.T) {
	handler := &Handler{wecomFlows: make(map[string]*wecomFlow)}
	now := time.Now()
	flow := &wecomFlow{
		ID: "wc_active", SCode: "code", QRDataURI: "data:image/png;base64,AA==",
		Status: wecomStatusWait, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(time.Minute),
	}
	handler.storeWecomFlow(flow)
	stored, found := handler.getWecomFlow(flow.ID)
	if !found || stored.ID != flow.ID {
		t.Fatalf("stored flow=%#v found=%v", stored, found)
	}
	stored.Status = "caller-only"
	if current, _ := handler.getWecomFlow(flow.ID); current.Status != wecomStatusWait {
		t.Fatal("flow lookup leaked mutable state")
	}

	handler.updateWecomFlowStatus(flow.ID, wecomStatusScanned)
	handler.setWecomFlowConfirmed(flow.ID, "bot-id")
	handler.setWecomFlowError(flow.ID, "safe error")
	terminal, found := handler.getWecomFlow(flow.ID)
	if !found || terminal.Status != wecomStatusError || terminal.Error != "safe error" {
		t.Fatalf("terminal flow=%#v found=%v", terminal, found)
	}

	expired := &wecomFlow{
		ID: "wc_expired", Status: wecomStatusWait,
		UpdatedAt: now, ExpiresAt: now.Add(-time.Minute),
	}
	stale := &wecomFlow{
		ID: "wc_stale", Status: wecomStatusConfirmed,
		UpdatedAt: now.Add(-wecomFlowGCAge - time.Minute),
	}
	handler.wecomFlows[expired.ID] = expired
	handler.wecomFlows[stale.ID] = stale
	handler.gcWecomFlowsLocked(now)
	if expired.Status != wecomStatusExpired {
		t.Fatalf("expired status=%q", expired.Status)
	}
	if _, found := handler.wecomFlows[stale.ID]; found {
		t.Fatal("stale terminal flow retained")
	}
	if _, found := handler.getWecomFlow("missing"); found {
		t.Fatal("missing flow found")
	}
	if id := newWecomFlowID(); !strings.HasPrefix(id, "wc_") {
		t.Fatalf("flow id=%q", id)
	}
}

func TestWeComTerminalPollingAndHTTPBoundariesCoverage(t *testing.T) {
	handler := &Handler{wecomFlows: make(map[string]*wecomFlow)}

	missingID := httptest.NewRecorder()
	handler.handlePollWecomFlow(
		missingID,
		httptest.NewRequest(http.MethodGet, "/api/wecom/flows/", nil),
	)
	if missingID.Code != http.StatusBadRequest {
		t.Fatalf("missing id status=%d body=%s", missingID.Code, missingID.Body.String())
	}

	notFoundRequest := httptest.NewRequest(http.MethodGet, "/api/wecom/flows/wc_missing", nil)
	notFoundRequest.SetPathValue("id", "wc_missing")
	notFound := httptest.NewRecorder()
	handler.handlePollWecomFlow(notFound, notFoundRequest)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("missing flow status=%d body=%s", notFound.Code, notFound.Body.String())
	}

	now := time.Now()
	handler.storeWecomFlow(&wecomFlow{
		ID: "wc_terminal", Status: wecomStatusConfirmed, BotID: "bot-id",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	terminalRequest := httptest.NewRequest(http.MethodGet, "/api/wecom/flows/wc_terminal", nil)
	terminalResponse := httptest.NewRecorder()
	terminalRequest.SetPathValue("id", " wc_terminal ")
	handler.handlePollWecomFlow(terminalResponse, terminalRequest)
	if terminalResponse.Code != http.StatusOK ||
		!strings.Contains(terminalResponse.Body.String(), `"status":"confirmed"`) ||
		!strings.Contains(terminalResponse.Body.String(), `"bot_id":"bot-id"`) {
		t.Fatalf("terminal status=%d body=%s", terminalResponse.Code, terminalResponse.Body.String())
	}

	for name, response := range map[string]struct {
		status int
		body   string
		ok     bool
	}{
		"success": {status: http.StatusOK, body: `{"value":"ready"}`, ok: true},
		"status":  {status: http.StatusBadGateway, body: "upstream down"},
		"decode":  {status: http.StatusOK, body: "{"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(response.status)
				_, _ = w.Write([]byte(response.body))
			}))
			defer server.Close()
			var output map[string]string
			err := doWecomJSONGet(context.Background(), server.URL, &output)
			if response.ok {
				if err != nil || output["value"] != "ready" {
					t.Fatalf("output=%#v err=%v", output, err)
				}
			} else if err == nil {
				t.Fatal("invalid upstream response succeeded")
			}
		})
	}
	if err := doWecomJSONGet(context.Background(), "://bad", &map[string]string{}); err == nil {
		t.Fatal("invalid request URL succeeded")
	}
}
