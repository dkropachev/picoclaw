package database

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestServedStoreCatalogValidationIsExactSortedAndDetached(t *testing.T) {
	t.Parallel()

	if validated, domains, err := validateServedStores(nil); err != nil || validated != nil || len(domains) != 0 {
		t.Fatalf("nil served-store catalog = %#v, %#v, %v", validated, domains, err)
	}

	input := []StoreBinding{
		{ID: "workspace/repository-reviews", Domain: "repository-reviews"},
		{ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory"},
		{ID: "workspace/aaaaaaaaaaaaaaaa/repository-reviews", Domain: "repository-reviews"},
	}
	validated, domains, err := validateServedStores(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []StoreBinding{
		{ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory"},
		{ID: "workspace/aaaaaaaaaaaaaaaa/repository-reviews", Domain: "repository-reviews"},
		{ID: "workspace/repository-reviews", Domain: "repository-reviews"},
	}
	if !reflect.DeepEqual(validated, want) {
		t.Fatalf("validated served stores = %#v, want %#v", validated, want)
	}
	input[0] = StoreBinding{ID: "global/changed", Domain: "changed"}
	if !reflect.DeepEqual(validated, want) ||
		domains["workspace/repository-reviews"] != "repository-reviews" {
		t.Fatalf("served stores retained caller mutation: %#v / %#v", validated, domains)
	}

	for _, test := range []struct {
		name     string
		bindings []StoreBinding
		code     ErrorCode
	}{
		{
			name: "invalid ID", bindings: []StoreBinding{{ID: "bad id", Domain: "reviews"}},
			code: CodeInvalid,
		},
		{
			name: "empty domain", bindings: []StoreBinding{{ID: "workspace/reviews"}},
			code: CodeInvalid,
		},
		{
			name:     "noncanonical domain",
			bindings: []StoreBinding{{ID: "workspace/reviews", Domain: "Repository-Reviews"}},
			code:     CodeInvalid,
		},
		{
			name:     "control domain",
			bindings: []StoreBinding{{ID: "workspace/reviews", Domain: ControlDomain}},
			code:     CodeInvalid,
		},
		{
			name: "duplicate same domain",
			bindings: []StoreBinding{
				{ID: "workspace/reviews", Domain: "reviews"},
				{ID: "workspace/reviews", Domain: "reviews"},
			},
			code: CodeIntegrity,
		},
		{
			name: "duplicate different domain",
			bindings: []StoreBinding{
				{ID: "workspace/reviews", Domain: "reviews"},
				{ID: "workspace/reviews", Domain: "evaluations"},
			},
			code: CodeIntegrity,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			validated, domains, err := validateServedStores(test.bindings)
			if validated != nil || domains != nil || CodeOf(err) != test.code {
				t.Fatalf("validateServedStores() = %#v, %#v, %v; want %s", validated, domains, err, test.code)
			}
		})
	}
}

func TestRequiredStoresMustBelongToExplicitScope(t *testing.T) {
	t.Parallel()

	served := map[StoreID]string{
		"workspace/optional": "reviews",
		"workspace/required": "reviews",
	}
	if err := requiredStoresBelongToScope(
		[]StoreID{"workspace/required"},
		served,
	); err != nil {
		t.Fatal(err)
	}
	if err := requiredStoresBelongToScope(nil, served); err != nil {
		t.Fatal(err)
	}
	if err := requiredStoresBelongToScope(
		[]StoreID{"workspace/omitted"},
		served,
	); CodeOf(err) != CodeIntegrity {
		t.Fatalf("out-of-scope required store error = %v", err)
	}
}

func TestStartServerRetainsDetachedServedScope(t *testing.T) {
	bindings := []StoreBinding{
		{ID: "workspace/optional", Domain: "reviews"},
		{ID: "workspace/required", Domain: "reviews"},
	}
	required := []StoreID{"workspace/required"}
	server, err := StartServer(t.Context(), ServerOptions{
		Home:           t.TempDir(),
		RequiredStores: required,
		ServedStores:   bindings,
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			return []StoreStatus{
				{ID: "workspace/required", Readiness: StoreReady},
				{
					ID: "workspace/optional", Readiness: StoreUnavailable,
					Error: NewError(CodeUnavailable, "database store is unavailable"),
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer(t, server) })

	bindings[0] = StoreBinding{ID: "global/changed", Domain: "changed"}
	required[0] = "global/changed"
	if !server.servedScope || len(server.servedStores) != 2 ||
		server.servedStores[0].ID != "workspace/optional" ||
		server.servedStoreDomains["workspace/optional"] != "reviews" ||
		len(server.requiredStores) != 1 || server.requiredStores[0] != "workspace/required" {
		t.Fatalf(
			"server retained mutable scope: scoped=%t stores=%#v domains=%#v required=%#v",
			server.servedScope,
			server.servedStores,
			server.servedStoreDomains,
			server.requiredStores,
		)
	}

	result, shutdown, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: ControlVersion,
		Operation: ControlOperationStatus, Payload: []byte(`{}`),
	})
	if err != nil || shutdown {
		t.Fatalf("scoped status = %#v, %t, %v", result, shutdown, err)
	}
	status := result.(BrokerStatus)
	if len(status.Stores) != 2 || status.Stores[0].ID != "workspace/optional" ||
		status.Stores[1].ID != "workspace/required" || len(status.RequiredStores) != 1 ||
		status.RequiredStores[0] != "workspace/required" {
		t.Fatalf("scoped status = %#v", status)
	}
}

func TestStartServerRejectsInvalidServedScopeBeforePublication(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		required []StoreID
		served   []StoreBinding
		code     ErrorCode
	}{
		{
			name: "invalid binding", served: []StoreBinding{{ID: "bad id", Domain: "reviews"}},
			code: CodeInvalid,
		},
		{
			name: "duplicate binding",
			served: []StoreBinding{
				{ID: "workspace/reviews", Domain: "reviews"},
				{ID: "workspace/reviews", Domain: "reviews"},
			},
			code: CodeIntegrity,
		},
		{
			name: "required outside scope", required: []StoreID{"workspace/evaluations"},
			served: []StoreBinding{{ID: "workspace/reviews", Domain: "reviews"}},
			code:   CodeIntegrity,
		},
		{
			name: "required inside explicit empty scope", required: []StoreID{"workspace/reviews"},
			served: []StoreBinding{}, code: CodeIntegrity,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, err := StartServer(t.Context(), ServerOptions{
				Home: t.TempDir(), RequiredStores: test.required, ServedStores: test.served,
			})
			if server != nil || CodeOf(err) != test.code {
				t.Fatalf("StartServer() = %#v, %v; want nil/%s", server, err, test.code)
			}
		})
	}
}

func TestScopedDispatchRejectsBeforeIdempotencyAndHandler(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	trace := make([]string, 0, 3)
	var handled Request
	server := &Server{
		manifest: Manifest{Token: "token", Epoch: "epoch"},
		now: func() time.Time {
			trace = append(trace, "deadline")
			return now
		},
		servedScope: true,
		servedStores: []StoreBinding{
			{ID: "workspace/reviews", Domain: "reviews"},
		},
		servedStoreDomains: map[StoreID]string{"workspace/reviews": "reviews"},
		idempotency:        newIdempotencyRegistry(),
		allowsIdempotency: func(string, int, string) bool {
			trace = append(trace, "idempotency-policy")
			return true
		},
		handler: HandlerFunc(func(_ context.Context, request Request) (any, error) {
			trace = append(trace, "handler")
			handled = request
			return EmptyPayload{}, nil
		}),
		ctx: context.Background(),
	}
	valid := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "request", Token: "token", BrokerEpoch: "epoch",
		StoreID: "workspace/reviews", Domain: "reviews", DomainVersion: 1,
		Operation: "read", DeadlineUnixNs: now.Add(time.Minute).UnixNano(),
		IdempotencyKey: "stable-key", Payload: []byte(`{}`),
	}

	for _, test := range []struct {
		name      string
		mutate    func(*RequestEnvelope)
		code      ErrorCode
		wantTrace []string
	}{
		{
			name: "authentication precedes validation", code: CodeUnauthorized,
			mutate: func(envelope *RequestEnvelope) { envelope.Token = "wrong" },
		},
		{
			name: "protocol precedes deadline", code: CodeUnsupported,
			mutate: func(envelope *RequestEnvelope) { envelope.Protocol++ },
		},
		{
			name: "invalid StoreID precedes deadline", code: CodeInvalid,
			mutate: func(envelope *RequestEnvelope) { envelope.StoreID = "bad id" },
		},
		{
			name: "deadline precedes scope", code: CodeDeadline, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) { envelope.DeadlineUnixNs = now.UnixNano() },
		},
		{
			name: "omitted StoreID", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) { envelope.StoreID = "" },
		},
		{
			name: "unserved StoreID", code: CodeUnsupported, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) { envelope.StoreID = "workspace/evaluations" },
		},
		{
			name: "wrong StoreID domain", code: CodeUnsupported, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) { envelope.Domain = "evaluations" },
		},
		{
			name: "payload StoreID", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Payload = []byte(`{"store_id":"workspace/evaluations"}`)
			},
		},
		{
			name: "default field StoreID", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Payload = []byte(`{"StoreID":"workspace/evaluations"}`)
			},
		},
		{
			name: "uppercase StoreID", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Payload = []byte(`{"STORE_ID":"workspace/evaluations"}`)
			},
		},
		{
			name: "workspace selector", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Payload = []byte(`{"workspace_selector":"0123456789abcdef"}`)
			},
		},
		{
			name: "default workspace selector", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Payload = []byte(`{"WorkspaceSelector":"0123456789abcdef"}`)
			},
		},
		{
			name: "nested StoreID", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Payload = []byte(`{"nested":[{"store-id":"workspace/evaluations"}]}`)
			},
		},
		{
			name: "resolver operation", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Operation = scopedStoreResolverOperation
			},
		},
		{
			name: "resolver operation alias", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) {
				envelope.Operation = "resolve_store"
			},
		},
		{
			name: "malformed scoped payload", code: CodeInvalid, wantTrace: []string{"deadline"},
			mutate: func(envelope *RequestEnvelope) { envelope.Payload = []byte(`{broken}`) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace = trace[:0]
			candidate := valid
			test.mutate(&candidate)
			response, shutdown := server.dispatch(candidate)
			if shutdown || CodeOf(response.Error) != test.code ||
				!slices.Equal(trace, test.wantTrace) {
				t.Fatalf(
					"dispatch() = %#v, %t, trace %#v; want %s/%#v",
					response,
					shutdown,
					trace,
					test.code,
					test.wantTrace,
				)
			}
			server.idempotency.mu.Lock()
			records := len(server.idempotency.records)
			server.idempotency.mu.Unlock()
			if records != 0 || !handled.StoreID.IsZero() {
				t.Fatalf("rejected request mutated dispatch state: records=%d handled=%#v", records, handled)
			}
		})
	}

	trace = trace[:0]
	response, shutdown := server.dispatch(valid)
	if response.Error != nil || shutdown ||
		!slices.Equal(trace, []string{"deadline", "idempotency-policy", "handler"}) ||
		handled.StoreID != valid.StoreID || handled.Domain != valid.Domain {
		t.Fatalf("allowed dispatch = %#v, %t, trace %#v, request %#v", response, shutdown, trace, handled)
	}
	server.idempotency.mu.Lock()
	records := len(server.idempotency.records)
	server.idempotency.mu.Unlock()
	if records != 1 {
		t.Fatalf("allowed idempotency records = %d, want 1", records)
	}
}

func TestScopedControlAndLegacyUnscopedDispatch(t *testing.T) {
	t.Parallel()

	now := time.Unix(2_000_000_000, 0)
	newServer := func(scoped bool) (*Server, *int) {
		handlerCalls := 0
		server := &Server{
			manifest:  Manifest{PID: 42, Token: "token", Epoch: "epoch"},
			startedAt: now, now: func() time.Time { return now },
			servedScope: scoped, servedStoreDomains: map[StoreID]string{},
			idempotency: newIdempotencyRegistry(), ctx: context.Background(),
			handler: HandlerFunc(func(context.Context, Request) (any, error) {
				handlerCalls++
				return EmptyPayload{}, nil
			}),
		}
		return server, &handlerCalls
	}
	envelope := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "request", Token: "token", BrokerEpoch: "epoch",
		Domain: "reviews", DomainVersion: 1, Operation: "read",
		DeadlineUnixNs: now.Add(time.Minute).UnixNano(), Payload: []byte(`{}`),
	}

	scoped, scopedCalls := newServer(true)
	response, _ := scoped.dispatch(envelope)
	if CodeOf(response.Error) != CodeInvalid || *scopedCalls != 0 {
		t.Fatalf("explicit empty scope dispatch = %#v, calls=%d", response, *scopedCalls)
	}
	ping := envelope
	ping.Domain = ControlDomain
	ping.DomainVersion = ControlVersion
	ping.Operation = ControlOperationPing
	response, shutdown := scoped.dispatch(ping)
	if response.Error != nil || shutdown || *scopedCalls != 0 {
		t.Fatalf("scoped control ping = %#v, %t, calls=%d", response, shutdown, *scopedCalls)
	}
	ping.StoreID = "workspace/reviews"
	response, _ = scoped.dispatch(ping)
	if CodeOf(response.Error) != CodeInvalid || *scopedCalls != 0 {
		t.Fatalf("store-bound control ping = %#v, calls=%d", response, *scopedCalls)
	}

	unscoped, unscopedCalls := newServer(false)
	response, shutdown = unscoped.dispatch(envelope)
	if response.Error != nil || shutdown || *unscopedCalls != 1 {
		t.Fatalf("legacy unscoped dispatch = %#v, %t, calls=%d", response, shutdown, *unscopedCalls)
	}
}

func TestScopedStatusRequiresEveryAndOnlyServedStore(t *testing.T) {
	served := []StoreBinding{
		{ID: "workspace/evaluations", Domain: "evaluations"},
		{ID: "workspace/reviews", Domain: "reviews"},
	}
	server := &Server{
		manifest: Manifest{PID: 42, Epoch: "epoch"}, startedAt: time.Unix(2_000_000_000, 0),
		requiredStores: []StoreID{"workspace/reviews"},
		servedStores:   served, servedScope: true,
	}
	request := Request{
		Domain: ControlDomain, Version: ControlVersion,
		Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}
	for _, test := range []struct {
		name     string
		statuses []StoreStatus
		wantOK   bool
	}{
		{
			name: "exact unsorted",
			statuses: []StoreStatus{
				{ID: "workspace/reviews", Readiness: StoreReady},
				{
					ID: "workspace/evaluations", Readiness: StoreUnavailable,
					Error: NewError(CodeUnavailable, "database store is unavailable"),
				},
			},
			wantOK: true,
		},
		{
			name:     "missing optional",
			statuses: []StoreStatus{{ID: "workspace/reviews", Readiness: StoreReady}},
		},
		{
			name:     "missing required",
			statuses: []StoreStatus{{ID: "workspace/evaluations", Readiness: StoreReady}},
		},
		{
			name: "extra",
			statuses: []StoreStatus{
				{ID: "workspace/evaluations", Readiness: StoreReady},
				{ID: "workspace/reviews", Readiness: StoreReady},
				{ID: "workspace/other", Readiness: StoreReady},
			},
		},
		{
			name: "same length substitution",
			statuses: []StoreStatus{
				{ID: "workspace/evaluations", Readiness: StoreReady},
				{ID: "workspace/other", Readiness: StoreReady},
			},
		},
		{
			name: "duplicate",
			statuses: []StoreStatus{
				{ID: "workspace/reviews", Readiness: StoreReady},
				{ID: "workspace/reviews", Readiness: StoreReady},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server.statusProvider = func(context.Context) ([]StoreStatus, error) {
				return append([]StoreStatus(nil), test.statuses...), nil
			}
			result, shutdown, err := server.handle(t.Context(), request)
			if test.wantOK {
				if err != nil || shutdown {
					t.Fatalf("status = %#v, %t, %v", result, shutdown, err)
				}
				status := result.(BrokerStatus)
				if len(status.Stores) != 2 || status.Stores[0].ID != served[0].ID ||
					status.Stores[1].ID != served[1].ID {
					t.Fatalf("sorted exact scoped status = %#v", status)
				}
				return
			}
			if result != nil || shutdown || CodeOf(err) != CodeIntegrity {
				t.Fatalf("invalid scoped status = %#v, %t, %v", result, shutdown, err)
			}
		})
	}
}

func TestExplicitEmptyScopeRequiresEmptyStatusWhileUnscopedRemainsCompatible(t *testing.T) {
	t.Parallel()

	request := Request{
		Domain: ControlDomain, Version: ControlVersion,
		Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}
	server := &Server{manifest: Manifest{PID: 42, Epoch: "epoch"}, startedAt: time.Now(), servedScope: true}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) { return nil, nil }
	if _, _, err := server.handle(t.Context(), request); err != nil {
		t.Fatalf("empty scoped status = %v", err)
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return []StoreStatus{{ID: "workspace/reviews", Readiness: StoreReady}}, nil
	}
	if _, _, err := server.handle(t.Context(), request); CodeOf(err) != CodeIntegrity {
		t.Fatalf("extra empty-scope status = %v", err)
	}

	server.servedScope = false
	if result, _, err := server.handle(t.Context(), request); err != nil ||
		len(result.(BrokerStatus).Stores) != 1 {
		t.Fatalf("legacy unscoped status = %#v, %v", result, err)
	}
}
