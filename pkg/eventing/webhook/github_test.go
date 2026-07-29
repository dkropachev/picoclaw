package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	githubTestConnector = "github-main"
	githubTestSecret    = "github-webhook-secret-32-bytes!!!"
	githubTestDelivery  = "Delivery-01234567-89ab-CDEF"
)

func newGitHubTestController(
	t *testing.T,
	store Inserter,
	maxPayload int,
) (*Controller, Generation) {
	t.Helper()
	backend, err := NewBackend(BackendConfig{
		Store: store,
		ConnectorSecrets: map[string]string{
			githubTestConnector: githubTestSecret,
		},
		ConnectorFormats: map[string]string{
			githubTestConnector: "github",
		},
		MaxPayloadBytes: maxPayload,
	})
	require.NoError(t, err)
	controller := NewController()
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, controller.Deactivate(context.Background(), generation))
	})
	return controller, generation
}

func githubSignedRequest(
	secret string,
	event string,
	delivery string,
	body string,
) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		RoutePrefix+githubTestConnector,
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(githubSignatureHeader, githubSignature(secret, body))
	request.Header.Set(githubEventHeader, event)
	request.Header.Set(githubDeliveryHeader, delivery)
	return request
}

func githubSignature(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return githubSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubOfficialHMACVector(t *testing.T) {
	t.Parallel()
	const (
		secret    = "It's a Secret to Everybody"
		payload   = "Hello, World!"
		signature = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	)
	digest, ok := canonicalGitHubDigest(signature)
	require.True(t, ok)
	assert.True(t, verifyGitHubSignature([]byte(secret), []byte(payload), digest))
	assert.False(t, verifyGitHubSignature([]byte(secret), []byte(payload+"!"), digest))
}

func TestNewBackendGitHubFormatValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		secret  string
		formats map[string]string
	}{
		{
			name:    "unknown format",
			secret:  githubTestSecret,
			formats: map[string]string{githubTestConnector: "GitHub"},
		},
		{
			name:    "orphan format",
			secret:  githubTestSecret,
			formats: map[string]string{githubTestConnector: "github", "orphan": "github"},
		},
		{
			name:    "short GitHub secret",
			secret:  strings.Repeat("s", minimumGitHubSecretBytes-1),
			formats: map[string]string{githubTestConnector: "github"},
		},
		{
			name:    "long GitHub secret",
			secret:  strings.Repeat("s", maximumGitHubSecretBytes+1),
			formats: map[string]string{githubTestConnector: "github"},
		},
		{
			name:    "untrimmed GitHub secret",
			secret:  " " + githubTestSecret,
			formats: map[string]string{githubTestConnector: "github"},
		},
		{
			name:    "invalid UTF-8 GitHub secret",
			secret:  strings.Repeat("s", minimumGitHubSecretBytes) + string([]byte{0xff}),
			formats: map[string]string{githubTestConnector: "github"},
		},
		{
			name:    "empty format remains Standard Webhooks",
			secret:  githubTestSecret,
			formats: map[string]string{githubTestConnector: ""},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend, err := NewBackend(BackendConfig{
				Store: newMemoryInserter(),
				ConnectorSecrets: map[string]string{
					githubTestConnector: test.secret,
				},
				ConnectorFormats: test.formats,
				MaxPayloadBytes:  1024,
			})
			require.Error(t, err)
			assert.Nil(t, backend)
			assert.NotContains(t, err.Error(), test.secret)
		})
	}

	backend, err := NewBackend(BackendConfig{
		Store: newMemoryInserter(),
		ConnectorSecrets: map[string]string{
			githubTestConnector: githubTestSecret,
		},
		ConnectorFormats: map[string]string{
			githubTestConnector: "github",
		},
		MaxPayloadBytes: 1024,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, backend.ConnectorCount())
}

func TestNewBackendConnectorFormatIsImmutable(t *testing.T) {
	t.Parallel()
	secrets := map[string]string{githubTestConnector: githubTestSecret}
	formats := map[string]string{githubTestConnector: "github"}
	store := newMemoryInserter()
	backend, err := NewBackend(BackendConfig{
		Store:            store,
		ConnectorSecrets: secrets,
		ConnectorFormats: formats,
		MaxPayloadBytes:  1024,
	})
	require.NoError(t, err)

	secrets[githubTestConnector] = testSecret
	formats[githubTestConnector] = "standard"
	controller := NewController()
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, controller.Deactivate(context.Background(), generation))
	})

	githubResponse := performRequest(
		controller,
		githubSignedRequest(
			githubTestSecret,
			"ping",
			githubTestDelivery,
			`{"zen":"Keep it logically awesome."}`,
		),
	)
	assert.Equal(t, http.StatusAccepted, githubResponse.Code)

	standardRequest := signedRequest(
		t,
		testSecret,
		"standard-after-mutation",
		`{"type":"changed","payload":{}}`,
	)
	standardRequest.URL.Path = RoutePrefix + githubTestConnector
	standardResponse := performRequest(controller, standardRequest)
	assert.Equal(t, http.StatusUnauthorized, standardResponse.Code)
	assert.Len(t, store.recordedInputs(), 1)
}

func TestBackendSelectsExactlyOneAuthenticationSchemePerConnector(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	backend, err := NewBackend(BackendConfig{
		Store: store,
		ConnectorSecrets: map[string]string{
			testConnector:       testSecret,
			githubTestConnector: githubTestSecret,
		},
		ConnectorFormats: map[string]string{
			githubTestConnector: "github",
		},
		MaxPayloadBytes: 1024,
	})
	require.NoError(t, err)
	controller := NewController()
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, controller.Deactivate(context.Background(), generation))
	})

	standard := performRequest(
		controller,
		signedRequest(
			t,
			testSecret,
			"mixed-standard",
			`{"type":"standard","payload":{}}`,
		),
	)
	assert.Equal(t, http.StatusAccepted, standard.Code)
	github := performRequest(
		controller,
		githubSignedRequest(
			githubTestSecret,
			"ping",
			"mixed-github",
			`{"zen":"Approachable is better than simple."}`,
		),
	)
	assert.Equal(t, http.StatusAccepted, github.Code)

	githubOnStandard := githubSignedRequest(
		githubTestSecret,
		"ping",
		"wrong-github-scheme",
		`{}`,
	)
	githubOnStandard.URL.Path = RoutePrefix + testConnector
	assert.Equal(
		t,
		http.StatusUnauthorized,
		performRequest(controller, githubOnStandard).Code,
	)

	standardOnGitHub := signedRequest(
		t,
		testSecret,
		"wrong-standard-scheme",
		`{"type":"standard","payload":{}}`,
	)
	standardOnGitHub.URL.Path = RoutePrefix + githubTestConnector
	assert.Equal(
		t,
		http.StatusUnauthorized,
		performRequest(controller, standardOnGitHub).Code,
	)
	assert.Len(t, store.recordedInputs(), 2)
}

func TestGitHubAuthenticationHeadersRejectedBeforeBodyRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "missing signature",
			mutate: func(request *http.Request) {
				request.Header.Del(githubSignatureHeader)
			},
		},
		{
			name: "missing event",
			mutate: func(request *http.Request) {
				request.Header.Del(githubEventHeader)
			},
		},
		{
			name: "missing delivery",
			mutate: func(request *http.Request) {
				request.Header.Del(githubDeliveryHeader)
			},
		},
		{
			name: "duplicate signature",
			mutate: func(request *http.Request) {
				request.Header.Add(githubSignatureHeader, githubSignature(githubTestSecret, `{}`))
			},
		},
		{
			name: "duplicate event",
			mutate: func(request *http.Request) {
				request.Header.Add(githubEventHeader, "push")
			},
		},
		{
			name: "duplicate delivery",
			mutate: func(request *http.Request) {
				request.Header.Add(githubDeliveryHeader, "another-delivery")
			},
		},
		{
			name: "noncanonical digest case",
			mutate: func(request *http.Request) {
				digest := strings.TrimPrefix(
					request.Header.Get(githubSignatureHeader),
					githubSignaturePrefix,
				)
				request.Header.Set(githubSignatureHeader, githubSignaturePrefix+strings.ToUpper(digest))
			},
		},
		{
			name: "wrong digest length",
			mutate: func(request *http.Request) {
				request.Header.Set(githubSignatureHeader, githubSignaturePrefix+"00")
			},
		},
		{
			name: "noncanonical event",
			mutate: func(request *http.Request) {
				request.Header.Set(githubEventHeader, "Pull_Request")
			},
		},
		{
			name: "untrimmed delivery",
			mutate: func(request *http.Request) {
				request.Header.Set(githubDeliveryHeader, " delivery")
			},
		},
		{
			name: "comma-coalesced delivery",
			mutate: func(request *http.Request) {
				request.Header.Set(githubDeliveryHeader, "first,second")
			},
		},
		{
			name: "control in delivery",
			mutate: func(request *http.Request) {
				request.Header.Set(githubDeliveryHeader, "first\nsecond")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			controller, _ := newGitHubTestController(t, newMemoryInserter(), 1024)
			request := githubSignedRequest(
				githubTestSecret,
				"pull_request",
				githubTestDelivery,
				`{}`,
			)
			test.mutate(request)
			body := &observedBody{reader: strings.NewReader(`{}`)}
			request.Body = body
			response := performRequest(controller, request)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
			assert.Zero(t, body.reads.Load())
		})
	}
}

func TestGitHubRawBodyHMAC(t *testing.T) {
	t.Parallel()
	controller, _ := newGitHubTestController(t, newMemoryInserter(), 1024)
	body := `{"action":"opened"}`

	badDigest := githubSignedRequest(
		githubTestSecret,
		"pull_request",
		githubTestDelivery,
		body,
	)
	badDigest.Header.Set(
		githubSignatureHeader,
		githubSignaturePrefix+strings.Repeat("0", sha256.Size*2),
	)
	observed := &observedBody{reader: strings.NewReader(body)}
	badDigest.Body = observed
	response := performRequest(controller, badDigest)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Positive(t, observed.reads.Load())

	changedBody := githubSignedRequest(
		githubTestSecret,
		"pull_request",
		"changed-raw-body",
		body,
	)
	changedBody.Body = &observedBody{reader: strings.NewReader("{ \"action\":\"opened\" }")}
	response = performRequest(controller, changedBody)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestGitHubNormalizationPreservesSemanticPayload(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	controller, _ := newGitHubTestController(t, store, 1<<20)
	body := `{
		"action":"opened",
		"number":9007199254740993123456789,
		"created_at":"2026-07-29T11:22:33Z",
		"sender":{
			"login":"octocat",
			"id":1,
			"node_id":"MDQ6VXNlcjE=",
			"type":"User",
			"html_url":"https://github.com/octocat"
		},
		"repository":{
			"id":2,
			"node_id":"MDEwOlJlcG9zaXRvcnky",
			"name":"hello-world",
			"full_name":"octocat/hello-world",
			"html_url":"https://github.com/octocat/hello-world",
			"default_branch":"main",
			"visibility":"public",
			"private":false,
			"fork":true,
			"owner":{"login":"octocat"}
		}
	}`
	response := performRequest(
		controller,
		githubSignedRequest(
			githubTestSecret,
			"pull_request",
			githubTestDelivery,
			body,
		),
	)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())

	inputs := store.recordedInputs()
	require.Len(t, inputs, 1)
	input := inputs[0]
	assert.Equal(t, "github", input.Source)
	assert.Equal(t, githubTestConnector, input.Connector)
	assert.Equal(t, "pull_request.opened", input.Type)
	assert.Equal(t, githubTestDelivery, input.DedupeKey)
	assert.Nil(t, input.OccurredAt)
	assert.JSONEq(t, body, string(input.Payload))
	assert.Contains(t, string(input.Payload), "9007199254740993123456789")

	require.NotNil(t, input.Actor)
	assert.Equal(t, "MDQ6VXNlcjE=", input.Actor.ID)
	assert.Equal(t, "user", input.Actor.Type)
	assert.Equal(t, "octocat", input.Actor.DisplayName)
	assert.Equal(t, "1", input.Actor.Attributes["database_id"])
	assert.Equal(t, "https://github.com/octocat", input.Actor.Attributes["url"])

	require.NotNil(t, input.Subject)
	assert.Equal(t, "MDEwOlJlcG9zaXRvcnky", input.Subject.ID)
	assert.Equal(t, "repository", input.Subject.Type)
	assert.Equal(t, "octocat/hello-world", input.Subject.Name)
	assert.Equal(t, "octocat", input.Subject.Attributes["owner"])
	assert.Equal(t, "false", input.Subject.Attributes["private"])
	assert.Equal(t, "true", input.Subject.Attributes["fork"])

	assert.Equal(t, "true", input.Attributes["body_authenticated"])
	assert.Equal(t, "false", input.Attributes["headers_authenticated"])
	assert.Equal(t, "hmac-sha256", input.Attributes["signature_algorithm"])
	assert.Equal(t, "octocat/hello-world", input.Attributes["repository_full_name"])
	assert.Equal(t, "public", input.Attributes["repository_visibility"])
}

func TestGitHubOptionalProjectionIsBounded(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	controller, _ := newGitHubTestController(t, store, 1<<20)
	bodyBytes, err := json.Marshal(map[string]any{
		"sender": map[string]any{
			"id":       2,
			"node_id":  strings.Repeat("actor-stable-id", 100),
			"login":    strings.Repeat("é", maxGitHubEntityFieldBytes),
			"html_url": "https://example.invalid/" + strings.Repeat("u", maxGitHubAttributeValueBytes),
		},
		"repository": map[string]any{
			"id":        3,
			"node_id":   strings.Repeat("repository-stable-id", 100),
			"full_name": strings.Repeat("界", maxGitHubEntityFieldBytes),
		},
	})
	require.NoError(t, err)
	response := performRequest(
		controller,
		githubSignedRequest(
			githubTestSecret,
			"push",
			"case-Preserving-Delivery",
			string(bodyBytes),
		),
	)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())

	inputs := store.recordedInputs()
	require.Len(t, inputs, 1)
	require.NotNil(t, inputs[0].Actor)
	require.NotNil(t, inputs[0].Subject)
	assert.Equal(t, "2", inputs[0].Actor.ID)
	assert.Equal(t, "3", inputs[0].Subject.ID)
	assert.LessOrEqual(t, len(inputs[0].Actor.DisplayName), maxGitHubEntityFieldBytes)
	assert.True(t, utf8.ValidString(inputs[0].Actor.DisplayName))
	assert.LessOrEqual(t, len(inputs[0].Actor.Attributes["url"]), maxGitHubAttributeValueBytes)
	assert.LessOrEqual(t, len(inputs[0].Subject.Name), maxGitHubEntityFieldBytes)
	assert.True(t, utf8.ValidString(inputs[0].Subject.Name))
}

func TestGitHubRejectsMalformedSemanticPayload(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"array":            `[]`,
		"scalar":           `"payload"`,
		"trailing data":    `{} {}`,
		"duplicate field":  `{"action":"opened","action":"closed"}`,
		"numeric action":   `{"action":1}`,
		"uppercase action": `{"action":"Opened"}`,
		"spaced action":    `{"action":" opened "}`,
		"long action":      `{"action":"` + strings.Repeat("a", maxGitHubActionBytes+1) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryInserter()
			controller, _ := newGitHubTestController(t, store, 1<<20)
			response := performRequest(
				controller,
				githubSignedRequest(
					githubTestSecret,
					"pull_request",
					"malformed-"+strings.ReplaceAll(name, " ", "-"),
					body,
				),
			)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Empty(t, store.recordedInputs())
		})
	}
}

func TestGitHubDeduplicationKeepsFirstDelivery(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	controller, _ := newGitHubTestController(t, store, 1<<20)
	firstBody := `{"action":"opened","version":1}`
	secondBody := `{"action":"closed","version":2}`

	first := performRequest(
		controller,
		githubSignedRequest(
			githubTestSecret,
			"pull_request",
			"Same-Case-Sensitive-Delivery",
			firstBody,
		),
	)
	second := performRequest(
		controller,
		githubSignedRequest(
			githubTestSecret,
			"issues",
			"Same-Case-Sensitive-Delivery",
			secondBody,
		),
	)
	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusOK, second.Code)

	var firstOutput, secondOutput admissionResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstOutput))
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondOutput))
	assert.True(t, firstOutput.Inserted)
	assert.False(t, secondOutput.Inserted)
	assert.Equal(t, firstOutput.EventID, secondOutput.EventID)
	inputs := store.recordedInputs()
	require.Len(t, inputs, 1)
	assert.Equal(t, "pull_request.opened", inputs[0].Type)
	assert.JSONEq(t, firstBody, string(inputs[0].Payload))
}

func TestGitHubPreservesAdmissionStatusMapping(t *testing.T) {
	t.Parallel()
	t.Run("payload too large", func(t *testing.T) {
		t.Parallel()
		store := newMemoryInserter()
		store.maxPayload = 2
		controller, _ := newGitHubTestController(t, store, 1024)
		response := performRequest(
			controller,
			githubSignedRequest(
				githubTestSecret,
				"push",
				"payload-too-large",
				`{"large":true}`,
			),
		)
		assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	})

	t.Run("store failure is retryable and opaque", func(t *testing.T) {
		t.Parallel()
		store := newMemoryInserter()
		store.insertErr = errors.New("database leaked " + githubTestSecret)
		controller, _ := newGitHubTestController(t, store, 1024)
		response := performRequest(
			controller,
			githubSignedRequest(
				githubTestSecret,
				"push",
				"store-failure",
				`{"safe":true}`,
			),
		)
		assert.Equal(t, http.StatusServiceUnavailable, response.Code)
		assert.Equal(t, "1", response.Header().Get("Retry-After"))
		assert.NotContains(t, response.Body.String(), githubTestSecret)
		assert.NotContains(t, response.Body.String(), "database")
	})

	t.Run("total request limit", func(t *testing.T) {
		t.Parallel()
		controller, _ := newGitHubTestController(t, newMemoryInserter(), 1)
		body := strings.Repeat("x", RequestMetadataAllowanceBytes+2)
		response := performRequest(
			controller,
			githubSignedRequest(
				githubTestSecret,
				"push",
				"total-request-limit",
				body,
			),
		)
		assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	})

	t.Run("raw body cannot use Standard metadata allowance", func(t *testing.T) {
		t.Parallel()
		store := newMemoryInserter()
		controller, _ := newGitHubTestController(t, store, len(`{}`))
		body := " {}"
		response := performRequest(
			controller,
			githubSignedRequest(
				githubTestSecret,
				"push",
				"github-no-metadata-allowance",
				body,
			),
		)
		assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
		assert.Empty(t, store.recordedInputs())
	})
}

func TestGitHubRejectsSecretsInDurableIdentity(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("s", minimumGitHubSecretBytes)
	tests := []struct {
		name     string
		event    string
		delivery string
		body     string
	}{
		{
			name:     "delivery",
			event:    "push",
			delivery: "prefix-" + secret,
			body:     `{}`,
		},
		{
			name:     "event",
			event:    "push" + secret,
			delivery: "safe-event-delivery",
			body:     `{}`,
		},
		{
			name:     "action",
			event:    "pull_request",
			delivery: "safe-action-delivery",
			body:     `{"action":"` + secret + `"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryInserter()
			backend, err := NewBackend(BackendConfig{
				Store: store,
				ConnectorSecrets: map[string]string{
					githubTestConnector: secret,
				},
				ConnectorFormats: map[string]string{
					githubTestConnector: "github",
				},
				MaxPayloadBytes: 1 << 20,
			})
			require.NoError(t, err)
			controller := NewController()
			generation, err := controller.Activate(backend)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(
					t,
					controller.Deactivate(context.Background(), generation),
				)
			})

			response := performRequest(
				controller,
				githubSignedRequest(
					secret,
					test.event,
					test.delivery,
					test.body,
				),
			)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.NotContains(t, response.Body.String(), secret)
			assert.Empty(t, store.recordedInputs())
		})
	}
}
