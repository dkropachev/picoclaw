package sqliteadapter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	testUserID    = id.UserID("@bot:matrix.test")
	testDeviceID  = id.DeviceID("DEVICE")
	testPickleKey = "test-pickle-key"
)

type matrixHarness struct {
	home    string
	client  *database.Client
	store   *matrixstore.Store
	storeID database.StoreID
	handler *BrokerHandler
}

func newMatrixHarness(t *testing.T) *matrixHarness {
	t.Helper()
	home := t.TempDir()
	storeRoot := filepath.Join(home, "matrix-data")
	channel := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := channel.Decode(&config.MatrixSettings{
		Homeserver:         "https://matrix.test",
		UserID:             testUserID.String(),
		DeviceID:           testDeviceID.String(),
		CryptoDatabasePath: storeRoot,
		CryptoPassphrase:   testPickleKey,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: home}},
		Channels: config.ChannelsConfig{"secure": channel},
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateDatabase(t.Context(), filepath.Join(storeRoot, "store.db")); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if err = fence.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := server.Close(ctx); closeErr != nil {
			t.Errorf("close Matrix broker: %v", closeErr)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	storeID, err := matrixstore.ResolveStore(t.Context(), client, "secure")
	if err != nil {
		t.Fatal(err)
	}
	store, err := matrixstore.New(client, storeID, testDeviceID, []byte(testPickleKey))
	if err != nil {
		t.Fatal(err)
	}
	return &matrixHarness{home: home, client: client, store: store, storeID: storeID, handler: handler}
}

func TestTypedStoreCryptoRoundTrip(t *testing.T) {
	harness := newMatrixHarness(t)
	store := harness.store
	ctx := t.Context()
	if err := matrixstore.Preflight(ctx, harness.client, harness.storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := matrixstore.ResolveStore(
		ctx, harness.client, "missing",
	); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("uncataloged resolve error = %v", err)
	}

	account := crypto.NewOlmAccount()
	if err := store.PutAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	loadedAccount, err := store.GetAccount(ctx)
	if err != nil || loadedAccount == nil || loadedAccount.IdentityKey() != account.IdentityKey() {
		t.Fatalf("GetAccount() = %#v, %v", loadedAccount, err)
	}
	if err = store.SaveNextBatch(ctx, testUserID, "batch-one"); err != nil {
		t.Fatal(err)
	}
	if batch, loadErr := store.LoadNextBatch(ctx, testUserID); loadErr != nil || batch != "batch-one" {
		t.Fatalf("initial LoadNextBatch() = %q, %v", batch, loadErr)
	}
	account.Shared = true
	if err = store.PutAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if batch, loadErr := store.LoadNextBatch(ctx, testUserID); loadErr != nil || batch != "batch-one" {
		t.Fatalf("LoadNextBatch() = %q, %v", batch, loadErr)
	}
	if err = store.SaveFilterID(ctx, testUserID, "filter"); err != nil {
		t.Fatal(err)
	}
	if filter, loadErr := store.LoadFilterID(ctx, testUserID); loadErr != nil || filter != "" {
		t.Fatalf("LoadFilterID() = %q, %v", filter, loadErr)
	}

	senderKey := id.SenderKey("sender-key")
	session := newOlmSession(t)
	if err = store.AddSession(ctx, senderKey, session); err != nil {
		t.Fatal(err)
	}
	if !store.HasSession(ctx, senderKey) {
		t.Fatal("HasSession() = false")
	}
	sessions, err := store.GetSessions(ctx, senderKey)
	if err != nil || len(sessions) != 1 || sessions[0].ID() != session.ID() {
		t.Fatalf("GetSessions() = %#v, %v", sessions, err)
	}
	latest, err := store.GetLatestSession(ctx, senderKey)
	if err != nil || latest == nil || latest.ID() != session.ID() {
		t.Fatalf("GetLatestSession() = %#v, %v", latest, err)
	}
	if created, creationErr := store.GetNewestSessionCreationTS(
		ctx, senderKey,
	); creationErr != nil || created.IsZero() {
		t.Fatalf("GetNewestSessionCreationTS() = %v, %v", created, creationErr)
	}
	session.LastDecryptedTime = session.LastDecryptedTime.Add(time.Minute)
	if err = store.UpdateSession(ctx, senderKey, session); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetSessions(ctx, senderKey)
	if err != nil || len(updated) != 1 || !updated[0].LastDecryptedTime.Equal(session.LastDecryptedTime) {
		t.Fatalf("updated GetSessions() = %#v, %v", updated, err)
	}
	if err = store.DeleteSession(ctx, senderKey, session); err != nil {
		t.Fatal(err)
	}
	if store.HasSession(ctx, senderKey) {
		t.Fatal("deleted session remains")
	}

	var hash [32]byte
	copy(hash[:], "a deterministic olm hash")
	receivedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err = store.PutOlmHash(ctx, hash, receivedAt); err != nil {
		t.Fatal(err)
	}
	if got, hashErr := store.GetOlmHash(ctx, hash); hashErr != nil || !got.Equal(receivedAt) {
		t.Fatalf("GetOlmHash() = %v, %v", got, hashErr)
	}
	if err = store.DeleteOldOlmHashes(ctx, receivedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got, hashErr := store.GetOlmHash(ctx, hash); hashErr != nil || !got.IsZero() {
		t.Fatalf("deleted GetOlmHash() = %v, %v", got, hashErr)
	}

	roomID := id.RoomID("!crypto:matrix.test")
	inbound := newInboundGroupSession(t, roomID, senderKey, 24*time.Hour, "backup-one")
	if err = store.PutGroupSession(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	loadedInbound, err := store.GetGroupSession(ctx, roomID, inbound.ID())
	if err != nil || loadedInbound == nil || loadedInbound.ID() != inbound.ID() {
		t.Fatalf("GetGroupSession() = %#v, %v", loadedInbound, err)
	}
	assertGroupSessionIter(t, store.GetGroupSessionsForRoom(ctx, roomID), inbound.ID())
	assertGroupSessionIter(t, store.GetAllGroupSessions(ctx), inbound.ID())
	assertGroupSessionIter(
		t, store.GetGroupSessionsWithoutKeyBackupVersion(ctx, "another-backup"), inbound.ID(),
	)

	withheldID := id.SessionID("withheld-session")
	withheld := event.RoomKeyWithheldEventContent{
		RoomID: roomID, Algorithm: id.AlgorithmMegolmV1, SessionID: withheldID,
		SenderKey: senderKey, Code: event.RoomKeyWithheldUnauthorized, Reason: "test",
	}
	if err = store.PutWithheldGroupSession(ctx, withheld); err != nil {
		t.Fatal(err)
	}
	if got, withheldErr := store.GetWithheldGroupSession(
		ctx,
		roomID,
		withheldID,
	); withheldErr != nil || got == nil ||
		got.Code != withheld.Code {
		t.Fatalf("GetWithheldGroupSession() = %#v, %v", got, withheldErr)
	}
	if got, withheldErr := store.GetGroupSession(ctx, roomID, withheldID); got != nil ||
		!errors.Is(withheldErr, crypto.ErrGroupSessionWithheld) {
		t.Fatalf("withheld GetGroupSession() = %#v, %v", got, withheldErr)
	}
	if err = store.RedactGroupSession(ctx, roomID, inbound.ID(), "manual"); err != nil {
		t.Fatal(err)
	}

	bulk := newInboundGroupSession(t, roomID, senderKey, 0, "")
	if err = store.PutGroupSession(ctx, bulk); err != nil {
		t.Fatal(err)
	}
	if ids, redactErr := store.RedactGroupSessions(ctx, roomID, senderKey, "bulk"); redactErr != nil || len(ids) == 0 {
		t.Fatalf("RedactGroupSessions() = %v, %v", ids, redactErr)
	}
	expired := newInboundGroupSession(t, roomID, "expired-sender", time.Millisecond, "")
	expired.ReceivedAt = time.Now().Add(-48 * time.Hour)
	if err = store.PutGroupSession(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if ids, redactErr := store.RedactExpiredGroupSessions(ctx); redactErr != nil {
		t.Fatalf("RedactExpiredGroupSessions() = %v, %v", ids, redactErr)
	}
	outdated := newInboundGroupSession(t, roomID, "outdated-sender", 0, "")
	if err = store.PutGroupSession(ctx, outdated); err != nil {
		t.Fatal(err)
	}
	if ids, redactErr := store.RedactOutdatedGroupSessions(ctx); redactErr != nil {
		t.Fatalf("RedactOutdatedGroupSessions() = %v, %v", ids, redactErr)
	}

	outbound, err := crypto.NewOutboundGroupSession(roomID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AddOutboundGroupSession(ctx, outbound); err != nil {
		t.Fatal(err)
	}
	loadedOutbound, err := store.GetOutboundGroupSession(ctx, roomID)
	if err != nil || loadedOutbound == nil || loadedOutbound.ID() != outbound.ID() {
		t.Fatalf("GetOutboundGroupSession() = %#v, %v", loadedOutbound, err)
	}
	outbound.MessageCount++
	if err = store.UpdateOutboundGroupSession(ctx, outbound); err != nil {
		t.Fatal(err)
	}
	identityKey := id.IdentityKey("curve-key")
	if err = store.MarkOutboundGroupSessionShared(ctx, testUserID, identityKey, outbound.ID()); err != nil {
		t.Fatal(err)
	}
	if shared, sharedErr := store.IsOutboundGroupSessionShared(
		ctx, testUserID, identityKey, outbound.ID(),
	); sharedErr != nil || !shared {
		t.Fatalf("IsOutboundGroupSessionShared() = %t, %v", shared, sharedErr)
	}
	if err = store.RemoveOutboundGroupSession(ctx, roomID); err != nil {
		t.Fatal(err)
	}

	if valid, indexErr := store.ValidateMessageIndex(
		ctx,
		senderKey,
		"index-session",
		"$event",
		1,
		10,
	); indexErr != nil ||
		!valid {
		t.Fatalf("first ValidateMessageIndex() = %t, %v", valid, indexErr)
	}
	if valid, indexErr := store.ValidateMessageIndex(
		ctx,
		senderKey,
		"index-session",
		"$other",
		1,
		11,
	); indexErr != nil ||
		valid {
		t.Fatalf("conflicting ValidateMessageIndex() = %t, %v", valid, indexErr)
	}
	if err = store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTypedStoreIdentityAndStateRoundTrip(t *testing.T) {
	harness := newMatrixHarness(t)
	store := harness.store
	ctx := t.Context()
	if err := store.PutAccount(ctx, crypto.NewOlmAccount()); err != nil {
		t.Fatal(err)
	}

	userID := id.UserID("@alice:matrix.test")
	device := &id.Device{
		UserID: userID, DeviceID: "ALICE", IdentityKey: "alice-curve",
		SigningKey: "alice-ed", Trust: id.TrustStateVerified, Name: "Alice phone",
	}
	if err := store.PutDevice(ctx, userID, device); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetDevice(ctx, userID, device.DeviceID); err != nil || got == nil || got.Name != device.Name {
		t.Fatalf("GetDevice() = %#v, %v", got, err)
	}
	if got, err := store.FindDeviceByKey(ctx, userID, device.IdentityKey); err != nil || got == nil {
		t.Fatalf("FindDeviceByKey() = %#v, %v", got, err)
	}
	if err := store.PutDevices(ctx, userID, map[id.DeviceID]*id.Device{device.DeviceID: device}); err != nil {
		t.Fatal(err)
	}
	if devices, err := store.GetDevices(ctx, userID); err != nil || len(devices) != 1 {
		t.Fatalf("GetDevices() = %#v, %v", devices, err)
	}
	if users, err := store.FilterTrackedUsers(
		ctx,
		[]id.UserID{userID, "@unknown:matrix.test"},
	); err != nil || len(users) != 1 ||
		users[0] != userID {
		t.Fatalf("FilterTrackedUsers() = %v, %v", users, err)
	}
	if err := store.MarkTrackedUsersOutdated(ctx, []id.UserID{userID}); err != nil {
		t.Fatal(err)
	}
	if users, err := store.GetOutdatedTrackedUsers(ctx); err != nil || len(users) != 1 {
		t.Fatalf("GetOutdatedTrackedUsers() = %v, %v", users, err)
	}

	masterKey := id.Ed25519("master-key")
	if err := store.PutCrossSigningKey(ctx, userID, id.XSUsageMaster, masterKey); err != nil {
		t.Fatal(err)
	}
	if keys, err := store.GetCrossSigningKeys(ctx, userID); err != nil || keys[id.XSUsageMaster].Key != masterKey {
		t.Fatalf("GetCrossSigningKeys() = %#v, %v", keys, err)
	}
	signerUser := id.UserID("@signer:matrix.test")
	signerKey := id.Ed25519("signer-key")
	if err := store.PutSignature(ctx, userID, masterKey, signerUser, signerKey, "signature"); err != nil {
		t.Fatal(err)
	}
	if valid, err := store.IsKeySignedBy(ctx, userID, masterKey, signerUser, signerKey); err != nil || !valid {
		t.Fatalf("IsKeySignedBy() = %t, %v", valid, err)
	}
	if signatures, err := store.GetSignaturesForKeyBy(
		ctx,
		userID,
		masterKey,
		signerUser,
	); err != nil ||
		signatures[signerKey] != "signature" {
		t.Fatalf("GetSignaturesForKeyBy() = %#v, %v", signatures, err)
	}
	if count, err := store.DropSignaturesByKey(ctx, signerUser, signerKey); err != nil || count != 1 {
		t.Fatalf("DropSignaturesByKey() = %d, %v", count, err)
	}
	if err := store.PutSecret(ctx, "secret-name", "secret-value"); err != nil {
		t.Fatal(err)
	}
	if value, err := store.GetSecret(ctx, "secret-name"); err != nil || value != "secret-value" {
		t.Fatalf("GetSecret() = %q, %v", value, err)
	}
	if err := store.DeleteSecret(ctx, "secret-name"); err != nil {
		t.Fatal(err)
	}

	roomID := id.RoomID("!state:matrix.test")
	if err := store.SetMembership(ctx, roomID, userID, event.MembershipJoin); err != nil {
		t.Fatal(err)
	}
	if !store.IsInRoom(ctx, roomID, userID) || !store.IsInvited(ctx, roomID, userID) ||
		!store.IsMembership(ctx, roomID, userID, event.MembershipJoin) {
		t.Fatal("membership predicates failed")
	}
	member := &event.MemberEventContent{Membership: event.MembershipJoin, Displayname: "Alice"}
	if err := store.SetMember(ctx, roomID, userID, member); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetMember(ctx, roomID, userID); err != nil || got.Displayname != "Alice" {
		t.Fatalf("GetMember() = %#v, %v", got, err)
	}
	if got, err := store.TryGetMember(ctx, roomID, "@missing:matrix.test"); err != nil || got != nil {
		t.Fatalf("TryGetMember() = %#v, %v", got, err)
	}
	otherUser := id.UserID("@other:matrix.test")
	if err := store.SetMember(ctx, roomID, otherUser, member); err != nil {
		t.Fatal(err)
	}
	if users, err := store.IsConfusableName(ctx, roomID, userID, "Alice"); err != nil || len(users) != 1 {
		t.Fatalf("IsConfusableName() = %v, %v", users, err)
	}

	stateKey := userID.String()
	evt := &event.Event{
		Type: event.StateMember, RoomID: roomID, StateKey: &stateKey,
		Content: event.Content{Parsed: member},
	}
	if err := store.ReplaceCachedMembers(ctx, roomID, []*event.Event{evt}); err != nil {
		t.Fatal(err)
	}
	if fetched, err := store.HasFetchedMembers(ctx, roomID); err != nil || !fetched {
		t.Fatalf("HasFetchedMembers() = %t, %v", fetched, err)
	}
	if members, err := store.GetAllMembers(ctx, roomID); err != nil || len(members) != 1 {
		t.Fatalf("GetAllMembers() = %#v, %v", members, err)
	}
	if users, err := store.GetRoomJoinedOrInvitedMembers(ctx, roomID); err != nil || len(users) != 1 {
		t.Fatalf("GetRoomJoinedOrInvitedMembers() = %v, %v", users, err)
	}
	if err := store.ClearCachedMembers(ctx, roomID, event.MembershipJoin); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkMembersFetched(ctx, roomID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMembership(ctx, roomID, userID, event.MembershipJoin); err != nil {
		t.Fatal(err)
	}

	levels := &event.PowerLevelsEventContent{Users: map[id.UserID]int{testUserID: 100}}
	if err := store.SetPowerLevels(ctx, roomID, levels); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetPowerLevels(ctx, roomID); err != nil || got.Users[testUserID] != 100 {
		t.Fatalf("GetPowerLevels() = %#v, %v", got, err)
	}
	createStateKey := ""
	create := &event.Event{
		Type: event.StateCreate, RoomID: roomID, StateKey: &createStateKey,
		Content: event.Content{Parsed: &event.CreateEventContent{Creator: testUserID}},
	}
	if err := store.SetCreate(ctx, create); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetCreate(ctx, roomID); err != nil || got == nil || got.RoomID != roomID {
		t.Fatalf("GetCreate() = %#v, %v", got, err)
	}
	rules := &event.JoinRulesEventContent{JoinRule: event.JoinRuleInvite}
	if err := store.SetJoinRules(ctx, roomID, rules); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetJoinRules(ctx, roomID); err != nil || got.JoinRule != rules.JoinRule {
		t.Fatalf("GetJoinRules() = %#v, %v", got, err)
	}
	encryption := &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}
	if err := store.SetEncryptionEvent(ctx, roomID, encryption); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetEncryptionEvent(ctx, roomID); err != nil || got.Algorithm != id.AlgorithmMegolmV1 {
		t.Fatalf("GetEncryptionEvent() = %#v, %v", got, err)
	}
	if encrypted, err := store.IsEncrypted(ctx, roomID); err != nil || !encrypted {
		t.Fatalf("IsEncrypted() = %t, %v", encrypted, err)
	}
	if rooms, err := store.FindSharedRooms(ctx, userID); err != nil || len(rooms) != 1 || rooms[0] != roomID {
		t.Fatalf("FindSharedRooms() = %v, %v", rooms, err)
	}
}

func TestTypedStoreAndHandlerRejectInvalidAuthority(t *testing.T) {
	if _, err := matrixstore.New(
		nil,
		"channel/matrix/test",
		testDeviceID,
		[]byte(testPickleKey),
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil client New() error = %v", err)
	}
	restore := database.SuspendProviderTestAuthority()
	defer restore()
	if handler, err := NewBrokerHandler(t.TempDir(), &config.Config{}); handler != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unauthorized NewBrokerHandler() = %#v, %v", handler, err)
	}
}

func newOlmSession(t *testing.T) *crypto.OlmSession {
	t.Helper()
	sender := crypto.NewOlmAccount()
	receiver := crypto.NewOlmAccount()
	if err := receiver.Internal.GenOneTimeKeys(1); err != nil {
		t.Fatal(err)
	}
	keys, err := receiver.Internal.OneTimeKeys()
	if err != nil {
		t.Fatal(err)
	}
	var oneTimeKey id.Curve25519
	for _, key := range keys {
		oneTimeKey = key
		break
	}
	internal, err := sender.Internal.NewOutboundSession(receiver.IdentityKey(), oneTimeKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return &crypto.OlmSession{
		Internal: internal,
		ExpirationMixin: crypto.ExpirationMixin{TimeMixin: crypto.TimeMixin{
			CreationTime: now.Add(-time.Minute), LastEncryptedTime: now, LastDecryptedTime: now,
		}},
	}
}

func newInboundGroupSession(
	t *testing.T,
	roomID id.RoomID,
	senderKey id.SenderKey,
	maxAge time.Duration,
	backup id.KeyBackupVersion,
) *crypto.InboundGroupSession {
	t.Helper()
	outbound, err := crypto.NewOutboundGroupSession(roomID, nil)
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := crypto.NewInboundGroupSession(
		senderKey, "signing-key", roomID, outbound.Internal.Key(), maxAge, 100, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	inbound.KeyBackupVersion = backup
	return inbound
}

func assertGroupSessionIter(
	t *testing.T,
	iter interface {
		AsList() ([]*crypto.InboundGroupSession, error)
	},
	want id.SessionID,
) {
	t.Helper()
	items, err := iter.AsList()
	if err != nil || len(items) == 0 {
		t.Fatalf("group session iterator = %#v, %v", items, err)
	}
	for _, item := range items {
		if item.ID() == want {
			return
		}
	}
	t.Fatalf("group session iterator did not contain %s", want)
}
