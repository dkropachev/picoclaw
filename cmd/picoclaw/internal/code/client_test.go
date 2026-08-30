package code

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/localgateway"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

const (
	clientTestWorkspaceID   = "devw_11111111111111111111111111111111"
	clientTestGateID        = "pgr_22222222222222222222222222222222"
	clientTestPublicationID = "ppb_33333333333333333333333333333333"
	clientTestRequestID     = "devq_44444444444444444444444444444444"
)

type clientGatewayFunc func(
	context.Context,
	localgateway.Request,
) (localgateway.Response, error)

func (function clientGatewayFunc) DoJSON(
	ctx context.Context,
	request localgateway.Request,
) (localgateway.Response, error) {
	return function(ctx, request)
}

func newClientForTest(function clientGatewayFunc) *Client {
	return &Client{transport: function}
}

func clientAggregateResponse(status int, workspaceID string) localgateway.Response {
	body, _ := json.Marshal(prworkspace.Aggregate{
		Workspace: prworkspace.Workspace{ID: workspaceID, Version: 7},
	})
	return localgateway.Response{StatusCode: status, Body: body}
}

func assertClientRequest(
	t *testing.T,
	request localgateway.Request,
	method string,
	path string,
	wantBody string,
) {
	t.Helper()
	assert.Equal(t, method, request.Method)
	assert.Equal(t, path, request.Path)
	assert.Empty(t, request.Query)
	if wantBody == "" {
		assert.Nil(t, request.Body)
		return
	}
	assert.JSONEq(t, wantBody, string(request.Body))
}

func TestNewClientUsesProtectedDevelopmentRuntime(t *testing.T) {
	t.Parallel()

	client, err := NewClient()
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.transport)
}

func TestClientCapabilities(t *testing.T) {
	t.Parallel()

	client := newClientForTest(func(
		ctx context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.NotNil(t, ctx)
		assertClientRequest(
			t,
			request,
			http.MethodGet,
			prworkspace.RuntimeRoutePrefix+"/capabilities",
			"",
		)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body: []byte(
				`{"version":1,"implement_feature_ready":false,"missing":["local_ci"]}`,
			),
		}, nil
	})

	got, err := client.Capabilities(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Capabilities{
		Version: 1, ImplementFeatureReady: false, Missing: []string{"local_ci"},
	}, got)
}

func TestClientListsAndResolvesRepositories(t *testing.T) {
	t.Parallel()

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		client := newClientForTest(func(
			_ context.Context,
			request localgateway.Request,
		) (localgateway.Response, error) {
			assertClientRequest(
				t,
				request,
				http.MethodGet,
				prworkspace.RuntimeRoutePrefix+"/repositories",
				"",
			)
			return localgateway.Response{
				StatusCode: http.StatusOK,
				Body: []byte(
					`{"repositories":[{"identity":"https://github.com|7","name":"acme/repo",` +
						`"default_branch":"main","can_implement":true}]}`,
				),
			}, nil
		})

		got, err := client.ListRepositories(context.Background())
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, prworkspace.ConfiguredRepository{
			Identity:      "https://github.com|7",
			Name:          "acme/repo",
			DefaultBranch: "main",
			CanImplement:  true,
		}, got[0])
	})

	t.Run("resolve", func(t *testing.T) {
		t.Parallel()
		client := newClientForTest(func(
			_ context.Context,
			request localgateway.Request,
		) (localgateway.Response, error) {
			assertClientRequest(
				t,
				request,
				http.MethodPost,
				prworkspace.RuntimeRoutePrefix+"/repositories/resolve",
				`{"repository_url":"https://github.com/acme/repo"}`,
			)
			return localgateway.Response{
				StatusCode: http.StatusOK,
				Body: []byte(
					`{"identity":"https://github.com|7","name":"acme/repo",` +
						`"default_branch":"main","can_implement":true}`,
				),
			}, nil
		})

		got, err := client.ResolveRepository(
			context.Background(),
			"https://github.com/acme/repo",
		)
		require.NoError(t, err)
		assert.Equal(t, "https://github.com|7", got.Identity)
		assert.True(t, got.CanImplement)
	})
}

func TestClientCreatesExactFeatureBrief(t *testing.T) {
	t.Parallel()

	client := newClientForTest(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assertClientRequest(
			t,
			request,
			http.MethodPost,
			prworkspace.RuntimeRoutePrefix,
			`{
				"intent":"implement_feature",
				"source":{
					"kind":"brief",
					"repository_identity":"https://github.com|7",
					"content":"add the feature"
				},
				"request_id":"`+clientTestRequestID+`"
			}`,
		)
		return clientAggregateResponse(http.StatusCreated, clientTestWorkspaceID), nil
	})

	got, err := client.Create(context.Background(), CreateRequest{
		RequestID:          clientTestRequestID,
		RepositoryIdentity: "https://github.com|7",
		Content:            "add the feature",
	})
	require.NoError(t, err)
	assert.Equal(t, clientTestWorkspaceID, got.Workspace.ID)
	assert.Equal(t, int64(7), got.Workspace.Version)
}

func TestClientGetsWorkspace(t *testing.T) {
	t.Parallel()

	client := newClientForTest(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assertClientRequest(
			t,
			request,
			http.MethodGet,
			prworkspace.RuntimeRoutePrefix+"/"+clientTestWorkspaceID,
			"",
		)
		return clientAggregateResponse(http.StatusOK, clientTestWorkspaceID), nil
	})

	got, err := client.Get(context.Background(), clientTestWorkspaceID)
	require.NoError(t, err)
	assert.Equal(t, clientTestWorkspaceID, got.Workspace.ID)
}

func TestClientBuildsFencedMutationRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		wantBody string
		invoke   func(*Client) (prworkspace.Aggregate, error)
	}{
		{
			name: "charter confirm",
			path: prworkspace.RuntimeRoutePrefix + "/" + clientTestWorkspaceID + "/charter/confirm",
			wantBody: `{
				"expected_version":7,
				"request_id":"` + clientTestRequestID + `",
				"expected_charter_revision":3
			}`,
			invoke: func(client *Client) (prworkspace.Aggregate, error) {
				return client.ConfirmCharter(context.Background(), ConfirmCharterRequest{
					WorkspaceID: clientTestWorkspaceID, ExpectedVersion: 7,
					ExpectedCharterRevision: 3, RequestID: clientTestRequestID,
				})
			},
		},
		{
			name: "gate response uses hyphenated field-values",
			path: prworkspace.RuntimeRoutePrefix + "/" + clientTestWorkspaceID + "/gates/" +
				clientTestGateID + "/respond",
			wantBody: `{
				"expected_version":7,
				"request_id":"` + clientTestRequestID + `",
				"field-values":{"decision":"publish","approved":true}
			}`,
			invoke: func(client *Client) (prworkspace.Aggregate, error) {
				return client.RespondGate(context.Background(), RespondGateRequest{
					WorkspaceID: clientTestWorkspaceID, GateID: clientTestGateID,
					ExpectedVersion: 7, RequestID: clientTestRequestID,
					FieldValues: map[string]any{"decision": "publish", "approved": true},
				})
			},
		},
		{
			name: "publication reconcile",
			path: prworkspace.RuntimeRoutePrefix + "/" + clientTestWorkspaceID + "/publications/" +
				clientTestPublicationID + "/reconcile",
			wantBody: `{
				"expected_version":7,
				"request_id":"` + clientTestRequestID + `",
				"expected_head_revision":"0123456789abcdef"
			}`,
			invoke: func(client *Client) (prworkspace.Aggregate, error) {
				return client.ReconcilePublication(
					context.Background(),
					ReconcilePublicationRequest{
						WorkspaceID: clientTestWorkspaceID, PublicationID: clientTestPublicationID,
						ExpectedVersion: 7, RequestID: clientTestRequestID,
						ExpectedHeadRevision: "0123456789abcdef",
					},
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := newClientForTest(func(
				_ context.Context,
				request localgateway.Request,
			) (localgateway.Response, error) {
				assertClientRequest(t, request, http.MethodPost, test.path, test.wantBody)
				if test.name == "gate response uses hyphenated field-values" {
					assert.NotContains(t, string(request.Body), "field_values")
				}
				return clientAggregateResponse(http.StatusOK, clientTestWorkspaceID), nil
			})

			got, err := test.invoke(client)
			require.NoError(t, err)
			assert.Equal(t, clientTestWorkspaceID, got.Workspace.ID)
		})
	}
}

func TestClientReturnsBoundedTypedAPIErrorStatusFirst(t *testing.T) {
	t.Parallel()

	const secret = "secret response detail must not escape"
	current := `{"workspace":{"id":"` + clientTestWorkspaceID + `","version":9}}`
	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{
			StatusCode: http.StatusConflict,
			Body: []byte(
				`{"code":"version_conflict","message":"` + secret + `","current":` + current + `}`,
			),
		}, errors.Join(localgateway.ErrRequestMayHaveBeenSent, localgateway.ErrInvalidJSON)
	})

	_, err := client.Create(context.Background(), CreateRequest{
		RequestID:          clientTestRequestID,
		RepositoryIdentity: "https://github.com|7",
		Content:            "add the feature",
	})
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.StatusCode)
	assert.Equal(t, "version_conflict", apiErr.Code)
	require.NotNil(t, apiErr.Current)
	assert.Equal(t, clientTestWorkspaceID, apiErr.Current.Workspace.ID)
	assert.Equal(t, int64(9), apiErr.Current.Workspace.Version)
	assert.NotContains(t, apiErr.Error(), secret)
	assert.True(t, RequestMayHaveBeenSent(err))
}

func TestClientAPIErrorRejectsUnstableEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "invalid code", body: `{"code":"UPPER OR SECRET","current":{}}`},
		{name: "invalid current", body: `{"code":"version_conflict","current":"secret"}`},
		{name: "not envelope", body: `{"message":"secret"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := newClientForTest(func(
				_ context.Context,
				_ localgateway.Request,
			) (localgateway.Response, error) {
				return localgateway.Response{
					StatusCode: http.StatusBadGateway,
					Body:       []byte(test.body),
				}, nil
			})

			_, err := client.Get(context.Background(), clientTestWorkspaceID)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, "invalid_response", apiErr.Code)
			assert.Nil(t, apiErr.Current)
			assert.NotContains(t, apiErr.Error(), "secret")
			assert.False(t, RequestMayHaveBeenSent(err))
		})
	}
}

func TestClientMarksUndecodableMutationSuccessAsMayHaveBeenSent(t *testing.T) {
	t.Parallel()

	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{
			StatusCode: http.StatusCreated,
			Body:       []byte(`{"workspace":"invalid"}`),
		}, nil
	})

	_, err := client.Create(context.Background(), CreateRequest{
		RequestID:          clientTestRequestID,
		RepositoryIdentity: "https://github.com|7",
		Content:            "add the feature",
	})
	require.ErrorIs(t, err, ErrInvalidResponse)
	assert.True(t, RequestMayHaveBeenSent(err))
}

func TestClientDoesNotMarkUndecodableGETAsMayHaveBeenSent(t *testing.T) {
	t.Parallel()

	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"workspace":"invalid"}`),
		}, nil
	})

	_, err := client.Get(context.Background(), clientTestWorkspaceID)
	require.ErrorIs(t, err, ErrInvalidResponse)
	assert.False(t, RequestMayHaveBeenSent(err))
}

func TestClientPreservesTransportDispatchMarker(t *testing.T) {
	t.Parallel()

	transportErr := errors.Join(
		localgateway.ErrRequestMayHaveBeenSent,
		localgateway.ErrAPIUnavailable,
	)
	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{}, transportErr
	})

	_, err := client.Create(context.Background(), CreateRequest{
		RequestID:          clientTestRequestID,
		RepositoryIdentity: "https://github.com|7",
		Content:            "add the feature",
	})
	require.ErrorIs(t, err, localgateway.ErrAPIUnavailable)
	assert.True(t, RequestMayHaveBeenSent(err))
}

func TestClientRejectsInvalidInputBeforeDispatch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		calls.Add(1)
		return clientAggregateResponse(http.StatusOK, clientTestWorkspaceID), nil
	})

	_, err := client.Get(context.Background(), "../workspace")
	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.False(t, RequestMayHaveBeenSent(err))

	_, err = client.Create(context.Background(), CreateRequest{
		RequestID: "short", RepositoryIdentity: "https://github.com|7", Content: "task",
	})
	require.ErrorIs(t, err, ErrInvalidRequest)

	_, err = client.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: clientTestWorkspaceID, GateID: clientTestGateID, ExpectedVersion: 7,
		RequestID: clientTestRequestID, FieldValues: map[string]any{"score": math.NaN()},
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.False(t, RequestMayHaveBeenSent(err))
	assert.Zero(t, calls.Load())
}

func TestClientBoundsResponseBeforeDecode(t *testing.T) {
	t.Parallel()

	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body: []byte(
				`{"padding":"` + strings.Repeat("x", clientMaxResponseBodyBytes) + `"}`,
			),
		}, nil
	})

	_, err := client.Get(context.Background(), clientTestWorkspaceID)
	require.ErrorIs(t, err, ErrInvalidResponse)
	assert.False(t, RequestMayHaveBeenSent(err))
}

func TestNilClientFailsClosed(t *testing.T) {
	t.Parallel()

	for _, client := range []*Client{nil, {}} {
		_, err := client.Capabilities(context.Background())
		require.ErrorIs(t, err, ErrClientUnavailable)
		assert.False(t, RequestMayHaveBeenSent(err))
	}
}

func TestClientCoversBoundedValidationFailures(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	client := newClientForTest(func(
		_ context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		calls.Add(1)
		return clientAggregateResponse(http.StatusOK, clientTestWorkspaceID), nil
	})

	_, err := client.ResolveRepository(context.Background(), "")
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = client.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: clientTestWorkspaceID, ExpectedVersion: 7,
		ExpectedCharterRevision: 0, RequestID: clientTestRequestID,
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = client.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: clientTestWorkspaceID, GateID: clientTestGateID,
		ExpectedVersion: 7, RequestID: clientTestRequestID,
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
	_, err = client.ReconcilePublication(context.Background(), ReconcilePublicationRequest{
		WorkspaceID: clientTestWorkspaceID, PublicationID: clientTestPublicationID,
		ExpectedVersion: 7, RequestID: clientTestRequestID,
	})
	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.Zero(t, calls.Load())

	assert.False(t, validClientOpaqueID("devw_"+strings.Repeat("A", 32), "devw_"))
	assert.False(t, validClientRequestID("invalid request id"))
	assert.False(t, validClientTask(""))
	assert.False(t, validClientBoundedText("too long", 3, true))
	assert.False(t, validClientBoundedText("", 8, false))
	assert.False(t, validClientBoundedText("bad\x7f", 8, false))
	assert.True(t, validClientBoundedText("", 8, true))
	assert.False(t, validClientAPIErrorCode(""))
	assert.False(t, validClientAPIErrorCode(strings.Repeat("a", clientMaxAPIErrorCodeBytes+1)))
}

func TestClientCoversTransportAndEnvelopeEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("nil API error", func(t *testing.T) {
		t.Parallel()
		var failure *APIError
		assert.Equal(t, "development workspace API request failed", failure.Error())
		assert.False(t, failure.Is(localgateway.ErrRequestMayHaveBeenSent))
	})

	t.Run("expected status with transport failure", func(t *testing.T) {
		t.Parallel()
		transportErr := errors.New("transport failed")
		client := newClientForTest(func(
			_ context.Context,
			_ localgateway.Request,
		) (localgateway.Response, error) {
			return localgateway.Response{StatusCode: http.StatusOK, Body: []byte(`{}`)}, transportErr
		})
		_, err := client.Capabilities(context.Background())
		require.ErrorIs(t, err, transportErr)
	})

	t.Run("missing status and nil target", func(t *testing.T) {
		t.Parallel()
		client := newClientForTest(func(
			_ context.Context,
			_ localgateway.Request,
		) (localgateway.Response, error) {
			return localgateway.Response{}, nil
		})
		err := client.do(context.Background(), http.MethodGet, "/test", nil, http.StatusOK, &struct{}{})
		require.ErrorIs(t, err, ErrInvalidResponse)

		client = newClientForTest(func(
			_ context.Context,
			_ localgateway.Request,
		) (localgateway.Response, error) {
			return localgateway.Response{StatusCode: http.StatusOK, Body: []byte(`{}`)}, nil
		})
		err = client.do(context.Background(), http.MethodGet, "/test", nil, http.StatusOK, nil)
		require.ErrorIs(t, err, ErrInvalidResponse)
	})

	t.Run("oversized request", func(t *testing.T) {
		t.Parallel()
		client := newClientForTest(func(
			_ context.Context,
			_ localgateway.Request,
		) (localgateway.Response, error) {
			t.Fatal("oversized request reached transport")
			return localgateway.Response{}, nil
		})
		err := client.do(
			context.Background(), http.MethodPost, "/test",
			strings.Repeat("x", clientMaxRequestBodyBytes+1), http.StatusOK, &struct{}{},
		)
		require.ErrorIs(t, err, ErrInvalidRequest)
	})

	t.Run("empty and oversized API envelopes", func(t *testing.T) {
		t.Parallel()
		for _, raw := range [][]byte{nil, make([]byte, clientMaxResponseBodyBytes+1)} {
			err := decodeAPIError(http.StatusBadGateway, raw, true)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, "invalid_response", apiErr.Code)
			assert.True(t, RequestMayHaveBeenSent(err))
		}
	})

	t.Run("dispatch marker helpers", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, markDispatched(nil, true))
		original := errors.Join(localgateway.ErrRequestMayHaveBeenSent, errors.New("sent"))
		assert.Same(t, original, markDispatched(original, true))
		marked := markDispatched(ErrInvalidResponse, true)
		assert.Equal(t, ErrInvalidResponse.Error(), marked.Error())
		require.ErrorIs(t, marked, ErrInvalidResponse)
		assert.True(t, RequestMayHaveBeenSent(marked))
	})
}
