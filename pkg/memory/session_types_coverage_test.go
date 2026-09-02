package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSessionSnapshotValidationRejectsRuntimeOnlyAndMalformedState(t *testing.T) {
	validScope := json.RawMessage(`{"version":1,"agent_id":"main","channel":"pico","values":{}}`)
	valid := SessionSnapshotReplacement{
		Key: "key", Scope: validScope,
		History: []providers.Message{{Role: "user", Content: "hello"}},
	}
	if err := validateSnapshotReplacement(valid); err != nil {
		t.Fatal(err)
	}
	for name, replacement := range map[string]SessionSnapshotReplacement{
		"blank key":     {Scope: validScope},
		"spaced key":    {Key: " key ", Scope: validScope},
		"missing scope": {Key: "key"},
		"invalid scope": {Key: "key", Scope: json.RawMessage(`{`)},
		"transient thought": {
			Key: "key", Scope: validScope,
			History: []providers.Message{{Role: "assistant", ReasoningContent: "thought"}},
		},
		"message prompt metadata": {
			Key: "key", Scope: validScope,
			History: []providers.Message{{Role: "user", Content: "x", PromptLayer: "runtime"}},
		},
		"system prompt metadata": {
			Key: "key", Scope: validScope,
			History: []providers.Message{{
				Role: "system", Content: "x",
				SystemParts: []providers.ContentBlock{{Type: "text", Text: "x", PromptSlot: "runtime"}},
			}},
		},
		"tool runtime name": {
			Key: "key", Scope: validScope,
			History: []providers.Message{{
				Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "call", Name: "runtime"}},
			}},
		},
		"tool runtime arguments": {
			Key: "key", Scope: validScope,
			History: []providers.Message{{
				Role: "assistant", ToolCalls: []providers.ToolCall{{
					ID: "call", Arguments: map[string]any{"x": true},
				}},
			}},
		},
		"oversized message": {
			Key: "key", Scope: validScope,
			History: []providers.Message{{Role: strings.Repeat("r", 1025), Content: "x"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSnapshotReplacement(replacement); err == nil {
				t.Fatal("invalid snapshot replacement accepted")
			}
		})
	}
}

func TestSessionMetadataComparisonScopeAndMainAliasBoundaries(t *testing.T) {
	if _, err := canonicalSessionScopeJSON(nil); err == nil {
		t.Fatal("missing session scope accepted")
	}
	if _, err := canonicalSessionScopeJSON(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid session scope accepted")
	}
	canonical, err := canonicalSessionScopeJSON(json.RawMessage(`{"values":{},"channel":"pico"}`))
	if err != nil || string(canonical) != `{"channel":"pico","values":{}}` {
		t.Fatalf("canonical scope = %s, %v", canonical, err)
	}
	left := SessionMeta{Key: "key", Scope: json.RawMessage(`{"a":1}`), Aliases: []string{"alias"}}
	right := cloneSessionMeta(left)
	equal, err := persistedSessionMetaEqual(left, right)
	if err != nil || !equal {
		t.Fatalf("equal metadata = %t, %v", equal, err)
	}
	right.Summary = "different"
	equal, err = persistedSessionMetaEqual(left, right)
	if err != nil || equal {
		t.Fatalf("different metadata = %t, %v", equal, err)
	}

	if !isMainSessionAlias("agent:main:main") || isMainSessionAlias("agent:main:other") {
		t.Fatal("plain main-session alias classification failed")
	}
	for _, agentID := range []string{"main", "Main", "MAIN"} {
		digest := sha256.Sum256([]byte("agent:" + agentID + ":main"))
		if !isMainSessionAlias("sk_v1_" + hex.EncodeToString(digest[:])) {
			t.Fatalf("hashed main alias for %q was rejected", agentID)
		}
	}
	revision, err := snapshotRevision("key", nil, SessionMeta{Key: "key"})
	if err != nil || !strings.HasPrefix(revision, "ssr_v1_") {
		t.Fatalf("snapshot revision = %q, %v", revision, err)
	}
	aliases := normalizeAliases("key", []string{" key ", " alias ", "alias", ""})
	if len(aliases) != 1 || aliases[0] != "alias" || normalizeAliases("key", nil) != nil {
		t.Fatalf("normalized aliases = %#v", aliases)
	}
}
