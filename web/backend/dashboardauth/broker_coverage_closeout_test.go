package dashboardauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

func TestLauncherAuthBrokerHandlerOperationMatrix(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home, filepath.Join(home, launcherconfig.FileName))
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, input any) (any, error) {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return handler.Handle(t.Context(), database.Request{
			Domain: launcherAuthDomain, Version: launcherAuthVersion,
			Operation: operation, Payload: payload,
		})
	}
	value, err := call(launcherAuthOperationInitialized, launcherAuthEmptyRequest{
		StoreID: launcherAuthStoreID,
	})
	if err != nil || value.(launcherAuthInitializedResponse).Initialized {
		t.Fatalf("initial state = %#v, %v", value, err)
	}
	value, err = call(launcherAuthOperationSetPassword, launcherAuthPasswordRequest{
		StoreID: launcherAuthStoreID, Password: "correct horse battery staple",
	})
	if err != nil || !value.(launcherAuthMutationResponse).Updated {
		t.Fatalf("set password = %#v, %v", value, err)
	}
	value, err = call(launcherAuthOperationInitialized, launcherAuthEmptyRequest{
		StoreID: launcherAuthStoreID,
	})
	if err != nil || !value.(launcherAuthInitializedResponse).Initialized {
		t.Fatalf("initialized state = %#v, %v", value, err)
	}
	for password, want := range map[string]bool{
		"correct horse battery staple": true,
		"wrong":                        false,
	} {
		value, err = call(launcherAuthOperationVerifyPassword, launcherAuthPasswordRequest{
			StoreID: launcherAuthStoreID, Password: password,
		})
		if err != nil || value.(launcherAuthVerificationResponse).Verified != want {
			t.Errorf("verify %q = %#v, %v; want %v", password, value, err, want)
		}
	}
	invalid := []struct {
		operation string
		input     any
	}{
		{launcherAuthOperationInitialized, launcherAuthEmptyRequest{StoreID: "global.auth"}},
		{launcherAuthOperationSetPassword, launcherAuthPasswordRequest{StoreID: launcherAuthStoreID}},
		{launcherAuthOperationVerifyPassword, launcherAuthPasswordRequest{StoreID: "global.auth"}},
	}
	for _, test := range invalid {
		if _, err := call(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if _, err := call("unknown", launcherAuthEmptyRequest{StoreID: launcherAuthStoreID}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
}

func TestLauncherAuthBrokerAuthorityAndErrorBoundaries(t *testing.T) {
	restore := database.SuspendProviderTestAuthority()
	home := t.TempDir()
	unauthorized := NewBrokerHandler(home, filepath.Join(home, launcherconfig.FileName))
	restore()
	if _, err := unauthorized.open(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unauthorized open error = %v", err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close error = %v", err)
	}
	if mapLauncherAuthError(nil) != nil {
		t.Fatal("nil launcher error mapped non-nil")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if database.CodeOf(mapLauncherAuthError(err)) != database.CodeDeadline {
			t.Errorf("deadline mapping for %v failed", err)
		}
	}
	if database.CodeOf(mapLauncherAuthError(errors.New("boom"))) != database.CodeInternal {
		t.Fatal("internal launcher error mapping failed")
	}
	if database.CodeOf(RunOfflineDatabaseMigration(t.TempDir(), "")) != database.CodeConflict {
		t.Fatal("unfenced launcher migration accepted")
	}
}
