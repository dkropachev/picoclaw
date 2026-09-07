//go:build whatsapp_native

//nolint:govet // Independent store assertions intentionally reuse short-lived result names.
package whatsappstore_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore"
	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore/sqliteadapter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestNativeStoreRoundTripAndAtomicDecryptionBatch(t *testing.T) {
	container, device, _, _ := openNativeStore(t)
	ctx := t.Context()

	identityKey := filled32(1)
	otherIdentityKey := filled32(2)
	if trusted, err := device.Identities.IsTrustedIdentity(ctx, "100:1", identityKey); err != nil || !trusted {
		t.Fatalf("new identity trust = %t, %v", trusted, err)
	}
	if err := device.Identities.PutIdentity(ctx, "100:1", identityKey); err != nil {
		t.Fatal(err)
	}
	if trusted, err := device.Identities.IsTrustedIdentity(ctx, "100:1", otherIdentityKey); err != nil || trusted {
		t.Fatalf("changed identity trust = %t, %v", trusted, err)
	}
	if err := device.Identities.PutIdentity(ctx, "100:2", identityKey); err != nil {
		t.Fatal(err)
	}
	if err := device.Identities.DeleteAllIdentities(ctx, "100"); err != nil {
		t.Fatal(err)
	}
	if trusted, err := device.Identities.IsTrustedIdentity(ctx, "100:1", otherIdentityKey); err != nil || !trusted {
		t.Fatalf("deleted identity trust = %t, %v", trusted, err)
	}

	if err := device.Sessions.PutSession(ctx, "200:1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if data, err := device.Sessions.GetSession(ctx, "200:1"); err != nil || !bytes.Equal(data, []byte("one")) {
		t.Fatalf("session = %q, %v", data, err)
	}
	if has, err := device.Sessions.HasSession(ctx, "200:1"); err != nil || !has {
		t.Fatalf("HasSession = %t, %v", has, err)
	}
	if err := device.Sessions.PutManySessions(ctx, map[string][]byte{
		"200:2": []byte("two"), "200:3": []byte("three"),
	}); err != nil {
		t.Fatal(err)
	}
	many, err := device.Sessions.GetManySessions(ctx, []string{"200:1", "200:2", "missing"})
	if err != nil || !bytes.Equal(many["200:2"], []byte("two")) || many["missing"] != nil {
		t.Fatalf("many sessions = %#v, %v", many, err)
	}
	if err := device.Sessions.DeleteSession(ctx, "200:1"); err != nil {
		t.Fatal(err)
	}
	if err := device.Sessions.DeleteAllSessions(ctx, "200"); err != nil {
		t.Fatal(err)
	}

	preKeys, err := device.PreKeys.GetOrGenPreKeys(ctx, 2)
	if err != nil || len(preKeys) != 2 {
		t.Fatalf("generated prekeys = %d, %v", len(preKeys), err)
	}
	firstPreKey, err := device.PreKeys.GetPreKey(ctx, preKeys[0].KeyID)
	if err != nil || firstPreKey == nil || firstPreKey.KeyID != preKeys[0].KeyID {
		t.Fatalf("GetPreKey = %#v, %v", firstPreKey, err)
	}
	if err := device.PreKeys.MarkPreKeysAsUploaded(ctx, preKeys[0].KeyID); err != nil {
		t.Fatal(err)
	}
	if count, err := device.PreKeys.UploadedPreKeyCount(ctx); err != nil || count != 1 {
		t.Fatalf("uploaded prekey count = %d, %v", count, err)
	}
	generated, err := device.PreKeys.GenOnePreKey(ctx)
	if err != nil || generated == nil {
		t.Fatalf("GenOnePreKey = %#v, %v", generated, err)
	}

	if err := device.SenderKeys.PutSenderKey(ctx, "group", "sender", []byte("sender-key")); err != nil {
		t.Fatal(err)
	}
	if data, err := device.SenderKeys.GetSenderKey(ctx, "group", "sender"); err != nil ||
		!bytes.Equal(data, []byte("sender-key")) {
		t.Fatalf("sender key = %q, %v", data, err)
	}

	appKeyID := []byte("app-key")
	appKey := store.AppStateSyncKey{Data: []byte("data"), Fingerprint: []byte("fingerprint"), Timestamp: 42}
	if err := device.AppStateKeys.PutAppStateSyncKey(ctx, appKeyID, appKey); err != nil {
		t.Fatal(err)
	}
	if got, err := device.AppStateKeys.GetAppStateSyncKey(ctx, appKeyID); err != nil || got == nil ||
		!bytes.Equal(got.Data, appKey.Data) {
		t.Fatalf("app-state key = %#v, %v", got, err)
	}
	if latest, err := device.AppStateKeys.GetLatestAppStateSyncKeyID(ctx); err != nil ||
		!bytes.Equal(latest, appKeyID) {
		t.Fatalf("latest app-state key = %q, %v", latest, err)
	}
	if all, err := device.AppStateKeys.GetAllAppStateSyncKeys(ctx); err != nil || len(all) != 1 {
		t.Fatalf("all app-state keys = %#v, %v", all, err)
	}

	var appHash [128]byte
	appHash[0] = 9
	if err := device.AppState.PutAppStateVersion(ctx, "regular", 1, appHash); err != nil {
		t.Fatal(err)
	}
	if version, hash, err := device.AppState.GetAppStateVersion(ctx, "regular"); err != nil ||
		version != 1 || hash != appHash {
		t.Fatalf("app-state version = %d/%x, %v", version, hash[:2], err)
	}
	mutation := store.AppStateMutationMAC{IndexMAC: bytes.Repeat([]byte{3}, 32), ValueMAC: bytes.Repeat([]byte{4}, 32)}
	if err := device.AppState.PutAppStateMutationMACs(
		ctx, "regular", 1, []store.AppStateMutationMAC{mutation},
	); err != nil {
		t.Fatal(err)
	}
	if value, err := device.AppState.GetAppStateMutationMAC(ctx, "regular", mutation.IndexMAC); err != nil ||
		!bytes.Equal(value, mutation.ValueMAC) {
		t.Fatalf("app-state MAC = %x, %v", value, err)
	}
	if err := device.AppState.DeleteAppStateMutationMACs(ctx, "regular", [][]byte{mutation.IndexMAC}); err != nil {
		t.Fatal(err)
	}
	if err := device.AppState.DeleteAppStateVersion(ctx, "regular"); err != nil {
		t.Fatal(err)
	}

	contact := types.NewJID("300", types.DefaultUserServer)
	if changed, previous, err := device.Contacts.PutPushName(ctx, contact, "Push"); err != nil ||
		!changed || previous != "" {
		t.Fatalf("PutPushName = %t/%q, %v", changed, previous, err)
	}
	if changed, _, err := device.Contacts.PutBusinessName(ctx, contact, "Business"); err != nil || !changed {
		t.Fatalf("PutBusinessName = %t, %v", changed, err)
	}
	if err := device.Contacts.PutContactName(ctx, contact, "First", "Full"); err != nil {
		t.Fatal(err)
	}
	secondContact := types.NewJID("301", types.DefaultUserServer)
	if err := device.Contacts.PutAllContactNames(ctx, []store.ContactEntry{{
		JID: secondContact, FirstName: "Second", FullName: "Contact",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := device.Contacts.PutManyRedactedPhones(ctx, []store.RedactedPhoneEntry{{
		JID: secondContact, RedactedPhone: "+3......01",
	}}); err != nil {
		t.Fatal(err)
	}
	if info, err := device.Contacts.GetContact(ctx, contact); err != nil || !info.Found || info.FirstName != "First" {
		t.Fatalf("contact = %#v, %v", info, err)
	}
	if contacts, err := device.Contacts.GetAllContacts(ctx); err != nil || len(contacts) != 2 {
		t.Fatalf("contacts = %#v, %v", contacts, err)
	}

	chat := types.NewJID("400", types.GroupServer)
	if err := device.ChatSettings.PutMutedUntil(ctx, chat, store.MutedForever); err != nil {
		t.Fatal(err)
	}
	if err := device.ChatSettings.PutPinned(ctx, chat, true); err != nil {
		t.Fatal(err)
	}
	if err := device.ChatSettings.PutArchived(ctx, chat, true); err != nil {
		t.Fatal(err)
	}
	if settings, err := device.ChatSettings.GetChatSettings(ctx, chat); err != nil || !settings.Found ||
		settings.MutedUntil != store.MutedForever || !settings.Pinned || !settings.Archived {
		t.Fatalf("chat settings = %#v, %v", settings, err)
	}

	sender := types.NewJID("401", types.DefaultUserServer)
	if err := device.MsgSecrets.PutMessageSecret(ctx, chat, sender, "one", []byte("secret-one")); err != nil {
		t.Fatal(err)
	}
	if err := device.MsgSecrets.PutMessageSecrets(ctx, []store.MessageSecretInsert{{
		Chat: chat, Sender: sender, ID: "two", Secret: []byte("secret-two"),
	}}); err != nil {
		t.Fatal(err)
	}
	if secret, realSender, err := device.MsgSecrets.GetMessageSecret(ctx, chat, sender, "two"); err != nil ||
		!bytes.Equal(secret, []byte("secret-two")) || realSender != sender {
		t.Fatalf("message secret = %q/%s, %v", secret, realSender, err)
	}

	tokenTime := time.Unix(1_700_000_000, 0).UTC()
	if err := device.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{
		User: sender, Token: []byte("token"), Timestamp: tokenTime,
	}); err != nil {
		t.Fatal(err)
	}
	if token, err := device.PrivacyTokens.GetPrivacyToken(ctx, sender); err != nil || token == nil ||
		!bytes.Equal(token.Token, []byte("token")) || !token.Timestamp.Equal(tokenTime) {
		t.Fatalf("privacy token = %#v, %v", token, err)
	}

	lid := types.NewJID("500", types.HiddenUserServer)
	pn := types.NewJID("501", types.DefaultUserServer)
	if err := device.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	secondLID := types.NewJID("502", types.HiddenUserServer)
	secondPN := types.NewJID("503", types.DefaultUserServer)
	if err := device.LIDs.PutManyLIDMappings(ctx, []store.LIDMapping{{LID: secondLID, PN: secondPN}}); err != nil {
		t.Fatal(err)
	}
	if got, err := device.LIDs.GetPNForLID(ctx, lid); err != nil || got != pn {
		t.Fatalf("PN for LID = %s, %v", got, err)
	}
	if got, err := device.LIDs.GetLIDForPN(ctx, pn); err != nil || got != lid {
		t.Fatalf("LID for PN = %s, %v", got, err)
	}
	if mappings, err := device.LIDs.GetManyLIDsForPNs(ctx, []types.JID{pn, secondPN}); err != nil ||
		mappings[pn] != lid || mappings[secondPN] != secondLID {
		t.Fatalf("many LID mappings = %#v, %v", mappings, err)
	}
	if err := device.EventBuffer.DoDecryptionTxn(ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("empty decryption transaction = %v", err)
	}

	rollbackErr := errors.New("stop decryption")
	if err := device.EventBuffer.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
		if err := device.Sessions.PutSession(txnContext, "rollback:1", []byte("discard")); err != nil {
			return err
		}
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("callback rollback error = %v", err)
	}
	if data, err := device.Sessions.GetSession(ctx, "rollback:1"); err != nil || data != nil {
		t.Fatalf("callback rollback session = %q, %v", data, err)
	}

	bufferHash := filled32(8)
	serverTime := time.Unix(1_700_000_001, 0).UTC()
	if err := device.EventBuffer.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
		if err := device.EventBuffer.DoDecryptionTxn(txnContext, func(nestedContext context.Context) error {
			return device.Sessions.PutSession(nestedContext, "nested:1", []byte("nested"))
		}); err != nil {
			return err
		}
		if err := device.Identities.PutIdentity(txnContext, "decrypt:1", identityKey); err != nil {
			return err
		}
		if trusted, err := device.Identities.IsTrustedIdentity(
			txnContext, "decrypt:1", otherIdentityKey,
		); err != nil || trusted {
			return errors.New("identity overlay did not expose collected write")
		}
		if err := device.Sessions.PutSession(txnContext, "decrypt:1", []byte("session")); err != nil {
			return err
		}
		if data, err := device.Sessions.GetSession(txnContext, "decrypt:1"); err != nil ||
			!bytes.Equal(data, []byte("session")) {
			return errors.New("session overlay did not expose collected write")
		}
		if err := device.PreKeys.RemovePreKey(txnContext, preKeys[1].KeyID); err != nil {
			return err
		}
		if removed, err := device.PreKeys.GetPreKey(txnContext, preKeys[1].KeyID); err != nil || removed != nil {
			return errors.New("pre-key overlay did not expose collected removal")
		}
		if err := device.SenderKeys.PutSenderKey(
			txnContext, "decrypt-group", "decrypt-sender", []byte("updated"),
		); err != nil {
			return err
		}
		if data, err := device.SenderKeys.GetSenderKey(txnContext, "decrypt-group", "decrypt-sender"); err != nil ||
			!bytes.Equal(data, []byte("updated")) {
			return errors.New("sender-key overlay did not expose collected write")
		}
		return device.EventBuffer.PutBufferedEvent(
			txnContext, bufferHash, []byte("plaintext"), serverTime,
		)
	}); err != nil {
		t.Fatal(err)
	}
	if data, err := device.Sessions.GetSession(ctx, "decrypt:1"); err != nil || !bytes.Equal(data, []byte("session")) {
		t.Fatalf("committed decryption session = %q, %v", data, err)
	}
	if data, err := device.Sessions.GetSession(ctx, "nested:1"); err != nil || !bytes.Equal(data, []byte("nested")) {
		t.Fatalf("nested decryption session = %q, %v", data, err)
	}
	if event, err := device.EventBuffer.GetBufferedEvent(ctx, bufferHash); err != nil || event == nil ||
		!bytes.Equal(event.Plaintext, []byte("plaintext")) || !event.ServerTime.Equal(serverTime) {
		t.Fatalf("buffered event = %#v, %v", event, err)
	}

	if err := device.EventBuffer.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
		if err := device.Sessions.PutSession(txnContext, "atomic:1", []byte("must-rollback")); err != nil {
			return err
		}
		// The duplicate buffer key fails after the session write has executed
		// inside the broker transaction.
		return device.EventBuffer.PutBufferedEvent(txnContext, bufferHash, []byte("duplicate"), serverTime)
	}); err == nil {
		t.Fatal("duplicate buffered event unexpectedly committed")
	}
	if data, err := device.Sessions.GetSession(ctx, "atomic:1"); err != nil || data != nil {
		t.Fatalf("server transaction rollback session = %q, %v", data, err)
	}

	if err := device.EventBuffer.ClearBufferedEventPlaintext(ctx, bufferHash); err != nil {
		t.Fatal(err)
	}
	if event, err := device.EventBuffer.GetBufferedEvent(ctx, bufferHash); err != nil ||
		event == nil || event.Plaintext != nil {
		t.Fatalf("cleared buffered event = %#v, %v", event, err)
	}
	if err := device.EventBuffer.DeleteOldBufferedHashes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := device.EventBuffer.AddOutgoingEvent(ctx, chat, "outgoing", "skmsg", []byte("wire")); err != nil {
		t.Fatal(err)
	}
	if format, data, err := device.EventBuffer.GetOutgoingEvent(ctx, chat, types.EmptyJID, "outgoing"); err != nil ||
		format != "skmsg" || !bytes.Equal(data, []byte("wire")) {
		t.Fatalf("outgoing event = %q/%q, %v", format, data, err)
	}
	if err := device.EventBuffer.DeleteOldOutgoingEvents(ctx); err != nil {
		t.Fatal(err)
	}

	loaded, err := container.GetFirstDevice(ctx)
	if err != nil || loaded.ID == nil || *loaded.ID != *device.ID || loaded.Account == nil {
		t.Fatalf("reloaded device = %#v, %v", loaded, err)
	}
	if err := loaded.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded.ID != nil {
		t.Fatal("Delete did not clear the client device ID")
	}
}

func TestDecryptionTransactionsSerializeAcrossIndependentContainers(t *testing.T) {
	_, first, home, storeID := openNativeStore(t)
	peerClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	peerContainer, err := whatsappstore.NewContainer(peerClient, storeID, waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peerContainer.Close() })
	second, err := peerContainer.GetFirstDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == nil || first.ID == nil || *second.ID != *first.ID {
		t.Fatalf("peer device mismatch: %#v / %#v", first.ID, second.ID)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := first.Sessions.PutSession(ctx, "serialized:1", []byte("base")); err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	allowCommit := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.EventBuffer.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
			data, readErr := first.Sessions.GetSession(txnContext, "serialized:1")
			if readErr != nil {
				return readErr
			}
			if !bytes.Equal(data, []byte("base")) {
				return errors.New("first transaction did not read base state")
			}
			close(firstEntered)
			select {
			case <-allowCommit:
			case <-ctx.Done():
				return ctx.Err()
			}
			return first.Sessions.PutSession(txnContext, "serialized:1", []byte("committed"))
		})
	}()
	select {
	case <-firstEntered:
	case err := <-firstDone:
		t.Fatalf("first transaction failed before callback barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	type transactionResult struct {
		observed []byte
		err      error
	}
	secondDone := make(chan transactionResult, 1)
	go func() {
		var observed []byte
		txnErr := second.EventBuffer.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
			var readErr error
			observed, readErr = second.Sessions.GetSession(txnContext, "serialized:1")
			if readErr != nil {
				return readErr
			}
			return second.Sessions.PutSession(txnContext, "serialized:1", []byte("peer"))
		})
		secondDone <- transactionResult{observed: observed, err: txnErr}
	}()
	select {
	case result := <-secondDone:
		t.Fatalf("peer transaction bypassed active broker lease: %#v", result)
	case <-time.After(150 * time.Millisecond):
	}
	close(allowCommit)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	result := <-secondDone
	if result.err != nil || !bytes.Equal(result.observed, []byte("committed")) {
		t.Fatalf("peer transaction observed %q, %v", result.observed, result.err)
	}
	if data, err := first.Sessions.GetSession(ctx, "serialized:1"); err != nil || !bytes.Equal(data, []byte("peer")) {
		t.Fatalf("peer transaction write = %q, %v", data, err)
	}

	if err := first.Sessions.PutSession(ctx, "serialized:abort", []byte("preserved")); err != nil {
		t.Fatal(err)
	}
	abortEntered := make(chan struct{})
	allowAbort := make(chan struct{})
	abortDone := make(chan error, 1)
	abortErr := errors.New("abort transaction")
	go func() {
		abortDone <- first.EventBuffer.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
			if err := first.Sessions.PutSession(txnContext, "serialized:abort", []byte("discarded")); err != nil {
				return err
			}
			close(abortEntered)
			select {
			case <-allowAbort:
				return abortErr
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-abortEntered:
	case err := <-abortDone:
		t.Fatalf("abort transaction failed before callback barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	readDone := make(chan transactionResult, 1)
	go func() {
		data, readErr := second.Sessions.GetSession(ctx, "serialized:abort")
		readDone <- transactionResult{observed: data, err: readErr}
	}()
	select {
	case result := <-readDone:
		t.Fatalf("ordinary peer read bypassed active broker lease: %#v", result)
	case <-time.After(150 * time.Millisecond):
	}
	close(allowAbort)
	if err := <-abortDone; !errors.Is(err, abortErr) {
		t.Fatalf("abort error = %v", err)
	}
	result = <-readDone
	if result.err != nil || !bytes.Equal(result.observed, []byte("preserved")) {
		t.Fatalf("peer read after abort = %q, %v", result.observed, result.err)
	}
}

func openNativeStore(
	t *testing.T,
) (*whatsappstore.Container, *store.Device, string, database.StoreID) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	storeRoot := filepath.Join(home, "whatsapp")
	channel := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if err := channel.Decode(&config.WhatsAppSettings{
		UseNative: true, SessionStorePath: storeRoot,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"primary": channel},
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteadapter.MigrateDatabase(t.Context(), filepath.Join(storeRoot, "store.db")); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := sqliteadapter.NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	storeID, err := whatsappstore.ResolveStore(t.Context(), client, "primary")
	if err != nil {
		t.Fatal(err)
	}
	var ready whatsappstore.ReadyResponse
	if err := client.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.PreflightOperation,
		whatsappstore.Target{StoreID: storeID}, &ready,
	); err != nil || !ready.Ready {
		t.Fatalf("preflight = %#v, %v", ready, err)
	}
	container, err := whatsappstore.NewContainer(client, storeID, waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device, err := container.GetFirstDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if device.ID != nil || device.Initialized {
		t.Fatalf("fresh device unexpectedly persisted: %#v", device)
	}
	id := types.NewJID("15551234567", types.DefaultUserServer)
	device.ID = &id
	device.LID = types.NewJID("600", types.HiddenUserServer)
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte("details"),
		AccountSignature:    bytes.Repeat([]byte{1}, 64),
		AccountSignatureKey: bytes.Repeat([]byte{2}, 32),
		DeviceSignature:     bytes.Repeat([]byte{3}, 64),
	}
	device.Platform = "test"
	device.PushName = "PicoClaw"
	if err := device.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !device.Initialized || device.Sessions == nil || device.EventBuffer == nil || device.LIDs == nil {
		t.Fatalf("saved device was not initialized: %#v", device)
	}
	return container, device, home, storeID
}

func filled32(value byte) [32]byte {
	var result [32]byte
	for index := range result {
		result[index] = value
	}
	return result
}
