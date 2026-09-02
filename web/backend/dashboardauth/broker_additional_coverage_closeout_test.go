package dashboardauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func dashboardAuthRequest(t *testing.T, operation string, payload any) database.Request {
	t.Helper()
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return database.Request{
		Domain: launcherAuthDomain, Version: launcherAuthVersion,
		Operation: operation, Payload: raw,
	}
}

func TestCoverageLauncherAuthBrokerValidationAndLifecycle(t *testing.T) {
	if store, err := NewBroker(nil); err == nil || store != nil {
		t.Fatalf("nil broker store = %#v, %v", store, err)
	}
	if err := (*Store)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	if (*Store)(nil).StoreID() != launcherAuthStoreID {
		t.Fatal("nil store did not retain logical StoreID")
	}
	if initialized, err := (*Store)(nil).IsInitialized(t.Context()); err == nil || initialized {
		t.Fatalf("nil store initialized = %v, %v", initialized, err)
	}
	if err := (*Store)(nil).SetPassword(t.Context(), "password"); err == nil {
		t.Fatal("nil store set password")
	}
	if verified, err := (*Store)(nil).VerifyPassword(t.Context(), "password"); err == nil || verified {
		t.Fatalf("nil store verified = %v, %v", verified, err)
	}
	if err := (&Store{}).SetPassword(t.Context(), ""); err == nil {
		t.Fatal("empty password accepted")
	}

	if _, err := (*BrokerHandler)(
		nil,
	).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler = %v", err)
	}
	handler := NewBrokerHandler(t.TempDir(), filepath.Join(t.TempDir(), "launcher-config.json"))
	for _, request := range []database.Request{
		{Domain: "other", Version: launcherAuthVersion},
		{Domain: launcherAuthDomain, Version: launcherAuthVersion + 1},
		{Domain: launcherAuthDomain, Version: launcherAuthVersion, Operation: "unknown", Payload: []byte(`{}`)},
		dashboardAuthRequest(t, launcherAuthOperationInitialized, launcherAuthEmptyRequest{StoreID: "global/auth"}),
		dashboardAuthRequest(t, launcherAuthOperationSetPassword,
			launcherAuthPasswordRequest{StoreID: launcherAuthStoreID}),
		dashboardAuthRequest(t, launcherAuthOperationVerifyPassword,
			launcherAuthPasswordRequest{StoreID: "global/auth", Password: "password"}),
	} {
		if _, err := handler.Handle(t.Context(), request); err == nil {
			t.Fatalf("invalid launcher-auth request succeeded: %#v", request)
		}
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (*BrokerHandler)(nil).open(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil handler open = %v", err)
	}
}

func TestCoverageLauncherAuthAuthorityMigrationAndErrorMapping(t *testing.T) {
	restore := database.SuspendProviderTestAuthority()
	handler := NewBrokerHandler(t.TempDir(), filepath.Join(t.TempDir(), "launcher-config.json"))
	restore()
	if _, err := handler.open(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unauthorized handler open = %v", err)
	}
	if err := RunOfflineDatabaseMigration(
		t.TempDir(),
		"launcher-config.json",
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("unfenced migration = %v", err)
	}
	for _, test := range []struct {
		err  error
		code database.ErrorCode
	}{
		{err: nil, code: ""},
		{err: context.Canceled, code: database.CodeDeadline},
		{err: context.DeadlineExceeded, code: database.CodeDeadline},
		{err: errors.New("plain"), code: database.CodeInternal},
	} {
		if mapped := mapLauncherAuthError(test.err); database.CodeOf(mapped) != test.code {
			t.Fatalf("mapped %v = %v, want %s", test.err, mapped, test.code)
		}
	}
}
