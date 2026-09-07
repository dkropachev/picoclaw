package matrixstore_test

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/database"
)

type faultMatrixHandler struct {
	groupSession matrixstore.InboundGroupSessionRecord
}

const faultSnapshotID matrixstore.GroupSessionSnapshotID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (handler *faultMatrixHandler) Handle(_ context.Context, request database.Request) (any, error) {
	switch request.Operation {
	case matrixstore.ResolveOperation:
		var input matrixstore.ResolveRequest
		_ = request.DecodePayload(&input)
		if input.ChannelName == "invalid-response" {
			return matrixstore.ResolveResponse{StoreID: "not valid"}, nil
		}
		return matrixstore.ResolveResponse{StoreID: "channel/matrix/fake"}, nil
	case matrixstore.PreflightOperation:
		return matrixstore.PreflightResponse{Ready: false}, nil
	case "crypto.get-all-group-sessions":
		var input matrixstore.Request
		_ = request.DecodePayload(&input)
		if input.Cursor == 0 && input.SnapshotID == "" {
			return matrixstore.Response{
				GroupSessions: []matrixstore.InboundGroupSessionRecord{handler.groupSession},
				SnapshotID:    faultSnapshotID,
				NextCursor:    1,
				More:          true,
			}, nil
		}
		if input.Cursor != 1 || input.SnapshotID != faultSnapshotID {
			return nil, database.NewError(database.CodeInvalid, "invalid snapshot continuation")
		}
		return matrixstore.Response{
			GroupSessions: []matrixstore.InboundGroupSessionRecord{handler.groupSession},
			NextCursor:    2,
		}, nil
	case "crypto.get-group-sessions-for-room":
		return matrixstore.Response{
			GroupSessions: []matrixstore.InboundGroupSessionRecord{handler.groupSession},
			SnapshotID:    "malformed",
			More:          true,
			NextCursor:    1,
		}, nil
	case "state.get-create":
		return matrixstore.Response{Create: &event.Event{
			Type:    event.StateCreate,
			Content: event.Content{VeryRaw: []byte(`{"broken"`)},
		}}, nil
	default:
		return nil, database.NewError(database.CodeUnavailable, "injected Matrix broker failure")
	}
}

func TestClientAndCodecFailurePaths(t *testing.T) {
	key := []byte("pickle-key")
	if !faultSnapshotID.Valid() {
		t.Fatal("canonical snapshot ID was rejected")
	}
	for _, snapshotID := range []matrixstore.GroupSessionSnapshotID{
		"", "short", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if snapshotID.Valid() {
			t.Fatalf("malformed snapshot ID %q was accepted", snapshotID)
		}
	}
	if record, err := matrixstore.EncodeAccount(nil, key); err != nil || record != nil {
		t.Fatalf("EncodeAccount(nil) = %#v, %v", record, err)
	}
	if record, err := matrixstore.EncodeAccount(&crypto.OlmAccount{}, key); err != nil || record != nil {
		t.Fatalf("EncodeAccount(blank) = %#v, %v", record, err)
	}
	if account, err := matrixstore.DecodeAccount(nil, key); err != nil || account != nil {
		t.Fatalf("DecodeAccount(nil) = %#v, %v", account, err)
	}
	if _, err := matrixstore.DecodeAccount(&matrixstore.AccountRecord{Pickle: []byte("bad")}, key); err == nil {
		t.Fatal("DecodeAccount(bad) unexpectedly succeeded")
	}
	if record, err := matrixstore.EncodeOlmSession(nil, key); err != nil || record != nil {
		t.Fatalf("EncodeOlmSession(nil) = %#v, %v", record, err)
	}
	if record, err := matrixstore.EncodeOlmSession(&crypto.OlmSession{}, key); err != nil || record != nil {
		t.Fatalf("EncodeOlmSession(blank) = %#v, %v", record, err)
	}
	if session, err := matrixstore.DecodeOlmSession(nil, key); err != nil || session != nil {
		t.Fatalf("DecodeOlmSession(nil) = %#v, %v", session, err)
	}
	if _, err := matrixstore.DecodeOlmSession(&matrixstore.OlmSessionRecord{Pickle: []byte("bad")}, key); err == nil {
		t.Fatal("DecodeOlmSession(bad) unexpectedly succeeded")
	}
	if record, err := matrixstore.EncodeInboundGroupSession(nil, key); err != nil || record != nil {
		t.Fatalf("EncodeInboundGroupSession(nil) = %#v, %v", record, err)
	}
	if record, err := matrixstore.EncodeInboundGroupSession(
		&crypto.InboundGroupSession{}, key,
	); err != nil || record != nil {
		t.Fatalf("EncodeInboundGroupSession(blank) = %#v, %v", record, err)
	}
	if session, err := matrixstore.DecodeInboundGroupSession(nil, key); err != nil || session != nil {
		t.Fatalf("DecodeInboundGroupSession(nil) = %#v, %v", session, err)
	}
	if _, err := matrixstore.DecodeInboundGroupSession(
		&matrixstore.InboundGroupSessionRecord{Pickle: []byte("bad")}, key,
	); err == nil {
		t.Fatal("DecodeInboundGroupSession(bad) unexpectedly succeeded")
	}
	if record, err := matrixstore.EncodeOutboundGroupSession(nil, key); err != nil || record != nil {
		t.Fatalf("EncodeOutboundGroupSession(nil) = %#v, %v", record, err)
	}
	if record, err := matrixstore.EncodeOutboundGroupSession(
		&crypto.OutboundGroupSession{}, key,
	); err != nil || record != nil {
		t.Fatalf("EncodeOutboundGroupSession(blank) = %#v, %v", record, err)
	}
	if session, err := matrixstore.DecodeOutboundGroupSession(nil, key); err != nil || session != nil {
		t.Fatalf("DecodeOutboundGroupSession(nil) = %#v, %v", session, err)
	}
	if _, err := matrixstore.DecodeOutboundGroupSession(
		&matrixstore.OutboundGroupSessionRecord{Pickle: []byte("bad")}, key,
	); err == nil {
		t.Fatal("DecodeOutboundGroupSession(bad) unexpectedly succeeded")
	}

	if _, err := matrixstore.New(
		nil,
		"channel/matrix/fake",
		"D",
		key,
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("New(nil) error = %v", err)
	}
	if _, err := matrixstore.ResolveStore(t.Context(), nil, "name"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("ResolveStore(nil) error = %v", err)
	}
	if _, err := matrixstore.ResolveStore(
		t.Context(),
		&database.Client{},
		" bad ",
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("ResolveStore(invalid) error = %v", err)
	}
	if err := matrixstore.Preflight(t.Context(), nil, "bad"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("Preflight(invalid) error = %v", err)
	}
	var nilStore *matrixstore.Store
	if err := nilStore.Flush(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil Store.Flush() error = %v", err)
	}
}

func TestClientBrokerFailureAndPaginationPaths(t *testing.T) {
	roomID := id.RoomID("!page:matrix.test")
	inbound := newInboundGroupSession(t, roomID, "sender", time.Hour, "backup")
	record, err := matrixstore.EncodeInboundGroupSession(inbound, []byte(testPickleKey))
	if err != nil {
		t.Fatal(err)
	}
	handler := &faultMatrixHandler{groupSession: *record}
	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := server.Close(ctx); closeErr != nil {
			t.Errorf("close fake Matrix broker: %v", closeErr)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = matrixstore.ResolveStore(
		t.Context(),
		client,
		"invalid-response",
	); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("invalid ResolveStore response error = %v", err)
	}
	storeID, err := matrixstore.ResolveStore(t.Context(), client, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err = matrixstore.Preflight(t.Context(), client, storeID); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("false Preflight response error = %v", err)
	}
	store, err := matrixstore.New(client, storeID, "D", []byte(testPickleKey))
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.GetAllGroupSessions(t.Context()).AsList()
	if err != nil || len(items) != 2 {
		t.Fatalf("paginated GetAllGroupSessions() = %#v, %v", items, err)
	}
	if _, err = store.GetGroupSessionsForRoom(t.Context(), "!bad:matrix.test").AsList(); err == nil {
		t.Fatal("invalid group session page unexpectedly succeeded")
	}
	if _, err = store.GetCreate(t.Context(), roomID); err == nil {
		t.Fatal("invalid create response unexpectedly succeeded")
	}

	// Exercise every client-side read error branch against a typed broker
	// failure. Successful paths are covered by the real SQLite round-trip test.
	_, _ = store.GetAccount(t.Context())
	_, _ = store.GetSessions(t.Context(), "sender")
	_, _ = store.GetLatestSession(t.Context(), "sender")
	_, _ = store.GetNewestSessionCreationTS(t.Context(), "sender")
	_, _ = store.GetOlmHash(t.Context(), [32]byte{})
	_, _ = store.GetGroupSession(t.Context(), roomID, "session")
	_, _ = store.GetWithheldGroupSession(t.Context(), roomID, "session")
	_, _ = store.GetGroupSessionsWithoutKeyBackupVersion(t.Context(), "backup").AsList()
	_, _ = store.GetOutboundGroupSession(t.Context(), roomID)
	_, _ = store.IsOutboundGroupSessionShared(t.Context(), "@user:test", "key", "session")
	_, _ = store.ValidateMessageIndex(t.Context(), "sender", "session", "$event", 1, 1)
	_, _ = store.GetDevices(t.Context(), "@user:test")
	_, _ = store.GetDevice(t.Context(), "@user:test", "D")
	_, _ = store.FindDeviceByKey(t.Context(), "@user:test", "key")
	_, _ = store.FilterTrackedUsers(t.Context(), []id.UserID{"@user:test"})
	_, _ = store.GetOutdatedTrackedUsers(t.Context())
	_, _ = store.GetCrossSigningKeys(t.Context(), "@user:test")
	_, _ = store.IsKeySignedBy(t.Context(), "@user:test", "key", "@signer:test", "signer")
	_, _ = store.DropSignaturesByKey(t.Context(), "@user:test", "key")
	_, _ = store.GetSignaturesForKeyBy(t.Context(), "@user:test", "key", "@signer:test")
	_, _ = store.GetSecret(t.Context(), "secret")
	_, _ = store.LoadFilterID(t.Context(), "@user:test")
	_, _ = store.LoadNextBatch(t.Context(), "@user:test")
	_, _ = store.GetMember(t.Context(), roomID, "@user:test")
	_, _ = store.TryGetMember(t.Context(), roomID, "@user:test")
	_, _ = store.IsConfusableName(t.Context(), roomID, "@user:test", "name")
	_, _ = store.GetPowerLevels(t.Context(), roomID)
	_, _ = store.GetJoinRules(t.Context(), roomID)
	_, _ = store.HasFetchedMembers(t.Context(), roomID)
	_, _ = store.GetAllMembers(t.Context(), roomID)
	_, _ = store.GetEncryptionEvent(t.Context(), roomID)
	_, _ = store.IsEncrypted(t.Context(), roomID)
	_, _ = store.FindSharedRooms(t.Context(), "@user:test")
	_, _ = store.GetRoomJoinedOrInvitedMembers(t.Context(), roomID)

	if err = store.PutAccount(t.Context(), nil); err == nil {
		t.Fatal("PutAccount(nil) unexpectedly succeeded")
	}
	if err = store.AddSession(t.Context(), "sender", nil); err == nil {
		t.Fatal("AddSession(nil) unexpectedly succeeded")
	}
	if err = store.UpdateSession(t.Context(), "sender", nil); err == nil {
		t.Fatal("UpdateSession(nil) unexpectedly succeeded")
	}
	if err = store.DeleteSession(t.Context(), "sender", nil); err == nil {
		t.Fatal("DeleteSession(nil) unexpectedly succeeded")
	}
	if err = store.PutGroupSession(t.Context(), nil); err == nil {
		t.Fatal("PutGroupSession(nil) unexpectedly succeeded")
	}
	if err = store.AddOutboundGroupSession(t.Context(), nil); err == nil {
		t.Fatal("AddOutboundGroupSession(nil) unexpectedly succeeded")
	}
	if err = store.UpdateOutboundGroupSession(t.Context(), nil); err == nil {
		t.Fatal("UpdateOutboundGroupSession(nil) unexpectedly succeeded")
	}

	badEvents := []*event.Event{
		nil,
		{Type: event.StateMember},
		{Type: event.StateMember, StateKey: pointer("@user:test"), Content: event.Content{
			VeryRaw: []byte(`{"broken"`),
		}},
	}
	_ = store.ReplaceCachedMembers(t.Context(), roomID, badEvents)
}

func pointer(value string) *string { return &value }
