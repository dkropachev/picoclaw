package webhook

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const testConnector = "build-system"

var testSecret = "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))

func connectorNameSafeTestSecret(fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 33))
}

type memoryInserter struct {
	mu         sync.Mutex
	byDedupe   map[string]eventing.StoredEvent
	inputs     []eventing.Envelope
	insertErr  error
	maxPayload int
	nextID     uint64
}

func newMemoryInserter() *memoryInserter {
	return &memoryInserter{byDedupe: make(map[string]eventing.StoredEvent)}
}

func (store *memoryInserter) Insert(
	_ context.Context,
	input eventing.Envelope,
) (eventing.InsertResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.insertErr != nil {
		return eventing.InsertResult{}, store.insertErr
	}
	if store.maxPayload > 0 && len(input.Payload) > store.maxPayload {
		return eventing.InsertResult{}, eventing.ErrPayloadTooLarge
	}
	key := input.Source + "\x00" + input.Connector + "\x00" + input.DedupeKey
	if stored, exists := store.byDedupe[key]; exists {
		return eventing.InsertResult{Event: stored, Inserted: false}, nil
	}
	store.nextID++
	input.ID = fmt.Sprintf("ev_%032x", store.nextID)
	normalized, err := eventing.NormalizeEnvelope(input, time.Now())
	if err != nil {
		return eventing.InsertResult{}, err
	}
	stored := eventing.StoredEvent{Envelope: normalized}
	store.byDedupe[key] = stored
	store.inputs = append(store.inputs, normalized.Clone())
	return eventing.InsertResult{Event: stored, Inserted: true}, nil
}

func (store *memoryInserter) recordedInputs() []eventing.Envelope {
	store.mu.Lock()
	defer store.mu.Unlock()
	inputs := make([]eventing.Envelope, len(store.inputs))
	for index := range store.inputs {
		inputs[index] = store.inputs[index].Clone()
	}
	return inputs
}

func newTestController(
	t *testing.T,
	store Inserter,
	maxPayload int,
) (*Controller, Generation) {
	t.Helper()
	backend, err := NewBackend(BackendConfig{
		Store:            store,
		ConnectorSecrets: map[string]string{testConnector: testSecret},
		MaxPayloadBytes:  maxPayload,
	})
	require.NoError(t, err)
	require.Equal(t, 1, backend.ConnectorCount())
	controller := NewController()
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, controller.Deactivate(context.Background(), generation))
	})
	return controller, generation
}

func signedRequest(
	t *testing.T,
	secret string,
	webhookID string,
	body string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		RoutePrefix+testConnector,
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	verifier, err := standardwebhooks.NewWebhook(secret)
	require.NoError(t, err)
	timestamp := time.Now()
	signature, err := verifier.Sign(webhookID, timestamp, []byte(body))
	require.NoError(t, err)
	request.Header.Set(standardwebhooks.HeaderWebhookID, webhookID)
	request.Header.Set(standardwebhooks.HeaderWebhookTimestamp, strconv.FormatInt(timestamp.Unix(), 10))
	request.Header.Set(standardwebhooks.HeaderWebhookSignature, signature)
	return request
}

func performRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestNewBackendValidation(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	valid := BackendConfig{
		Store:            store,
		ConnectorSecrets: map[string]string{testConnector: testSecret},
		MaxPayloadBytes:  1024,
	}
	tests := []struct {
		name   string
		mutate func(*BackendConfig)
	}{
		{name: "missing store", mutate: func(config *BackendConfig) { config.Store = nil }},
		{name: "zero maximum", mutate: func(config *BackendConfig) { config.MaxPayloadBytes = 0 }},
		{name: "missing connectors", mutate: func(config *BackendConfig) {
			config.ConnectorSecrets = nil
		}},
		{name: "invalid name", mutate: func(config *BackendConfig) {
			config.ConnectorSecrets = map[string]string{"../escape": testSecret}
		}},
		{name: "case collision", mutate: func(config *BackendConfig) {
			config.ConnectorSecrets = map[string]string{
				"Example": testSecret,
				"example": testSecret,
			}
		}},
		{name: "missing secret prefix", mutate: func(config *BackendConfig) {
			config.ConnectorSecrets = map[string]string{testConnector: strings.TrimPrefix(testSecret, "whsec_")}
		}},
		{name: "short secret", mutate: func(config *BackendConfig) {
			config.ConnectorSecrets = map[string]string{
				testConnector: "whsec_" + base64.StdEncoding.EncodeToString([]byte("short")),
			}
		}},
		{name: "invalid base64 secret", mutate: func(config *BackendConfig) {
			config.ConnectorSecrets = map[string]string{testConnector: "whsec_not-base64!"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := valid
			test.mutate(&config)
			_, err := NewBackend(config)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), testSecret)
		})
	}

	backend, err := NewBackend(valid)
	require.NoError(t, err)
	assert.Equal(t, 1, backend.ConnectorCount())
	assert.Equal(t, 0, (*Backend)(nil).ConnectorCount())
}

func TestNewBackendRejectsSecretBearingConnectorIdentity(t *testing.T) {
	t.Parallel()
	firstSecret := connectorNameSafeTestSecret(0x41)
	secondSecret := connectorNameSafeTestSecret(0x42)
	tests := []struct {
		name       string
		connectors map[string]string
	}{
		{
			name: "own secret",
			connectors: map[string]string{
				"prefix-" + firstSecret + "-suffix": firstSecret,
			},
		},
		{
			name: "other connector secret",
			connectors: map[string]string{
				"owner":                             firstSecret,
				"prefix-" + firstSecret + "-suffix": secondSecret,
			},
		},
		{
			name: "other connector secret before invalid own secret",
			connectors: map[string]string{
				"owner":                             firstSecret,
				"prefix-" + firstSecret + "-suffix": "invalid-own-secret",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryInserter()
			backend, err := NewBackend(BackendConfig{
				Store:            store,
				ConnectorSecrets: test.connectors,
				MaxPayloadBytes:  1024,
			})
			require.Nil(t, backend)
			require.EqualError(t, err, connectorIdentitySecretConflictMessage)
			for connector, secret := range test.connectors {
				assert.NotContains(t, err.Error(), connector)
				assert.NotContains(t, err.Error(), secret)
			}
			assert.Empty(t, store.recordedInputs())
		})
	}
}

func TestControllerProtocolStatuses(t *testing.T) {
	t.Parallel()
	validBody := `{"type":"deploy.completed","payload":{}}`

	t.Run("inactive is retryable", func(t *testing.T) {
		t.Parallel()
		response := performRequest(
			NewController(),
			signedRequest(t, testSecret, "delivery-inactive", validBody),
		)
		assert.Equal(t, http.StatusServiceUnavailable, response.Code)
		assert.Equal(t, "1", response.Header().Get("Retry-After"))
	})

	t.Run("invalid route is not found", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-route", validBody)
		request.URL.Path = RoutePrefix + "nested/path"
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusNotFound, response.Code)
	})

	t.Run("method is rejected", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-method", validBody)
		request.Method = http.MethodPut
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusMethodNotAllowed, response.Code)
		assert.Equal(t, http.MethodPost, response.Header().Get("Allow"))
	})

	t.Run("unknown connector is not found", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-unknown", validBody)
		request.URL.Path = RoutePrefix + "disabled"
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusNotFound, response.Code)
	})

	t.Run("missing auth is uniform and body is unread", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		body := &observedBody{reader: strings.NewReader(validBody)}
		request := httptest.NewRequest(http.MethodPost, RoutePrefix+testConnector, nil)
		request.Body = body
		request.Header.Set("Content-Type", "application/json")
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
		assert.Zero(t, body.reads.Load())
	})

	t.Run("duplicate auth header is rejected before body read", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-duplicate-header", validBody)
		body := &observedBody{reader: strings.NewReader(validBody)}
		request.Body = body
		request.Header.Add(standardwebhooks.HeaderWebhookID, "second-id")
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
		assert.Zero(t, body.reads.Load())
	})

	t.Run("invalid media type", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-media", validBody)
		request.Header.Set("Content-Type", "text/plain")
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnsupportedMediaType, response.Code)
	})

	t.Run("unsupported content encoding", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-encoding", validBody)
		request.Header.Set("Content-Encoding", "gzip")
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnsupportedMediaType, response.Code)
	})

	t.Run("invalid signature", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-signature", validBody)
		request.Header.Set(standardwebhooks.HeaderWebhookSignature, "v1,bad")
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("stale timestamp", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-time", validBody)
		verifier, err := standardwebhooks.NewWebhook(testSecret)
		require.NoError(t, err)
		timestamp := time.Now().Add(-10 * time.Minute)
		signature, err := verifier.Sign("delivery-time", timestamp, []byte(validBody))
		require.NoError(t, err)
		request.Header.Set(
			standardwebhooks.HeaderWebhookTimestamp,
			strconv.FormatInt(timestamp.Unix(), 10),
		)
		request.Header.Set(standardwebhooks.HeaderWebhookSignature, signature)
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("future timestamp", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1024)
		request := signedRequest(t, testSecret, "delivery-future-time", validBody)
		verifier, err := standardwebhooks.NewWebhook(testSecret)
		require.NoError(t, err)
		timestamp := time.Now().Add(10 * time.Minute)
		signature, err := verifier.Sign("delivery-future-time", timestamp, []byte(validBody))
		require.NoError(t, err)
		request.Header.Set(
			standardwebhooks.HeaderWebhookTimestamp,
			strconv.FormatInt(timestamp.Unix(), 10),
		)
		request.Header.Set(standardwebhooks.HeaderWebhookSignature, signature)
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code)
	})

	t.Run("request body exceeds total limit", func(t *testing.T) {
		t.Parallel()
		controller, _ := newTestController(t, newMemoryInserter(), 1)
		body := strings.Repeat("x", RequestMetadataAllowanceBytes+2)
		request := signedRequest(t, testSecret, "delivery-total-limit", body)
		response := performRequest(controller, request)
		assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	})

	t.Run("payload exceeds store limit", func(t *testing.T) {
		t.Parallel()
		store := newMemoryInserter()
		store.maxPayload = 2
		controller, _ := newTestController(t, store, 1024)
		body := `{"type":"large","payload":{"more":"than two"}}`
		response := performRequest(
			controller,
			signedRequest(t, testSecret, "delivery-payload-limit", body),
		)
		assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	})
}

func TestControllerRejectsEncodedPathAliasesOverHTTP(t *testing.T) {
	t.Parallel()
	body := `{"type":"deploy.completed","payload":{}}`
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "canonical path",
			path:       RoutePrefix + testConnector,
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "encoded prefix character",
			path:       "/webhooks/%65vents/" + testConnector,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "encoded route slashes",
			path:       "/webhooks%2Fevents%2F" + testConnector,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "encoded connector character",
			path:       RoutePrefix + "%62uild-system",
			wantStatus: http.StatusNotFound,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryInserter()
			controller, _ := newTestController(t, store, 1024)
			server := httptest.NewServer(controller)
			t.Cleanup(server.Close)
			verifier, err := standardwebhooks.NewWebhook(testSecret)
			require.NoError(t, err)

			webhookID := fmt.Sprintf("delivery-real-http-%d", index)
			timestamp := time.Now()
			signature, signErr := verifier.Sign(webhookID, timestamp, []byte(body))
			require.NoError(t, signErr)
			request, requestErr := http.NewRequest(
				http.MethodPost,
				server.URL+test.path,
				strings.NewReader(body),
			)
			require.NoError(t, requestErr)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(standardwebhooks.HeaderWebhookID, webhookID)
			request.Header.Set(
				standardwebhooks.HeaderWebhookTimestamp,
				strconv.FormatInt(timestamp.Unix(), 10),
			)
			request.Header.Set(standardwebhooks.HeaderWebhookSignature, signature)

			response, doErr := server.Client().Do(request)
			require.NoError(t, doErr)
			responseBody, readErr := io.ReadAll(response.Body)
			require.NoError(t, readErr)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, test.wantStatus, response.StatusCode, string(responseBody))
			if test.wantStatus == http.StatusNotFound {
				assert.JSONEq(t, `{"error":"Not Found"}`, string(responseBody))
				assert.Empty(t, store.recordedInputs())
			} else {
				assert.Len(t, store.recordedInputs(), 1)
			}
		})
	}
}

type observedBody struct {
	reader io.Reader
	reads  atomic.Int64
}

func (body *observedBody) Read(buffer []byte) (int, error) {
	body.reads.Add(1)
	return body.reader.Read(buffer)
}

func (*observedBody) Close() error {
	return nil
}

func TestControllerStrictEnvelopeAndExactNumbers(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	controller, _ := newTestController(t, store, 1<<20)
	validBody := `{
		"type":"deploy.completed",
		"occurred_at":"2026-07-28T12:00:00.123456789-04:00",
		"actor":{"id":"bot-1","type":"service","display_name":"Builder","attributes":{"team":"ops"}},
		"subject":{"id":"release-1","type":"release","name":"v1","url":"https://example.invalid","attributes":{"env":"prod"}},
		"attributes":{"priority":"high"},
		"payload":{"large":9007199254740993123456789,"exponent":1e400}
	}`
	response := performRequest(
		controller,
		signedRequest(t, testSecret, "delivery-exact", validBody),
	)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())

	inputs := store.recordedInputs()
	require.Len(t, inputs, 1)
	input := inputs[0]
	assert.Equal(t, "webhook", input.Source)
	assert.Equal(t, testConnector, input.Connector)
	assert.Equal(t, "deploy.completed", input.Type)
	assert.Equal(t, "delivery-exact", input.DedupeKey)
	assert.Equal(t, "bot-1", input.Actor.ID)
	assert.Equal(t, "release-1", input.Subject.ID)
	assert.Equal(t, map[string]string{"priority": "high"}, input.Attributes)
	assert.Equal(
		t,
		`{"large":9007199254740993123456789,"exponent":1e400}`,
		string(input.Payload),
	)
	assert.Contains(t, string(input.Payload), "9007199254740993123456789")
	assert.Contains(t, string(input.Payload), "1e400")
	require.NotNil(t, input.OccurredAt)
	assert.Equal(t, 123456789, input.OccurredAt.Nanosecond())
	assert.Empty(t, input.ReplayOf)
}

func TestControllerRejectsNonCanonicalEnvelope(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"not object":               `[]`,
		"missing type":             `{"payload":{}}`,
		"null type":                `{"type":null,"payload":{}}`,
		"missing payload":          `{"type":"x"}`,
		"payload array":            `{"type":"x","payload":[]}`,
		"unknown server field":     `{"type":"x","id":"client","payload":{}}`,
		"duplicate top field":      `{"type":"x","type":"y","payload":{}}`,
		"invalid occurred at":      `{"type":"x","occurred_at":"today","payload":{}}`,
		"null actor":               `{"type":"x","actor":null,"payload":{}}`,
		"actor unknown field":      `{"type":"x","actor":{"login":"bot"},"payload":{}}`,
		"actor duplicate field":    `{"type":"x","actor":{"id":"a","id":"b"},"payload":{}}`,
		"subject wrong shape":      `{"type":"x","subject":[],"payload":{}}`,
		"attributes wrong value":   `{"type":"x","attributes":{"number":1},"payload":{}}`,
		"attributes duplicate key": `{"type":"x","attributes":{"key":"a","key":"b"},"payload":{}}`,
		"trailing JSON":            `{"type":"x","payload":{}} {}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			controller, _ := newTestController(t, newMemoryInserter(), 1<<20)
			response := performRequest(
				controller,
				signedRequest(t, testSecret, "delivery-"+strings.ReplaceAll(name, " ", "-"), body),
			)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}

func TestControllerDeduplicationKeepsFirstPayload(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	controller, _ := newTestController(t, store, 1<<20)
	firstBody := `{"type":"example","payload":{"version":1}}`
	secondBody := `{"type":"example","payload":{"version":2}}`

	first := performRequest(
		controller,
		signedRequest(t, testSecret, "delivery-dedupe", firstBody),
	)
	second := performRequest(
		controller,
		signedRequest(t, testSecret, "delivery-dedupe", secondBody),
	)
	require.Equal(t, http.StatusAccepted, first.Code)
	require.Equal(t, http.StatusOK, second.Code)

	var firstResponse, secondResponse admissionResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstResponse))
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondResponse))
	assert.True(t, firstResponse.Inserted)
	assert.False(t, secondResponse.Inserted)
	assert.Equal(t, firstResponse.EventID, secondResponse.EventID)
	inputs := store.recordedInputs()
	require.Len(t, inputs, 1)
	assert.JSONEq(t, `{"version":1}`, string(inputs[0].Payload))
}

func TestControllerConcurrentDuplicateCreatesOneEvent(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	controller, _ := newTestController(t, store, 1<<20)
	const requestCount = 24
	statuses := make(chan int, requestCount)
	eventIDs := make(chan string, requestCount)
	var wait sync.WaitGroup
	for range requestCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := performRequest(
				controller,
				signedRequest(t, testSecret, "delivery-concurrent", `{"type":"race","payload":{"ok":true}}`),
			)
			statuses <- response.Code
			var output admissionResponse
			if json.Unmarshal(response.Body.Bytes(), &output) == nil {
				eventIDs <- output.EventID
			}
		}()
	}
	wait.Wait()
	close(statuses)
	close(eventIDs)

	accepted := 0
	ok := 0
	for status := range statuses {
		switch status {
		case http.StatusAccepted:
			accepted++
		case http.StatusOK:
			ok++
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, requestCount-1, ok)
	var eventID string
	for current := range eventIDs {
		if eventID == "" {
			eventID = current
		}
		assert.Equal(t, eventID, current)
	}
	assert.Len(t, store.recordedInputs(), 1)
}

func TestControllerStoreFailureIsRetryableAndOpaque(t *testing.T) {
	t.Parallel()
	store := newMemoryInserter()
	store.insertErr = errors.New(
		"database rejected secret " + testSecret + ` and body {"password":"visible"}`,
	)
	controller, _ := newTestController(t, store, 1<<20)
	body := `{"type":"failure","payload":{"password":"visible"}}`
	response := performRequest(
		controller,
		signedRequest(t, testSecret, "delivery-failure", body),
	)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Equal(t, "1", response.Header().Get("Retry-After"))
	assert.NotContains(t, response.Body.String(), testSecret)
	assert.NotContains(t, response.Body.String(), "visible")
	assert.NotContains(t, response.Body.String(), "database")
}

func TestControllerRejectsConfiguredSecretsInDurableIdentity(t *testing.T) {
	store := newMemoryInserter()
	secondarySecret := "whsec_" +
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x6b}, 32))
	backend, err := NewBackend(BackendConfig{
		Store: store,
		ConnectorSecrets: map[string]string{
			testConnector: testSecret,
			"secondary":   secondarySecret,
		},
		MaxPayloadBytes: 1 << 20,
	})
	require.NoError(t, err)
	controller := NewController()
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, controller.Deactivate(context.Background(), generation))
	})

	tests := []struct {
		name      string
		webhookID string
		eventType string
	}{
		{
			name:      "selected connector secret in webhook ID",
			webhookID: "delivery-" + testSecret + "-suffix",
			eventType: "deploy.completed",
		},
		{
			name:      "other enabled connector secret in webhook ID",
			webhookID: "delivery-" + secondarySecret + "-suffix",
			eventType: "deploy.completed",
		},
		{
			name:      "selected connector secret in type",
			webhookID: "delivery-selected-type",
			eventType: "deploy." + testSecret + ".completed",
		},
		{
			name:      "other enabled connector secret in type",
			webhookID: "delivery-secondary-type",
			eventType: "deploy." + secondarySecret + ".completed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bodyBytes, marshalErr := json.Marshal(map[string]any{
				"type":    test.eventType,
				"payload": map[string]any{"safe": true},
			})
			require.NoError(t, marshalErr)
			response := performRequest(
				controller,
				signedRequest(t, testSecret, test.webhookID, string(bodyBytes)),
			)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.NotContains(t, response.Body.String(), testSecret)
			assert.NotContains(t, response.Body.String(), secondarySecret)
		})
	}
	assert.Empty(t, store.recordedInputs())
}

type blockingInserter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (store *blockingInserter) Insert(
	ctx context.Context,
	input eventing.Envelope,
) (eventing.InsertResult, error) {
	store.once.Do(func() { close(store.entered) })
	select {
	case <-store.release:
	case <-ctx.Done():
		return eventing.InsertResult{}, ctx.Err()
	}
	input.ID = "ev_00000000000000000000000000000001"
	normalized, err := eventing.NormalizeEnvelope(input, time.Now())
	if err != nil {
		return eventing.InsertResult{}, err
	}
	return eventing.InsertResult{
		Event:    eventing.StoredEvent{Envelope: normalized},
		Inserted: true,
	}, nil
}

func TestControllerGenerationDrainAndStaleCleanup(t *testing.T) {
	t.Parallel()
	store := &blockingInserter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	backend, err := NewBackend(BackendConfig{
		Store:            store,
		ConnectorSecrets: map[string]string{testConnector: testSecret},
		MaxPayloadBytes:  1024,
	})
	require.NoError(t, err)
	controller := NewController()
	firstGeneration, err := controller.Activate(backend)
	require.NoError(t, err)

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseDone <- performRequest(
			controller,
			signedRequest(t, testSecret, "delivery-blocked", `{"type":"blocked","payload":{}}`),
		)
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("insert was not admitted")
	}

	timeoutContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, controller.Deactivate(timeoutContext, firstGeneration), context.DeadlineExceeded)
	assert.False(t, controller.IsActive(firstGeneration))

	retry := performRequest(
		controller,
		signedRequest(t, testSecret, "delivery-retry", `{"type":"retry","payload":{}}`),
	)
	assert.Equal(t, http.StatusServiceUnavailable, retry.Code)
	assert.Equal(t, "1", retry.Header().Get("Retry-After"))
	_, err = controller.Activate(backend)
	assert.ErrorIs(t, err, ErrGenerationDraining)

	close(store.release)
	response := <-responseDone
	assert.Equal(t, http.StatusAccepted, response.Code)
	require.NoError(t, controller.Deactivate(context.Background(), firstGeneration))

	secondGeneration, err := controller.Activate(backend)
	require.NoError(t, err)
	assert.True(t, controller.IsActive(secondGeneration))
	require.NoError(t, controller.Deactivate(context.Background(), firstGeneration))
	assert.True(t, controller.IsActive(secondGeneration))
	require.NoError(t, controller.Deactivate(context.Background(), secondGeneration))
}

func TestControllerGenerationOwnershipAndZeroValue(t *testing.T) {
	t.Parallel()
	firstController := NewController()
	secondController := NewController()
	require.NoError(t, firstController.Deactivate(context.Background(), Generation{}))

	backend, err := NewBackend(BackendConfig{
		Store:            newMemoryInserter(),
		ConnectorSecrets: map[string]string{testConnector: testSecret},
		MaxPayloadBytes:  1024,
	})
	require.NoError(t, err)
	generation, err := firstController.Activate(backend)
	require.NoError(t, err)
	_, err = firstController.Activate(backend)
	assert.ErrorIs(t, err, ErrActiveGeneration)
	assert.ErrorIs(
		t,
		secondController.Deactivate(context.Background(), generation),
		ErrGenerationNotOwned,
	)
	require.NoError(t, firstController.Deactivate(context.Background(), generation))
}
