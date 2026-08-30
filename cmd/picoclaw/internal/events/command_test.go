package events

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/localgateway"
)

func executeEventsCommand(
	t *testing.T,
	client *gatewayClient,
	args ...string,
) (string, error) {
	t.Helper()
	command := newEventsCommand(client)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}

func TestEventsCommandShape(t *testing.T) {
	t.Parallel()

	command := newEventsCommand(staticGateway(http.StatusOK, `{}`, nil))
	assert.Equal(t, "events", command.Name())
	assert.Contains(t, command.Aliases, "event")
	assert.Contains(t, command.Long, "never open the event database directly")

	names := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		names = append(names, child.Name())
	}
	assert.ElementsMatch(t, []string{"dispatches", "get", "list", "payload", "replay"}, names)

	replay, _, err := command.Find([]string{"replay"})
	require.NoError(t, err)
	assert.Contains(t, replay.Long, "may repeat workflow or external side effects")
	require.NotNil(t, replay.Flags().Lookup("yes"))
}

func TestEventsListCommandMapsEveryFilter(t *testing.T) {
	t.Parallel()

	calls := 0
	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		calls++
		assert.Equal(t, "/runtime/eventing/events", request.Path)
		assert.Equal(t, "github", request.Query.Get("source"))
		assert.Equal(t, "primary", request.Query.Get("connector"))
		assert.Equal(t, "issues.opened", request.Query.Get("type"))
		assert.Equal(t, "pending", request.Query.Get("routing_status"))
		assert.Equal(t, "25", request.Query.Get("limit"))
		assert.Equal(t, "cursor-token", request.Query.Get("cursor"))
		assert.Equal(
			t,
			"connector=primary&cursor=cursor-token&limit=25&routing_status=pending&source=github&type=issues.opened",
			request.Query.Encode(),
		)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"events":[],"next_cursor":"next"}`),
		}, nil
	}))

	output, err := executeEventsCommand(
		t,
		client,
		"list",
		"--source", "github",
		"--connector", "primary",
		"--type", "issues.opened",
		"--routing-status", "pending",
		"--limit", "25",
		"--cursor", "cursor-token",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.JSONEq(t, `{"events":[],"next_cursor":"next"}`, output)
}

func TestEventsListOmitsUnsetQueryValues(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.Empty(t, request.Query)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"events":[]}`),
		}, nil
	}))
	_, err := executeEventsCommand(t, client, "list")
	require.NoError(t, err)
}

func TestEventsListPreservesGatewayBadRequestTextForOversizedQuery(t *testing.T) {
	t.Parallel()

	client := staticGateway(0, "", localgateway.ErrQueryTooLarge)
	_, err := executeEventsCommand(
		t,
		client,
		"list",
		"--source", strings.Repeat("x", 64<<10),
	)
	require.EqualError(t, err, "gateway rejected the event request (400 Bad Request)")
}

func TestEventsGetCommandUsesCanonicalEventPath(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.Equal(t, "/runtime/eventing/events/"+testEventID, request.Path)
		assert.Equal(t, http.MethodGet, request.Method)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"id":"` + testEventID + `","payload_bytes":42}`),
		}, nil
	}))
	output, err := executeEventsCommand(t, client, "get", testEventID)
	require.NoError(t, err)
	assert.Contains(t, output, `"payload_bytes": 42`)
}

func TestEventsGetRejectsInvalidIDBeforeRequest(t *testing.T) {
	t.Parallel()

	called := false
	client := testGatewayClient(jsonGatewayFunc(func(
		context.Context,
		localgateway.Request,
	) (localgateway.Response, error) {
		called = true
		return localgateway.Response{}, nil
	}))
	_, err := executeEventsCommand(t, client, "get", "../events")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32 lowercase hexadecimal")
	assert.False(t, called)
}

func TestEventsPayloadCommandWritesExactBytes(t *testing.T) {
	t.Parallel()

	const payload = " \n{\"large\":9007199254740993,\"tiny\":1e-1000}\t"
	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.Equal(t, "/runtime/eventing/events/"+testEventID+"/payload", request.Path)
		assert.Equal(t, http.MethodGet, request.Method)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(payload),
		}, nil
	}))
	output, err := executeEventsCommand(t, client, "payload", testEventID)
	require.NoError(t, err)
	assert.Equal(t, payload, output)
}

func TestEventsPayloadRejectsInvalidIDBeforeRequest(t *testing.T) {
	t.Parallel()

	called := false
	client := testGatewayClient(jsonGatewayFunc(func(
		context.Context,
		localgateway.Request,
	) (localgateway.Response, error) {
		called = true
		return localgateway.Response{}, nil
	}))
	_, err := executeEventsCommand(t, client, "payload", "ev_bad")
	require.Error(t, err)
	assert.False(t, called)
}

func TestEventsDispatchesCommandMapsEveryFilter(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.Equal(t, "/runtime/eventing/dispatches", request.Path)
		assert.Equal(t, testEventID, request.Query.Get("event_id"))
		assert.Equal(t, "github-issue-triage.yml", request.Query.Get("workflow_ref"))
		assert.Equal(t, "failed", request.Query.Get("status"))
		assert.Equal(t, "10", request.Query.Get("limit"))
		assert.Equal(t, "dispatch-cursor", request.Query.Get("cursor"))
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"dispatches":[]}`),
		}, nil
	}))
	_, err := executeEventsCommand(
		t,
		client,
		"dispatches",
		"--event-id", testEventID,
		"--workflow", "github-issue-triage.yml",
		"--status", "failed",
		"--limit", "10",
		"--cursor", "dispatch-cursor",
	)
	require.NoError(t, err)
}

func TestEventsDispatchesRejectsInvalidEventIDBeforeRequest(t *testing.T) {
	t.Parallel()

	called := false
	client := testGatewayClient(jsonGatewayFunc(func(
		context.Context,
		localgateway.Request,
	) (localgateway.Response, error) {
		called = true
		return localgateway.Response{}, nil
	}))
	_, err := executeEventsCommand(t, client, "dispatches", "--event-id", "ev_bad")
	require.Error(t, err)
	assert.False(t, called)
}

func TestEventsReplayRequiresConfirmationBeforeTransport(t *testing.T) {
	t.Parallel()

	called := false
	client := testGatewayClient(jsonGatewayFunc(func(
		context.Context,
		localgateway.Request,
	) (localgateway.Response, error) {
		called = true
		return localgateway.Response{}, nil
	}))
	_, err := executeEventsCommand(t, client, "replay", testEventID)
	require.EqualError(t, err, "replay requires --yes because effects may repeat")
	assert.False(t, called)
}

func TestEventsReplayConfirmedMakesOneRequest(t *testing.T) {
	t.Parallel()

	calls := 0
	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		calls++
		assert.Equal(t, []byte("{}"), request.Body)
		return localgateway.Response{
			StatusCode: http.StatusCreated,
			Body: []byte(
				`{"event":{"id":"ev_abcdefabcdefabcdefabcdefabcdefab"}}`,
			),
		}, nil
	}))
	output, err := executeEventsCommand(t, client, "replay", testEventID, "--yes")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, output, "ev_abcdefabcdefabcdefabcdefabcdefab")
}

func TestEventsCommandSurfacesUnavailablePIDCleanly(t *testing.T) {
	t.Parallel()

	client := staticGateway(0, "", localgateway.ErrGatewayUnavailable)
	_, err := executeEventsCommand(t, client, "list")
	require.EqualError(t, err, "live gateway is unavailable; start picoclaw gateway and retry")
}

func TestEventsCommandPrettyPrintsWithoutInterpretingContent(t *testing.T) {
	t.Parallel()

	client := staticGateway(
		http.StatusOK,
		`{"events":[{"payload_bytes":9007199254740993,"name":"\u001b[31m"}]}`,
		nil,
	)
	output, err := executeEventsCommand(t, client, "list")
	require.NoError(t, err)
	assert.Contains(t, output, "9007199254740993")
	assert.Contains(t, output, `\u001b[31m`)
	assert.False(t, strings.ContainsRune(output, '\x1b'))
}
