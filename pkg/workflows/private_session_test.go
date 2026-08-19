package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

type revisionFenceAgentRunner struct {
	captureRevision string
	captures        []ReadOnlySessionRef
	agentCalls      int
}

func (r *revisionFenceAgentRunner) CaptureReadOnlySession(
	_ context.Context,
	ref ReadOnlySessionRef,
) (*FrozenReadOnlySession, error) {
	r.captures = append(r.captures, ref)
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    ref.AgentID,
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"fixture"},
		Values:     map[string]string{"fixture": "revision-fence"},
	}
	return &FrozenReadOnlySession{
		AgentID: ref.AgentID,
		Snapshot: session.SessionSnapshot{
			Key:      session.BuildSessionKey(scope),
			Scope:    &scope,
			Revision: r.captureRevision,
		},
		HistoryRevision: "sha256:revision-fence-fixture",
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (r *revisionFenceAgentRunner) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	r.agentCalls++
	const response = `{"ask_user":false,"reason":"captured revision is sufficient","questions":[]}`
	structured := ValidateAgentStructuredOutput(response, req.Output)
	if !structured.Valid {
		return nil, errors.New("revision-fence fixture produced invalid gate output")
	}
	return map[string]any{
		"text":             response,
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

func revisionFenceGateCompilation(t *testing.T) *GateCompilation {
	t.Helper()
	compilation, err := CompileGateWorkflow("Revision-fenced gate", []GateSpec{{
		ID:       "discussion",
		Kind:     GateAIWorkingContext,
		AgentID:  "main",
		Criteria: "Ask only when captured evidence cannot resolve the finding.",
		Title:    "Discuss finding",
	}}, map[string]any{"finding": "bounded"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	return compilation
}

func TestReadOnlySessionRefExpectedRevisionIsPrivateBoundedAndCloned(t *testing.T) {
	const revision = "ssr_v1_exact_projection"
	ref := ReadOnlySessionRef{
		AgentID:          "main",
		Session:          "agent:main:web:revision-fence",
		ExpectedRevision: revision,
	}
	normalized, err := normalizePrivateReadOnlySessionRef(ref)
	if err != nil || normalized != ref {
		t.Fatalf("normalizePrivateReadOnlySessionRef() = (%#v, %v), want exact ref", normalized, err)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("json.Marshal(ref) error = %v", err)
	}
	if bytes.Contains(encoded, []byte(revision)) || bytes.Contains(encoded, []byte("ExpectedRevision")) {
		t.Fatalf("public ref JSON exposed revision capability: %s", encoded)
	}

	request := RunRequest{PrivateRoot: &PrivateRootRequest{
		Values:          map[string]any{},
		ReadOnlySession: &ref,
	}}
	cloned, err := cloneRunRequestForExecution(request)
	if err != nil {
		t.Fatalf("cloneRunRequestForExecution() error = %v", err)
	}
	if cloned.PrivateRoot == nil || cloned.PrivateRoot.ReadOnlySession == nil ||
		*cloned.PrivateRoot.ReadOnlySession != ref ||
		cloned.PrivateRoot.ReadOnlySession == request.PrivateRoot.ReadOnlySession {
		t.Fatalf("cloned revision ref = %#v, want detached %#v", cloned.PrivateRoot, ref)
	}

	invalid := []string{
		" surrounded ",
		string([]byte{0xff}),
		strings.Repeat("r", maxPrivateSessionRevisionBytes+1),
	}
	for _, candidate := range invalid {
		t.Run("invalid", func(t *testing.T) {
			bad := ref
			bad.ExpectedRevision = candidate
			if _, err := normalizePrivateReadOnlySessionRef(bad); !errors.Is(err, ErrPrivateWorkflowContext) {
				t.Fatalf("normalize revision error = %v, want %v", err, ErrPrivateWorkflowContext)
			}
		})
	}
	compatible := ref
	compatible.ExpectedRevision = ""
	if got, err := normalizePrivateReadOnlySessionRef(compatible); err != nil || got != compatible {
		t.Fatalf("empty compatibility revision = (%#v, %v), want accepted", got, err)
	}
}

func TestPrivateRootRevisionFencePrecedesDurableCreation(t *testing.T) {
	tests := []struct {
		name             string
		expected         string
		captured         string
		wantErr          bool
		wantPersistedRev string
		wantAgentCalls   int
	}{
		{
			name:             "exact revision",
			expected:         "ssr_v1_projected",
			captured:         "ssr_v1_projected",
			wantPersistedRev: "",
			wantAgentCalls:   1,
		},
		{
			name:             "empty compatibility fence",
			captured:         "ssr_v1_current",
			wantPersistedRev: "",
			wantAgentCalls:   1,
		},
		{
			name:     "stale revision",
			expected: "ssr_v1_projected",
			captured: "ssr_v1_mutated",
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			agents := &revisionFenceAgentRunner{captureRevision: test.captured}
			compilation := revisionFenceGateCompilation(t)
			compilation.PrivateRoot.ReadOnlySession = &ReadOnlySessionRef{
				AgentID:          "main",
				Session:          "agent:main:web:revision-fence",
				ExpectedRevision: test.expected,
			}
			result, runErr := (&Executor{
				WorkspaceDir: workspace,
				Store:        store,
				Agents:       agents,
			}).Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/revision-fenced-gate",
				PrivateRoot: compilation.PrivateRoot,
			})
			if test.wantErr {
				if result != nil || !errors.Is(runErr, ErrPrivateWorkflowContext) {
					t.Fatalf("Run() = (%#v, %v), want pre-create revision failure", result, runErr)
				}
				runs, listErr := store.ListRuns(context.Background())
				if listErr != nil || len(runs) != 0 {
					t.Fatalf("durable runs after stale capture = %#v, err %v", runs, listErr)
				}
			} else {
				if runErr != nil || result == nil {
					t.Fatalf("Run() = (%#v, %v), want captured working gate", result, runErr)
				}
				persisted, getErr := store.GetRun(context.Background(), result.RunID)
				if getErr != nil || persisted.privateRoot == nil || persisted.privateRoot.ReadOnlySession == nil {
					t.Fatalf("GetRun() = (%#v, %v), want captured private root", persisted, getErr)
				}
				if got := persisted.privateRoot.ReadOnlySession.Snapshot.Revision; got != test.wantPersistedRev {
					t.Fatalf("persisted source revision = %q, want stripped %q", got, test.wantPersistedRev)
				}
			}
			if len(agents.captures) != 1 || agents.captures[0].ExpectedRevision != test.expected {
				t.Fatalf("capture refs = %#v, want exact revision %q", agents.captures, test.expected)
			}
			if agents.agentCalls != test.wantAgentCalls {
				t.Fatalf("agent calls = %d, want %d", agents.agentCalls, test.wantAgentCalls)
			}
		})
	}
}

func TestPrivateRootMalformedRevisionFailsBeforeCaptureOrDurableCreation(t *testing.T) {
	invalid := []string{
		" stale ",
		string([]byte{0xff}),
		strings.Repeat("r", maxPrivateSessionRevisionBytes+1),
	}
	for _, expected := range invalid {
		workspace := t.TempDir()
		store := NewFileRunStore(workspace)
		agents := &revisionFenceAgentRunner{captureRevision: "unexpected"}
		compilation := revisionFenceGateCompilation(t)
		compilation.PrivateRoot.ReadOnlySession = &ReadOnlySessionRef{
			AgentID:          "main",
			Session:          "agent:main:web:revision-fence",
			ExpectedRevision: expected,
		}
		result, runErr := (&Executor{
			WorkspaceDir: workspace,
			Store:        store,
			Agents:       agents,
		}).Run(context.Background(), RunRequest{
			Workflow:    compilation.Workflow,
			WorkflowRef: "inline/malformed-revision-gate",
			PrivateRoot: compilation.PrivateRoot,
		})
		if result != nil || !errors.Is(runErr, ErrPrivateWorkflowContext) {
			t.Fatalf("Run(malformed revision) = (%#v, %v), want private-context failure", result, runErr)
		}
		if len(agents.captures) != 0 || agents.agentCalls != 0 {
			t.Fatalf("malformed revision invoked agents: captures=%d calls=%d", len(agents.captures), agents.agentCalls)
		}
		runs, listErr := store.ListRuns(context.Background())
		if listErr != nil || len(runs) != 0 {
			t.Fatalf("durable runs after malformed revision = %#v, err %v", runs, listErr)
		}
	}
}

func TestPrivateGateAdmissionRejectsPostCompileWorkflowMutation(t *testing.T) {
	compilation, err := CompileGateWorkflow("Stamped gate", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Approve?"},
	}}, map[string]any{"private": "value"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	compilation.Workflow.Name = "mutated after compilation"
	result, runErr := (&Executor{WorkspaceDir: t.TempDir()}).Run(
		context.Background(),
		RunRequest{
			Workflow:    compilation.Workflow,
			WorkflowRef: "inline/mutated-private-gate",
			PrivateRoot: compilation.PrivateRoot,
		},
	)
	if result != nil || !errors.Is(runErr, ErrPrivateWorkflowContext) {
		t.Fatalf("Run() = (%#v, %v), want pre-create private-context failure", result, runErr)
	}
}

func TestPrivateGateAdmissionBindsValuesAndRejectsFreshRetryProvenance(t *testing.T) {
	compile := func(t *testing.T) *GateCompilation {
		t.Helper()
		compilation, err := CompileGateWorkflow("Bound gate", []GateSpec{{
			ID:        "policy",
			Kind:      GateDeterministic,
			When:      "false",
			Title:     "Policy",
			Questions: []any{"Approve?"},
		}}, map[string]any{"private": "original"})
		if err != nil {
			t.Fatalf("CompileGateWorkflow() error = %v", err)
		}
		return compilation
	}

	tests := []struct {
		name   string
		mutate func(*GateCompilation, *RunRequest)
	}{
		{
			name: "mutated values",
			mutate: func(compilation *GateCompilation, _ *RunRequest) {
				compilation.PrivateRoot.Values[workflowGateSubjectInput] = map[string]any{
					"private": "changed",
				}
			},
		},
		{
			name: "reconstructed root",
			mutate: func(compilation *GateCompilation, request *RunRequest) {
				request.PrivateRoot = &PrivateRootRequest{
					Values: cloneMap(compilation.PrivateRoot.Values),
				}
			},
		},
		{
			name: "fresh capture labeled retry",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				request.RetryOfRunID = "run_prior"
			},
		},
		{
			name: "whitespace retry provenance",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				request.RetryOfRunID = " "
			},
		},
		{
			name: "whitespace parent",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				request.ParentRunID = " "
			},
		},
		{
			name: "whitespace caller job",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				request.CallerJobID = " "
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compilation := compile(t)
			workspace := t.TempDir()
			request := RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/bound-private-gate",
				PrivateRoot: compilation.PrivateRoot,
			}
			test.mutate(compilation, &request)
			result, runErr := (&Executor{WorkspaceDir: workspace}).Run(
				context.Background(),
				request,
			)
			if result != nil || !errors.Is(runErr, ErrPrivateWorkflowContext) {
				t.Fatalf(
					"Run() = (%#v, %v), want pre-create private-context failure",
					result,
					runErr,
				)
			}
			runs, listErr := NewFileRunStore(workspace).ListRuns(context.Background())
			if listErr != nil {
				t.Fatalf("ListRuns() error = %v", listErr)
			}
			if len(runs) != 0 {
				t.Fatalf("durable runs = %#v, want none", runs)
			}
		})
	}
}

func TestFrozenReadOnlySessionJSONPreservesRuntimeMessageMetadata(t *testing.T) {
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"fixture"},
		Values:     map[string]string{"fixture": "private-json"},
	}
	want := FrozenReadOnlySession{
		AgentID: "main",
		Snapshot: session.SessionSnapshot{
			Key: session.BuildSessionKey(scope),
			History: []providers.Message{{
				Role:         "assistant",
				Content:      "decision context",
				PromptLayer:  "history",
				PromptSlot:   "turn",
				PromptSource: "session",
				SystemParts: []providers.ContentBlock{{
					Type:         "text",
					Text:         "system context",
					PromptLayer:  "system",
					PromptSlot:   "policy",
					PromptSource: "config",
				}},
				ToolCalls: []providers.ToolCall{
					{
						ID:               "call-1",
						Name:             "review",
						Arguments:        map[string]any{"line": json.Number("9007199254740993")},
						ThoughtSignature: "opaque-signature",
					},
					{
						ID:        "call-2",
						Name:      "preserve-empty-map",
						Arguments: map[string]any{},
						Function: &providers.FunctionCall{
							Name:      "fallback-must-remain-suppressed",
							Arguments: `{"fallback":true}`,
						},
					},
				},
			}},
			Summary: "summary",
			Scope:   &scope,
		},
		HistoryRevision: "sha256:exact",
		FrozenMedia: media.FrozenSet{
			Version: media.FrozenSetVersion,
			Blobs:   []media.FrozenBlob{},
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got FrozenReadOnlySession
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestFrozenReadOnlySessionMediaRoundTripCloneAndBinding(t *testing.T) {
	first := frozenReadOnlySessionWithMedia(t, "aGVsbG8=")
	second := frozenReadOnlySessionWithMedia(t, "d29ybGQ=")
	if first.HistoryRevision != second.HistoryRevision {
		t.Fatalf("fixture history revisions differ: %q and %q", first.HistoryRevision, second.HistoryRevision)
	}

	cloned := cloneFrozenReadOnlySession(&first)
	cloned.FrozenMedia.Blobs[0].Data[0] ^= 0xff
	if bytes.Equal(cloned.FrozenMedia.Blobs[0].Data, first.FrozenMedia.Blobs[0].Data) {
		t.Fatal("clone shares frozen media blob bytes")
	}
	if err := validateFrozenReadOnlySession(&first, "main"); err != nil {
		t.Fatalf("mutating clone corrupted source: %v", err)
	}

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var roundTrip FrozenReadOnlySession
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(roundTrip, first) {
		t.Fatalf("media round trip = %#v, want %#v", roundTrip, first)
	}
	if roundTrip.HistoryRevision != "sha256:frozen-reference-history" ||
		!strings.HasPrefix(
			roundTrip.Snapshot.History[0].Attachments[0].Ref,
			"frozen-media://sha256/",
		) {
		t.Fatalf("round trip changed frozen-reference history semantics: %#v", roundTrip)
	}

	rootRevision := func(value FrozenReadOnlySession) string {
		t.Helper()
		payload, marshalErr := marshalPrivateWorkflowJSON(frozenWorkflowRootPayload{
			Values:          map[string]any{"subject": "same"},
			ReadOnlySession: &value,
		})
		if marshalErr != nil {
			t.Fatalf("marshal root payload: %v", marshalErr)
		}
		return privateWorkflowRootRevision(payload)
	}
	rootOne := &frozenWorkflowRootContext{
		Values:          map[string]any{"subject": "same"},
		ReadOnlySession: &first,
		Revision:        rootRevision(first),
	}
	rootTwo := &frozenWorkflowRootContext{
		Values:          map[string]any{"subject": "same"},
		ReadOnlySession: &second,
		Revision:        rootRevision(second),
	}
	if rootOne.Revision == rootTwo.Revision {
		t.Fatal("private root revision does not bind frozen media")
	}
	binding := func(root *frozenWorkflowRootContext) string {
		t.Helper()
		value, bindingErr := privateWorkflowRunBinding(&Run{
			ID:          "wr_media_binding",
			WorkflowRef: "inline/media-binding",
			privateRoot: root,
			execution:   &workflowExecutionState{WorkflowRevision: "sha256:workflow"},
		})
		if bindingErr != nil {
			t.Fatalf("privateWorkflowRunBinding() error = %v", bindingErr)
		}
		return value
	}
	if binding(rootOne) == binding(rootTwo) {
		t.Fatal("private run binding does not bind frozen media")
	}
}

func TestFrozenReadOnlySessionMediaTamperAndStrictJSONFailClosed(t *testing.T) {
	valid := frozenReadOnlySessionWithMedia(t, "aGVsbG8=")
	mutations := []struct {
		name   string
		mutate func(*FrozenReadOnlySession)
	}{
		{
			name: "version zero",
			mutate: func(value *FrozenReadOnlySession) {
				value.FrozenMedia.Version = 0
			},
		},
		{
			name: "blob bytes",
			mutate: func(value *FrozenReadOnlySession) {
				value.FrozenMedia.Blobs[0].Data[0] ^= 0xff
			},
		},
		{
			name: "missing closure",
			mutate: func(value *FrozenReadOnlySession) {
				value.FrozenMedia = media.FrozenSet{Version: media.FrozenSetVersion}
			},
		},
		{
			name: "metadata mismatch",
			mutate: func(value *FrozenReadOnlySession) {
				value.Snapshot.History[0].Attachments[0].ContentType = "application/json"
			},
		},
		{
			name: "canonical key scope mismatch",
			mutate: func(value *FrozenReadOnlySession) {
				value.Snapshot.Key = session.BuildOpaqueSessionKey("different-private-scope")
			},
		},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneFrozenReadOnlySession(&valid)
			test.mutate(candidate)
			if err := validateFrozenReadOnlySession(candidate, "main"); !errors.Is(err, ErrPrivateWorkflowContext) {
				t.Fatalf("validate error = %v, want %v", err, ErrPrivateWorkflowContext)
			}
			if _, err := json.Marshal(candidate); !errors.Is(err, ErrPrivateWorkflowContext) {
				t.Fatalf("Marshal() error = %v, want %v", err, ErrPrivateWorkflowContext)
			}
		})
	}

	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("Marshal(valid) error = %v", err)
	}
	withoutFrozenMedia := func() []byte {
		var envelope map[string]json.RawMessage
		if unmarshalErr := json.Unmarshal(encoded, &envelope); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		delete(envelope, "frozen_media")
		result, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return result
	}()
	versionZero := func() []byte {
		var envelope map[string]json.RawMessage
		if unmarshalErr := json.Unmarshal(encoded, &envelope); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		envelope["frozen_media"] = json.RawMessage(`{"version":0}`)
		result, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return result
	}()
	duplicateFrozenMedia := bytes.Replace(
		encoded,
		[]byte(`"frozen_media":`),
		[]byte(`"frozen_media":{"version":1},"frozen_media":`),
		1,
	)
	unknownField := append([]byte(`{"unknown":true,`), encoded[1:]...)
	unpairedSurrogate := bytes.Replace(
		encoded,
		[]byte(`"agent_id":"main"`),
		[]byte(`"agent_id":"\ud800"`),
		1,
	)
	for name, raw := range map[string][]byte{
		"missing frozen media": withoutFrozenMedia,
		"version zero":         versionZero,
		"duplicate field":      duplicateFrozenMedia,
		"unknown field":        unknownField,
		"unpaired surrogate":   unpairedSurrogate,
		"null":                 []byte("null"),
	} {
		t.Run(name, func(t *testing.T) {
			var decoded FrozenReadOnlySession
			if err := json.Unmarshal(raw, &decoded); err == nil {
				t.Fatal("Unmarshal() succeeded")
			}
		})
	}
}

func frozenReadOnlySessionWithMedia(t *testing.T, base64Data string) FrozenReadOnlySession {
	t.Helper()
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"fixture"},
		Values:     map[string]string{"fixture": "private-media"},
	}
	snapshot, frozenMedia, err := session.FreezeSessionSnapshotMedia(
		context.Background(),
		session.SessionSnapshot{
			Key: session.BuildSessionKey(scope),
			History: []providers.Message{{
				Role:    "user",
				Content: "review this evidence",
				Attachments: []providers.Attachment{{
					Ref:         "data:text/plain;base64," + base64Data,
					Filename:    "evidence.txt",
					ContentType: "text/plain",
				}},
			}},
			Scope: &scope,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
	}
	return FrozenReadOnlySession{
		AgentID:         "main",
		Snapshot:        snapshot,
		HistoryRevision: "sha256:frozen-reference-history",
		FrozenMedia:     frozenMedia,
	}
}
