package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentCapabilitiesRequestLimitAcceptsWorstCaseRetainedValues(
	t *testing.T,
) {
	values := make([]string, agentCapabilityValuesLimit)
	for index := range values {
		values[index] = fmt.Sprintf(
			"skill-%04d-%s",
			index,
			strings.Repeat("\\", 900),
		)
	}
	requestBody, err := json.Marshal(agentCapabilitiesPatchRequest{
		ExpectedRevision: "sha256:revision",
		Skills: capabilityPolicyRequest(
			capabilityModeSelected,
			values...,
		),
	})
	if err != nil {
		t.Fatalf("json.Marshal(request) error = %v", err)
	}
	if len(requestBody) <= agentRequestMaxBytes ||
		int64(len(requestBody)) >= agentCapabilitiesRequestMaxBytes {
		t.Fatalf(
			"request bytes = %d, want between %d and %d",
			len(requestBody),
			agentRequestMaxBytes,
			agentCapabilitiesRequestMaxBytes,
		)
	}

	request := httptest.NewRequest("PATCH", "/", strings.NewReader(string(requestBody)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	var decoded agentCapabilitiesPatchRequest
	if !decodeAgentRequestWithMaxBytes(
		recorder,
		request,
		&decoded,
		agentCapabilitiesRequestMaxBytes,
	) {
		t.Fatalf(
			"bounded capability request was rejected: status=%d body=%s",
			recorder.Code,
			recorder.Body.String(),
		)
	}
	if decoded.Skills == nil || decoded.Skills.Values == nil ||
		len(*decoded.Skills.Values) != agentCapabilityValuesLimit {
		t.Fatalf("decoded request = %#v", decoded)
	}
}

func TestAgentCapabilitiesFilePermissionPreservesExactExistingMode(
	t *testing.T,
) {
	if got := agentCapabilitiesFilePermission(0, true); got != 0 {
		t.Fatalf("existing mode = %#o, want 0000", got)
	}
	if got := agentCapabilitiesFilePermission(0, false); got != 0o644 {
		t.Fatalf("new mode = %#o, want 0644", got)
	}
	if got := agentCapabilitiesFilePermission(0o640, true); got != 0o640 {
		t.Fatalf("existing mode = %#o, want 0640", got)
	}
}
