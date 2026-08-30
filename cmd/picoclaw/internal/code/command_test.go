package code

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/localgateway"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

type fakeWorkspaceClient struct {
	capabilities  Capabilities
	repositories  []prworkspace.ConfiguredRepository
	resolved      prworkspace.ConfiguredRepository
	capabilityErr error
	listErr       error
	resolveErr    error
	create        func(context.Context, CreateRequest) (prworkspace.Aggregate, error)
	get           func(context.Context, string) (prworkspace.Aggregate, error)
	confirm       func(context.Context, ConfirmCharterRequest) (prworkspace.Aggregate, error)
	respond       func(context.Context, RespondGateRequest) (prworkspace.Aggregate, error)
	reconcile     func(context.Context, ReconcilePublicationRequest) (prworkspace.Aggregate, error)

	capabilityCalls int
	listCalls       int
	resolveCalls    int
	createCalls     []CreateRequest
	getCalls        int
	confirmCalls    int
	respondCalls    int
	reconcileCalls  int
}

func (client *fakeWorkspaceClient) Capabilities(context.Context) (Capabilities, error) {
	client.capabilityCalls++
	return client.capabilities, client.capabilityErr
}

func (client *fakeWorkspaceClient) ListRepositories(context.Context) ([]prworkspace.ConfiguredRepository, error) {
	client.listCalls++
	return client.repositories, client.listErr
}

func (client *fakeWorkspaceClient) ResolveRepository(
	context.Context,
	string,
) (prworkspace.ConfiguredRepository, error) {
	client.resolveCalls++
	return client.resolved, client.resolveErr
}

func (client *fakeWorkspaceClient) Create(
	ctx context.Context,
	request CreateRequest,
) (prworkspace.Aggregate, error) {
	client.createCalls = append(client.createCalls, request)
	return client.create(ctx, request)
}

func (client *fakeWorkspaceClient) Get(
	ctx context.Context,
	workspaceID string,
) (prworkspace.Aggregate, error) {
	client.getCalls++
	if client.get == nil {
		return completedAggregate(), nil
	}
	return client.get(ctx, workspaceID)
}

func (client *fakeWorkspaceClient) ConfirmCharter(
	ctx context.Context,
	request ConfirmCharterRequest,
) (prworkspace.Aggregate, error) {
	client.confirmCalls++
	return client.confirm(ctx, request)
}

func (client *fakeWorkspaceClient) RespondGate(
	ctx context.Context,
	request RespondGateRequest,
) (prworkspace.Aggregate, error) {
	client.respondCalls++
	return client.respond(ctx, request)
}

func (client *fakeWorkspaceClient) ReconcilePublication(
	ctx context.Context,
	request ReconcilePublicationRequest,
) (prworkspace.Aggregate, error) {
	client.reconcileCalls++
	return client.reconcile(ctx, request)
}

func commandTestDependencies(client workspaceClient, terminal bool) commandDependencies {
	return commandDependencies{
		newClient: func() (workspaceClient, error) { return client, nil },
		resolveRepository: func(context.Context, string) (string, error) {
			return "https://github.com/octo/repo", nil
		},
		random: bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)),
		sleep: func(ctx context.Context, _ time.Duration) error {
			return ctx.Err()
		},
		stdinIsTerminal: func() bool { return terminal },
	}
}

func executeCodeCommand(
	t *testing.T,
	ctx context.Context,
	dependencies commandDependencies,
	input string,
	arguments ...string,
) (string, string, error) {
	t.Helper()
	command := newCodeCommand(dependencies)
	command.SetArgs(arguments)
	command.SetContext(ctx)
	command.SetIn(strings.NewReader(input))
	var output bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&stderr)
	err := command.Execute()
	return output.String(), stderr.String(), err
}

func TestCodeCommandJSONCreatesAndProjectsOneExactResult(t *testing.T) {
	t.Parallel()

	client := &fakeWorkspaceClient{
		capabilities: Capabilities{Version: 1, ImplementFeatureReady: true, Missing: []string{}},
		resolved: prworkspace.ConfiguredRepository{
			Identity: "https://github.com|42", Name: "octo/repo", CanImplement: true,
		},
		repositories: []prworkspace.ConfiguredRepository{{
			Identity: "https://github.com|42", Name: "octo/repo", CanImplement: true,
		}},
	}
	client.create = func(_ context.Context, request CreateRequest) (prworkspace.Aggregate, error) {
		assert.Equal(t, "add bounded feature", request.Content)
		return completedAggregate(), nil
	}

	output, stderr, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, false),
		"",
		"--json",
		"--repo", "octo/repo",
		"add bounded feature",
	)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(output, "\n"))
	var result Result
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	assert.Equal(t, "devq_"+strings.Repeat("11", 16), result.RequestID)
	assert.Equal(t, actionStatus(prworkspace.ExecutionSucceeded), result.Status)
	assert.Equal(t, "https://github.com/octo/repo/pull/17", result.PullRequestURL)
	assert.Empty(t, result.ErrorCode)
	assert.NotContains(t, output, "add bounded feature")
	assert.Contains(t, stderr, "request_id="+result.RequestID)
	assert.Len(t, client.createCalls, 1)
	assert.Equal(t, 1, client.getCalls)
}

func TestCodeCommandUsesRealProtectedClientEndToEnd(t *testing.T) {
	homePath := t.TempDir()
	t.Setenv("PICOCLAW_HOME", homePath)
	calls := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer code-e2e-token", request.Header.Get("Authorization"))
		calls = append(calls, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case http.MethodGet + " " + prworkspace.RuntimeRoutePrefix + "/capabilities":
			_ = json.NewEncoder(writer).Encode(Capabilities{
				Version: 1, ImplementFeatureReady: true, Missing: []string{},
			})
		case http.MethodPost + " " + prworkspace.RuntimeRoutePrefix + "/repositories/resolve":
			_ = json.NewEncoder(writer).Encode(prworkspace.ConfiguredRepository{
				Identity: "https://github.com|42", Name: "octo/repo", CanImplement: true,
			})
		case http.MethodGet + " " + prworkspace.RuntimeRoutePrefix + "/repositories":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"repositories": []prworkspace.ConfiguredRepository{{
					Identity: "https://github.com|42", Name: "octo/repo", CanImplement: true,
				}},
			})
		case http.MethodPost + " " + prworkspace.RuntimeRoutePrefix:
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "implement_feature", body["intent"])
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(completedAggregate())
		case http.MethodGet + " " + prworkspace.RuntimeRoutePrefix + "/" + completedAggregate().Workspace.ID:
			_ = json.NewEncoder(writer).Encode(completedAggregate())
		default:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"code":"not_found"}`))
		}
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(endpoint.Port())
	require.NoError(t, err)
	pidData, err := json.Marshal(ppid.PidFileData{
		PID: os.Getpid(), Token: "code-e2e-token", Host: endpoint.Hostname(), Port: port,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(homePath, ".picoclaw.pid"), pidData, 0o600))

	dependencies := commandTestDependencies(nil, false)
	dependencies.newClient = func() (workspaceClient, error) { return NewClient() }
	output, stderr, err := executeCodeCommand(
		t,
		context.Background(),
		dependencies,
		"",
		"--json", "--repo", "octo/repo", "implement through protected client",
	)
	require.NoError(t, err, "output=%s stderr=%s calls=%v", output, stderr, calls)
	assert.Equal(t, []string{
		http.MethodGet + " " + prworkspace.RuntimeRoutePrefix + "/capabilities",
		http.MethodPost + " " + prworkspace.RuntimeRoutePrefix + "/repositories/resolve",
		http.MethodGet + " " + prworkspace.RuntimeRoutePrefix + "/repositories",
		http.MethodPost + " " + prworkspace.RuntimeRoutePrefix,
		http.MethodGet + " " + prworkspace.RuntimeRoutePrefix + "/" + completedAggregate().Workspace.ID,
	}, calls)
	assert.Contains(t, output, `"pull_request_url":"https://github.com/octo/repo/pull/17"`)
}

func TestCodeCommandCreateResponseLossRetriesExactRequest(t *testing.T) {
	t.Parallel()

	client := readyFakeClient()
	calls := 0
	client.create = func(_ context.Context, _ CreateRequest) (prworkspace.Aggregate, error) {
		calls++
		if calls == 1 {
			return prworkspace.Aggregate{}, errors.Join(
				localgateway.ErrRequestMayHaveBeenSent,
				localgateway.ErrAPIUnavailable,
			)
		}
		return completedAggregate(), nil
	}
	dependencies := commandTestDependencies(client, false)
	dependencies.sleep = func(context.Context, time.Duration) error { return nil }
	output, _, err := executeCodeCommand(
		t, context.Background(), dependencies, "", "--json", "task",
	)
	require.NoError(t, err)
	require.Len(t, client.createCalls, 2)
	assert.Equal(t, client.createCalls[0], client.createCalls[1])
	assert.NotEmpty(t, output)
}

func TestCodeCommandMalformedCreateSuccessRetriesStableRequest(t *testing.T) {
	t.Parallel()

	client := readyFakeClient()
	client.create = func(context.Context, CreateRequest) (prworkspace.Aggregate, error) {
		if len(client.createCalls) == 1 {
			malformed := completedAggregate()
			malformed.Workspace.RepositoryID = "99"
			malformed.ProviderSnapshot.RepositoryID = "99"
			return malformed, nil
		}
		return completedAggregate(), nil
	}
	dependencies := commandTestDependencies(client, false)
	dependencies.sleep = func(context.Context, time.Duration) error { return nil }
	output, _, err := executeCodeCommand(
		t,
		context.Background(),
		dependencies,
		"",
		"--json", "task",
	)
	require.NoError(t, err)
	require.Len(t, client.createCalls, 2)
	assert.Equal(t, client.createCalls[0], client.createCalls[1])
	assert.Equal(t, 1, client.getCalls)
	assert.Contains(t, output, `"error_code":""`)
}

func TestCodeCommandCapabilityFailurePrecedesRepositoryAndCreate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		missing []string
		want    string
	}{
		{missing: []string{"local_ci"}, want: "implementation_unavailable"},
		{missing: []string{"unsafe_provider"}, want: "unsafe_provider"},
	} {
		client := &fakeWorkspaceClient{capabilities: Capabilities{
			Version: 1, ImplementFeatureReady: false, Missing: test.missing,
		}}
		output, _, err := executeCodeCommand(
			t,
			context.Background(),
			commandTestDependencies(client, false),
			"",
			"--json", "task",
		)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 1, exitErr.ExitCode())
		assert.Contains(t, output, `"error_code":"`+test.want+`"`)
		assert.Zero(t, client.resolveCalls)
		assert.Zero(t, client.listCalls)
		assert.Empty(t, client.createCalls)
		assert.Zero(t, client.getCalls)
	}
}

func TestCodeCommandResumeDoesNotPreflightOrCreate(t *testing.T) {
	t.Parallel()

	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
		return completedAggregate(), nil
	}
	output, stderr, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, false),
		"",
		"--json", "--resume", completedAggregate().Workspace.ID,
	)
	require.NoError(t, err)
	assert.Zero(t, client.capabilityCalls)
	assert.Empty(t, client.createCalls)
	assert.Empty(t, stderr)
	assert.Contains(t, output, completedAggregate().Workspace.ID)
}

func TestCodeCommandResumeRejectsPickupWorkspaceBeforeMutation(t *testing.T) {
	t.Parallel()

	pickup := completedAggregate()
	pickup.Workspace.Intent = prworkspace.IntentPickupPR
	pickup.Workspace.SourceKind = prworkspace.SourcePullRequest
	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) { return pickup, nil }
	output, _, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, false),
		"",
		"--json", "--resume", pickup.Workspace.ID,
	)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, output, `"error_code":"malformed_response"`)
	assert.Zero(t, client.confirmCalls)
	assert.Zero(t, client.respondCalls)
	assert.Zero(t, client.reconcileCalls)
}

func TestCodeCommandNonInteractiveGateEmitsExitTwoAndPendingGate(t *testing.T) {
	t.Parallel()

	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
		return waitingGateAggregate(), nil
	}
	output, _, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, false),
		"",
		"--json", "--resume", waitingGateAggregate().Workspace.ID,
	)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 2, exitErr.ExitCode())
	var result Result
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	require.NotNil(t, result.PendingGate)
	assert.Equal(t, "human_gate_required", result.ErrorCode)
	assert.NotContains(t, output, "\x1b")
}

func TestCodeCommandInteractiveGateRespondsThenCompletes(t *testing.T) {
	t.Parallel()

	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
		return waitingGateAggregate(), nil
	}
	client.respond = func(_ context.Context, request RespondGateRequest) (prworkspace.Aggregate, error) {
		assert.Equal(t, map[string]any{"action": "accept"}, request.FieldValues)
		return completedAggregate(), nil
	}
	output, _, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, true),
		"accept\n",
		"--resume", waitingGateAggregate().Workspace.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, client.respondCalls)
	assert.Contains(t, output, "pull_request=https://github.com/octo/repo/pull/17")
}

func TestCodeCommandReconcilesUnknownPublicationThroughHumanGateExactlyTwice(t *testing.T) {
	t.Parallel()

	unknown := completedAggregate()
	unknown.Workspace.Phase = prworkspace.PhasePublication
	unknown.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	unknown.Workspace.Version = 7
	unknown.Publications[0].State = prworkspace.ExecutionUnknown
	waiting := unknown
	waiting.Workspace.Version = 8
	waiting.Gates = []prworkspace.GateRun{{
		ID: clientTestGateID, DecisionPoint: "pr.publication.reconcile",
		TargetID: unknown.Publications[0].ID, State: prworkspace.ExecutionWaitingUser,
		Turns: []prworkspace.GateTurn{{Status: "waiting", GateForm: &prworkspace.GateForm{
			Prompt: "Recheck provider?",
			Fields: []gatetypes.GateField{{
				ID: "action", Type: gatetypes.GateFieldSelect, Label: "Action",
				Required: true, MinSelections: 1, MaxSelections: 1,
				Options: []gatetypes.GateFieldOption{{
					ID: "recheck-provider", Label: "Recheck provider",
				}},
			}},
		}}},
	}}
	afterGate := unknown
	afterGate.Workspace.Version = 9
	afterGate.Gates = []prworkspace.GateRun{{
		ID: clientTestGateID, DecisionPoint: "pr.publication.reconcile",
		TargetID: unknown.Publications[0].ID, State: prworkspace.ExecutionSucceeded,
		Turns: []prworkspace.GateTurn{{
			Status: "completed", FieldValues: map[string]any{"action": "recheck-provider"},
		}},
	}}
	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) { return unknown, nil }
	client.reconcile = func(_ context.Context, _ ReconcilePublicationRequest) (prworkspace.Aggregate, error) {
		if client.reconcileCalls == 1 {
			return waiting, nil
		}
		return completedAggregate(), nil
	}
	client.respond = func(_ context.Context, request RespondGateRequest) (prworkspace.Aggregate, error) {
		assert.Equal(t, map[string]any{"action": "recheck-provider"}, request.FieldValues)
		return afterGate, nil
	}
	output, _, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, true),
		"recheck-provider\n",
		"--resume", unknown.Workspace.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, 2, client.reconcileCalls)
	assert.Equal(t, 1, client.respondCalls)
	assert.Contains(t, output, "pull_request=https://github.com/octo/repo/pull/17")
}

func TestCodeCommandDoesNotReconcileTwiceWithoutObservedRecheckResponse(t *testing.T) {
	t.Parallel()

	unknown := completedAggregate()
	unknown.Workspace.Phase = prworkspace.PhasePublication
	unknown.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	unknown.Publications[0].State = prworkspace.ExecutionUnknown
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
		if client.getCalls == 2 {
			cancel()
		}
		return unknown, nil
	}
	client.reconcile = func(context.Context, ReconcilePublicationRequest) (prworkspace.Aggregate, error) {
		return unknown, nil
	}
	dependencies := commandTestDependencies(client, false)
	dependencies.sleep = func(context.Context, time.Duration) error { return nil }
	output, _, err := executeCodeCommand(
		t, ctx, dependencies, "", "--json", "--resume", unknown.Workspace.ID,
	)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.ExitCode())
	assert.Equal(t, 1, client.reconcileCalls)
	assert.Contains(t, output, `"error_code":"interrupted"`)
}

func TestCodeCommandReconcileResponseLossUsesAuthoritativeCurrentWithoutDuplicate(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"may-have-sent", "conflict-current"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			unknown := completedAggregate()
			unknown.Workspace.Phase = prworkspace.PhasePublication
			unknown.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
			unknown.Workspace.Version = 7
			unknown.Publications[0].State = prworkspace.ExecutionUnknown
			waiting := unknown
			waiting.Workspace.Version = 8
			waiting.Gates = []prworkspace.GateRun{reconcileWaitingGate(unknown.Publications[0].ID)}
			afterGate := unknown
			afterGate.Workspace.Version = 9
			afterGate.Gates = []prworkspace.GateRun{reconcileAnsweredGate(unknown.Publications[0].ID)}

			client := &fakeWorkspaceClient{}
			client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
				if mode == "may-have-sent" && client.getCalls == 2 {
					return waiting, nil
				}
				return unknown, nil
			}
			var requests []ReconcilePublicationRequest
			client.reconcile = func(
				_ context.Context,
				request ReconcilePublicationRequest,
			) (prworkspace.Aggregate, error) {
				requests = append(requests, request)
				if client.reconcileCalls == 1 {
					if mode == "conflict-current" {
						current := waiting
						return prworkspace.Aggregate{}, &APIError{
							StatusCode: http.StatusConflict, Code: "version_conflict", Current: &current,
						}
					}
					return prworkspace.Aggregate{}, errors.Join(
						localgateway.ErrRequestMayHaveBeenSent,
						localgateway.ErrAPIUnavailable,
					)
				}
				return completedAggregate(), nil
			}
			client.respond = func(context.Context, RespondGateRequest) (prworkspace.Aggregate, error) {
				return afterGate, nil
			}

			output, _, err := executeCodeCommand(
				t,
				context.Background(),
				commandTestDependencies(client, true),
				"recheck-provider\n",
				"--resume", unknown.Workspace.ID,
			)
			require.NoError(t, err)
			require.Len(t, requests, 2)
			assert.NotEqual(t, requests[0].RequestID, requests[1].RequestID)
			assert.Equal(t, 2, client.reconcileCalls)
			assert.Equal(t, 1, client.respondCalls)
			if mode == "may-have-sent" {
				assert.Equal(t, 2, client.getCalls)
			} else {
				assert.Equal(t, 1, client.getCalls)
			}
			assert.Equal(t, 1, strings.Count(output, "https://github.com/octo/repo/pull/17"))
		})
	}
}

func reconcileWaitingGate(publicationID string) prworkspace.GateRun {
	return prworkspace.GateRun{
		ID: clientTestGateID, DecisionPoint: "pr.publication.reconcile",
		TargetID: publicationID, State: prworkspace.ExecutionWaitingUser,
		Turns: []prworkspace.GateTurn{{Status: "waiting", GateForm: &prworkspace.GateForm{
			Prompt: "Recheck provider?",
			Fields: []gatetypes.GateField{{
				ID: "action", Type: gatetypes.GateFieldSelect, Label: "Action",
				Required: true, MinSelections: 1, MaxSelections: 1,
				Options: []gatetypes.GateFieldOption{{
					ID: "recheck-provider", Label: "Recheck provider",
				}},
			}},
		}}},
	}
}

func reconcileAnsweredGate(publicationID string) prworkspace.GateRun {
	return prworkspace.GateRun{
		ID: clientTestGateID, DecisionPoint: "pr.publication.reconcile",
		TargetID: publicationID, State: prworkspace.ExecutionSucceeded,
		Turns: []prworkspace.GateTurn{{
			Status: "completed", FieldValues: map[string]any{"action": "recheck-provider"},
		}},
	}
}

func TestCodeCommandAcceptsClarifiedCharterThenCompletes(t *testing.T) {
	t.Parallel()

	charter := completedAggregate()
	charter.Workspace.Phase = prworkspace.PhaseCharter
	charter.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	charter.Workspace.ActiveCharterID = ""
	charter.Workspace.Version = 3
	charter.Charters = []prworkspace.Charter{{
		ID: "pcr_11111111111111111111111111111111", Revision: 1,
		ClarificationNeeded: true, ClarificationQuestion: "Use the bounded behavior?",
	}}
	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) { return charter, nil }
	client.confirm = func(_ context.Context, request ConfirmCharterRequest) (prworkspace.Aggregate, error) {
		assert.Equal(t, int64(1), request.ExpectedCharterRevision)
		return completedAggregate(), nil
	}
	output, _, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, true),
		"yes\n",
		"--resume", charter.Workspace.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, client.confirmCalls)
	assert.Contains(t, output, "Use the bounded behavior?")
}

func TestCodeCommandGateResponseLossUsesAuthoritativeGetWithoutDuplicatePrompt(t *testing.T) {
	t.Parallel()

	waiting := waitingGateAggregate()
	getCalls := 0
	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
		getCalls++
		if getCalls == 1 {
			return waiting, nil
		}
		return completedAggregate(), nil
	}
	client.respond = func(context.Context, RespondGateRequest) (prworkspace.Aggregate, error) {
		return prworkspace.Aggregate{}, errors.Join(
			localgateway.ErrRequestMayHaveBeenSent,
			localgateway.ErrAPIUnavailable,
		)
	}
	_, _, err := executeCodeCommand(
		t,
		context.Background(),
		commandTestDependencies(client, true),
		"accept\n",
		"--resume", waiting.Workspace.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, client.respondCalls)
	assert.Equal(t, 2, getCalls)
}

func TestCodeCommandCanceledContextExits130WithoutMutation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeWorkspaceClient{}
	client.get = func(ctx context.Context, _ string) (prworkspace.Aggregate, error) {
		return prworkspace.Aggregate{}, ctx.Err()
	}
	output, _, err := executeCodeCommand(
		t,
		ctx,
		commandTestDependencies(client, false),
		"",
		"--json", "--resume", completedAggregate().Workspace.ID,
	)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.ExitCode())
	assert.Contains(t, output, `"error_code":"interrupted"`)
	assert.Zero(t, client.respondCalls)
	assert.Zero(t, client.reconcileCalls)
}

func TestCodeCommandUnchangedQueuedWorkspacePollsUntilCallerCancellation(t *testing.T) {
	t.Parallel()

	queued := completedAggregate()
	queued.Workspace.Phase = prworkspace.PhasePlanning
	queued.Workspace.ExecutionState = prworkspace.ExecutionQueued
	queued.Workspace.Version = 4
	queued.RepairAttempts = nil
	queued.ValidationRuns = nil
	queued.Publications = nil
	queued.ProviderSnapshot.PullRequestID = ""
	queued.ProviderSnapshot.PullNumber = 0
	queued.ProviderSnapshot.HeadRef = ""
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeWorkspaceClient{}
	client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
		if client.getCalls == 101 {
			cancel()
		}
		return queued, nil
	}
	dependencies := commandTestDependencies(client, false)
	dependencies.sleep = func(context.Context, time.Duration) error { return nil }
	output, _, err := executeCodeCommand(
		t,
		ctx,
		dependencies,
		"",
		"--json", "--resume", queued.Workspace.ID,
	)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.ExitCode())
	assert.Equal(t, 101, client.getCalls)
	assert.Zero(t, client.confirmCalls)
	assert.Zero(t, client.respondCalls)
	assert.Zero(t, client.reconcileCalls)
	assert.Contains(t, output, `"error_code":"interrupted"`)
}

func TestCodeCommandRejectsInvalidContractsBeforeClientEffects(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"--json"},
		{"--json", " task "},
		{"--json", "--resume", "devw_bad"},
		{"--json", "--resume", completedAggregate().Workspace.ID, "task"},
		{"--json", "--request-id", "short", "task"},
	}
	for _, arguments := range tests {
		client := &fakeWorkspaceClient{}
		output, _, err := executeCodeCommand(
			t, context.Background(), commandTestDependencies(client, false), "", arguments...,
		)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Contains(t, output, `"error_code":"invalid_request"`)
		assert.Zero(t, client.capabilityCalls)
	}
}

func TestCodeJSONInvocationDetectionIsNarrow(t *testing.T) {
	t.Parallel()

	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json", "task"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "--no-color", "code", "--json=true"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=T", "task"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=TRUE", "task"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=1", "task"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=false", "task"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=F", "task"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=0", "task"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--", "--json"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json=false", "--json", "task"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--json", "--json=false", "task"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "events", "--json"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "agent", "code", "--json"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--json", "--help"}))
	assert.False(t, IsJSONInvocation([]string{"picoclaw", "code", "--json", "--help=true"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json", "--help=false"}))
	assert.True(t, IsJSONInvocation([]string{"picoclaw", "code", "--json", "--help", "--help=false"}))

	assert.True(t, IsCodeInvocation([]string{"picoclaw", "code", "task"}))
	assert.True(t, IsCodeInvocation([]string{"picoclaw", "--no-color=false", "code", "task"}))
	assert.False(t, IsCodeInvocation([]string{"picoclaw", "agent", "code"}))
	assert.False(t, IsCodeInvocation([]string{"picoclaw", "--", "code"}))
	assert.False(t, IsCodeInvocation([]string{"picoclaw", "events", "--json"}))
}

func TestExitErrorIsHandledAndStable(t *testing.T) {
	t.Parallel()

	failure := &ExitError{code: 2}
	assert.Equal(t, 2, failure.ExitCode())
	assert.True(t, failure.CLIErrorHandled())
	assert.NotContains(t, failure.Error(), "secret")
}

func TestCodeCommandHelpersCoverValidationRetriesAndSafeErrors(t *testing.T) {
	t.Parallel()

	assert.Error(t, validateCapabilities(Capabilities{}))
	assert.Error(t, validateCapabilities(Capabilities{
		Version: 1, ImplementFeatureReady: false, Missing: []string{"bad-code"},
	}))
	assert.Error(t, validateCapabilities(Capabilities{
		Version: 1, ImplementFeatureReady: false, Missing: []string{"local_ci", "local_ci"},
	}))
	assert.NoError(t, validateCapabilities(Capabilities{
		Version: 1, ImplementFeatureReady: false, Missing: []string{"local_ci"},
	}))

	assert.False(t, transientError(ErrInvalidResponse))
	assert.False(t, transientError(&APIError{StatusCode: 503, Code: "unavailable"}))
	assert.True(t, transientError(localgateway.ErrAPIUnavailable))
	assert.Equal(t, "unsafe_provider", errorCode(&APIError{Code: "unsafe_provider"}))
	assert.Equal(t, "gateway_unavailable", errorCode(&APIError{Code: "private_internal_code"}))
	assert.Equal(t, "malformed_response", errorCode(ErrInvalidResponse))
	assert.Equal(t, "repository_not_configured", errorCode(errors.New("repository_not_configured")))
	assert.Equal(t, "gateway_unavailable", errorCode(errors.New("private detail")))

	short := mutationRequestID("base", "gate", "target", 1)
	long := mutationRequestID(strings.Repeat("x", 128), "gate", strings.Repeat("y", 128), 1)
	assert.LessOrEqual(t, len(short), 128)
	assert.Regexp(t, `^devmut_[0-9a-f]{32}$`, long)
}

func TestCodeCommandBindsEveryAggregateResponse(t *testing.T) {
	t.Parallel()

	t.Run("create repository identity", func(t *testing.T) {
		t.Parallel()

		client := readyFakeClient()
		client.create = func(context.Context, CreateRequest) (prworkspace.Aggregate, error) {
			value := completedAggregate()
			value.Workspace.RepositoryID = "99"
			value.ProviderSnapshot.RepositoryID = "99"
			return value, nil
		}
		output, _, err := executeCodeCommand(
			t, context.Background(), commandTestDependencies(client, false), "", "--json", "task",
		)
		require.Error(t, err)
		assert.Contains(t, output, `"error_code":"malformed_response"`)
		assert.Zero(t, client.getCalls)
	})

	t.Run("immediate get workspace id", func(t *testing.T) {
		t.Parallel()

		client := readyFakeClient()
		client.create = func(context.Context, CreateRequest) (prworkspace.Aggregate, error) {
			return completedAggregate(), nil
		}
		client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
			value := completedAggregate()
			value.Workspace.ID = "devw_22222222222222222222222222222222"
			return value, nil
		}
		output, _, err := executeCodeCommand(
			t, context.Background(), commandTestDependencies(client, false), "", "--json", "task",
		)
		require.Error(t, err)
		assert.Contains(t, output, `"error_code":"malformed_response"`)
		assert.Equal(t, 1, client.getCalls)
	})

	t.Run("resume intent and source", func(t *testing.T) {
		t.Parallel()

		client := &fakeWorkspaceClient{}
		client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
			value := completedAggregate()
			value.ProviderSnapshot.Intent = prworkspace.IntentPickupPR
			value.ProviderSnapshot.SourceKind = prworkspace.SourcePullRequest
			return value, nil
		}
		output, _, err := executeCodeCommand(
			t,
			context.Background(),
			commandTestDependencies(client, false),
			"",
			"--json", "--resume", completedAggregate().Workspace.ID,
		)
		require.Error(t, err)
		assert.Contains(t, output, `"error_code":"malformed_response"`)
	})

	t.Run("mutation workspace id", func(t *testing.T) {
		t.Parallel()

		client := &fakeWorkspaceClient{}
		client.get = func(context.Context, string) (prworkspace.Aggregate, error) {
			return waitingGateAggregate(), nil
		}
		client.respond = func(context.Context, RespondGateRequest) (prworkspace.Aggregate, error) {
			value := completedAggregate()
			value.Workspace.ID = "devw_22222222222222222222222222222222"
			return value, nil
		}
		output, _, err := executeCodeCommand(
			t,
			context.Background(),
			commandTestDependencies(client, true),
			"accept\n",
			"--json=false", "--resume", completedAggregate().Workspace.ID,
		)
		require.Error(t, err)
		assert.Contains(t, output, "error=malformed_response")
		assert.Equal(t, 1, client.respondCalls)
	})
}

func TestCodeCommandNormalizesRepositoryAPIErrors(t *testing.T) {
	t.Parallel()

	client := readyFakeClient()
	client.resolveErr = &APIError{StatusCode: http.StatusBadRequest, Code: "repository_unavailable"}
	output, _, err := executeCodeCommand(
		t, context.Background(), commandTestDependencies(client, false), "", "--json", "task",
	)
	require.Error(t, err)
	assert.Contains(t, output, `"error_code":"repository_not_configured"`)
	assert.Empty(t, client.createCalls)
}

func TestRetryValueRetriesCallDeadlineWhileParentIsLive(t *testing.T) {
	t.Parallel()

	calls := 0
	value, err := retryValue(
		context.Background(),
		func(context.Context, time.Duration) error { return nil },
		func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, context.DeadlineExceeded
			}
			return 17, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 17, value)
	assert.Equal(t, 2, calls)
}

func TestReadLineReturnsPromptlyWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	input, output := io.Pipe()
	runner := commandRunner{reader: bufio.NewReaderSize(input, (64<<10)+1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.readLine(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("terminal read did not observe context cancellation")
	}
	require.NoError(t, output.Close())
	require.NoError(t, input.Close())
}

func TestRenderCharterIncludesEveryBoundedSection(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	runner := commandRunner{output: &output}
	runner.renderCharter(prworkspace.Charter{
		Type: prworkspace.PRTypeFeature, Goal: "Ship\x1b[31m safely",
		AcceptanceCriteria: []string{"tests pass"}, IncludedAreas: []string{"CLI"},
		ExcludedAreas: []string{"server"}, NonGoals: []string{"general shell"},
		BaseSHA: "base", HeadSHA: "head",
	})
	for _, wanted := range []string{
		"Charter type: feature", "Goal: Ship", "Acceptance criteria:", "tests pass",
		"Included areas:", "CLI", "Excluded areas:", "server", "Non-goals:",
		"general shell", "Base revision: base", "Head revision: head",
	} {
		assert.Contains(t, output.String(), wanted)
	}
	assert.NotContains(t, output.String(), "\x1b")
}

func TestReadGateFieldSupportsEveryDeclaredFieldType(t *testing.T) {
	t.Parallel()

	runner := commandRunner{
		reader: bufio.NewReader(strings.NewReader("text\nyes\none\none,two\n")),
		output: io.Discard,
	}
	tests := []struct {
		field gatetypes.GateField
		want  any
	}{
		{field: gatetypes.GateField{ID: "text", Type: gatetypes.GateFieldShortText, Label: "Text"}, want: "text"},
		{field: gatetypes.GateField{ID: "boolean", Type: gatetypes.GateFieldBoolean, Label: "Bool"}, want: true},
		{
			field: gatetypes.GateField{
				ID: "one", Type: gatetypes.GateFieldSelect, Label: "One", MinSelections: 1, MaxSelections: 1,
				Options: []gatetypes.GateFieldOption{{ID: "one", Label: "One"}},
			},
			want: "one",
		},
		{
			field: gatetypes.GateField{
				ID: "many", Type: gatetypes.GateFieldSelect, Label: "Many", MinSelections: 1, MaxSelections: 2,
				Options: []gatetypes.GateFieldOption{{ID: "one", Label: "One"}, {ID: "two", Label: "Two"}},
			},
			want: []string{"one", "two"},
		},
	}
	for _, test := range tests {
		got, present, err := runner.readGateField(context.Background(), test.field)
		require.NoError(t, err)
		assert.True(t, present)
		assert.Equal(t, test.want, got)
	}
}

func readyFakeClient() *fakeWorkspaceClient {
	configured := prworkspace.ConfiguredRepository{
		Identity: "https://github.com|42", Name: "octo/repo", CanImplement: true,
	}
	return &fakeWorkspaceClient{
		capabilities: Capabilities{Version: 1, ImplementFeatureReady: true, Missing: []string{}},
		resolved:     configured, repositories: []prworkspace.ConfiguredRepository{configured},
	}
}

func waitingGateAggregate() prworkspace.Aggregate {
	value := completedAggregate()
	value.Workspace.Phase = prworkspace.PhaseImplementation
	value.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	value.Workspace.Version = 7
	value.Gates = []prworkspace.GateRun{{
		ID: clientTestGateID, DecisionPoint: "pr.implementation.complete",
		State: prworkspace.ExecutionWaitingUser, SubjectRevision: "sha256:subject",
		Turns: []prworkspace.GateTurn{{
			Status: "waiting",
			GateForm: &prworkspace.GateForm{
				Prompt: "Approve?\x1b[31m",
				Fields: []gatetypes.GateField{{
					ID: "action", Type: gatetypes.GateFieldSelect, Label: "Action",
					Required: true, MinSelections: 1, MaxSelections: 1,
					Options: []gatetypes.GateFieldOption{{ID: "accept", Label: "Accept"}},
				}},
			},
		}},
	}}
	return value
}

func actionStatus(state prworkspace.ExecutionState) string {
	return string(state)
}
