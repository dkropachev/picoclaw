//go:build !mipsle && !netbsd && !(freebsd && arm)

package webhook

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestConfiguredSecretIdentityNeverReachesDurableInbox(t *testing.T) {
	t.Parallel()
	secondarySecret := "whsec_" +
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x7c}, 32))
	store, err := eventing.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "events.db"),
		eventing.WithRedaction(nil, []string{testSecret, secondarySecret}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

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

	requestBodies := []struct {
		id        string
		eventType string
	}{
		{
			id:        "durable-" + secondarySecret,
			eventType: "deploy.completed",
		},
		{
			id:        "durable-type",
			eventType: "deploy." + secondarySecret,
		},
	}
	for _, requestBody := range requestBodies {
		body, marshalErr := json.Marshal(map[string]any{
			"type":    requestBody.eventType,
			"payload": map[string]any{},
		})
		require.NoError(t, marshalErr)
		response := performRequest(
			controller,
			signedRequest(t, testSecret, requestBody.id, string(body)),
		)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.NotContains(t, response.Body.String(), secondarySecret)
	}

	page, err := store.List(context.Background(), eventing.EventFilter{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, page.Events)
}

func TestCrossConnectorSecretIdentityNeverReachesDurableInbox(t *testing.T) {
	t.Parallel()
	firstSecret := connectorNameSafeTestSecret(0x43)
	secondSecret := connectorNameSafeTestSecret(0x44)
	databaseDir := t.TempDir()
	store, err := eventing.Open(
		context.Background(),
		filepath.Join(databaseDir, "events.db"),
		eventing.WithRedaction(nil, []string{firstSecret, secondSecret}),
	)
	require.NoError(t, err)
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			require.NoError(t, store.Close())
		}
	})

	credentialBearingName := "prefix-" + firstSecret + "-suffix"
	backend, err := NewBackend(BackendConfig{
		Store: store,
		ConnectorSecrets: map[string]string{
			"owner":               firstSecret,
			credentialBearingName: secondSecret,
		},
		MaxPayloadBytes: 1 << 20,
	})
	require.Nil(t, backend)
	require.EqualError(t, err, connectorIdentitySecretConflictMessage)
	assert.NotContains(t, err.Error(), credentialBearingName)
	assert.NotContains(t, err.Error(), firstSecret)
	assert.NotContains(t, err.Error(), secondSecret)

	page, err := store.List(context.Background(), eventing.EventFilter{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, page.Events)
	require.NoError(t, store.Close())
	storeClosed = true

	entries, err := os.ReadDir(databaseDir)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(databaseDir, entry.Name()))
		require.NoError(t, readErr)
		if bytes.Contains(data, []byte(firstSecret)) ||
			bytes.Contains(data, []byte(secondSecret)) {
			t.Fatal("durable event store contains a configured webhook signing credential")
		}
	}
}
