package events

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ppid "github.com/sipeed/picoclaw/pkg/pid"
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

	command := newEventsCommand(testGatewayClient(httpDoerFunc(
		func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{}`), nil
		},
	)))
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
	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "/runtime/eventing/events", request.URL.Path)
		assert.Equal(t, "github", request.URL.Query().Get("source"))
		assert.Equal(t, "primary", request.URL.Query().Get("connector"))
		assert.Equal(t, "issues.opened", request.URL.Query().Get("type"))
		assert.Equal(t, "pending", request.URL.Query().Get("routing_status"))
		assert.Equal(t, "25", request.URL.Query().Get("limit"))
		assert.Equal(t, "cursor-token", request.URL.Query().Get("cursor"))
		assert.Equal(
			t,
			"connector=primary&cursor=cursor-token&limit=25&routing_status=pending&source=github&type=issues.opened",
			request.URL.RawQuery,
		)
		return jsonResponse(http.StatusOK, `{"events":[],"next_cursor":"next"}`), nil
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

	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Empty(t, request.URL.RawQuery)
		return jsonResponse(http.StatusOK, `{"events":[]}`), nil
	}))
	_, err := executeEventsCommand(t, client, "list")
	require.NoError(t, err)
}

func TestEventsGetCommandUsesCanonicalEventPath(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "/runtime/eventing/events/"+testEventID, request.URL.Path)
		assert.Equal(t, http.MethodGet, request.Method)
		return jsonResponse(http.StatusOK, `{"id":"`+testEventID+`","payload_bytes":42}`), nil
	}))
	output, err := executeEventsCommand(t, client, "get", testEventID)
	require.NoError(t, err)
	assert.Contains(t, output, `"payload_bytes": 42`)
}

func TestEventsGetRejectsInvalidIDBeforeRequest(t *testing.T) {
	t.Parallel()

	called := false
	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))
	_, err := executeEventsCommand(t, client, "get", "../events")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32 lowercase hexadecimal")
	assert.False(t, called)
}

func TestEventsPayloadCommandWritesExactBytes(t *testing.T) {
	t.Parallel()

	const payload = " \n{\"large\":9007199254740993,\"tiny\":1e-1000}\t"
	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "/runtime/eventing/events/"+testEventID+"/payload", request.URL.Path)
		assert.Equal(t, http.MethodGet, request.Method)
		return jsonResponse(http.StatusOK, payload), nil
	}))
	output, err := executeEventsCommand(t, client, "payload", testEventID)
	require.NoError(t, err)
	assert.Equal(t, payload, output)
}

func TestEventsPayloadRejectsInvalidIDBeforeRequest(t *testing.T) {
	t.Parallel()

	called := false
	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))
	_, err := executeEventsCommand(t, client, "payload", "ev_bad")
	require.Error(t, err)
	assert.False(t, called)
}

func TestEventsDispatchesCommandMapsEveryFilter(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "/runtime/eventing/dispatches", request.URL.Path)
		assert.Equal(t, testEventID, request.URL.Query().Get("event_id"))
		assert.Equal(t, "github-issue-triage.yml", request.URL.Query().Get("workflow_ref"))
		assert.Equal(t, "failed", request.URL.Query().Get("status"))
		assert.Equal(t, "10", request.URL.Query().Get("limit"))
		assert.Equal(t, "dispatch-cursor", request.URL.Query().Get("cursor"))
		return jsonResponse(http.StatusOK, `{"dispatches":[]}`), nil
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
	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))
	_, err := executeEventsCommand(t, client, "dispatches", "--event-id", "ev_bad")
	require.Error(t, err)
	assert.False(t, called)
}

func TestEventsReplayRequiresConfirmationBeforePIDOrHTTP(t *testing.T) {
	t.Parallel()

	pidRead := false
	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request made without --yes")
		return nil, nil
	}))
	client.readPID = func(string) *ppid.PidFileData {
		pidRead = true
		return nil
	}
	_, err := executeEventsCommand(t, client, "replay", testEventID)
	require.EqualError(t, err, "replay requires --yes because effects may repeat")
	assert.False(t, pidRead)
}

func TestEventsReplayConfirmedMakesOneRequest(t *testing.T) {
	t.Parallel()

	calls := 0
	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Equal(t, "{}", string(body))
		return jsonResponse(
			http.StatusCreated,
			`{"event":{"id":"ev_abcdefabcdefabcdefabcdefabcdefab"}}`,
		), nil
	}))
	output, err := executeEventsCommand(t, client, "replay", testEventID, "--yes")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Contains(t, output, "ev_abcdefabcdefabcdefabcdefabcdefab")
}

func TestEventsCommandSurfacesUnavailablePIDCleanly(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request made without live PID")
		return nil, nil
	}))
	client.readPID = func(string) *ppid.PidFileData { return nil }
	_, err := executeEventsCommand(t, client, "list")
	require.EqualError(t, err, "live gateway is unavailable; start picoclaw gateway and retry")
}

func TestEventsCommandPrettyPrintsWithoutInterpretingContent(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(
			http.StatusOK,
			`{"events":[{"payload_bytes":9007199254740993,"name":"\u001b[31m"}]}`,
		), nil
	}))
	output, err := executeEventsCommand(t, client, "list")
	require.NoError(t, err)
	assert.Contains(t, output, "9007199254740993")
	assert.Contains(t, output, `\u001b[31m`)
	assert.False(t, strings.ContainsRune(output, '\x1b'))
}
