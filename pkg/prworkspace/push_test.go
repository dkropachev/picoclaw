package prworkspace

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/hkdf"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

type webPushReceiver struct {
	privateKey []byte
	public     []byte
	publicKey  string
	auth       string
}

func newWebPushReceiver(t *testing.T) webPushReceiver {
	t.Helper()
	privateKey, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	publicKey := elliptic.Marshal(elliptic.P256(), x, y)
	auth := make([]byte, 16)
	_, err = io.ReadFull(rand.Reader, auth)
	require.NoError(t, err)
	return webPushReceiver{
		privateKey: privateKey,
		public:     publicKey,
		publicKey:  base64.RawURLEncoding.EncodeToString(publicKey),
		auth:       base64.RawURLEncoding.EncodeToString(auth),
	}
}

type capturedWebPushRequest struct {
	body   []byte
	header http.Header
}

func newWebPushTestServer(
	t *testing.T,
	status int,
	requests chan<- capturedWebPushRequest,
) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, 8<<10))
		require.NoError(t, err)
		requests <- capturedWebPushRequest{body: body, header: request.Header.Clone()}
		response.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server
}

func newVAPIDKeyPair(t *testing.T) (string, string) {
	t.Helper()
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	require.NoError(t, err)
	return privateKey, publicKey
}

type developmentPushStateStub struct {
	document eventing.DevelopmentPushStateDocument
	getErr   error
	putErr   error
	puts     int
}

func (store *developmentPushStateStub) GetDevelopmentPushState(
	context.Context,
) (eventing.DevelopmentPushStateDocument, error) {
	if store.getErr != nil {
		return eventing.DevelopmentPushStateDocument{}, store.getErr
	}
	result := store.document
	result.State = append(json.RawMessage(nil), result.State...)
	return result, nil
}

func (store *developmentPushStateStub) PutDevelopmentPushState(
	_ context.Context,
	state json.RawMessage,
	expectedVersion uint64,
) (eventing.DevelopmentPushStateDocument, error) {
	store.puts++
	if store.putErr != nil {
		return eventing.DevelopmentPushStateDocument{}, store.putErr
	}
	if expectedVersion != store.document.Version {
		return eventing.DevelopmentPushStateDocument{}, ErrConflict
	}
	store.document = eventing.DevelopmentPushStateDocument{
		Version: expectedVersion + 1,
		State:   append(json.RawMessage(nil), state...),
	}
	return store.document, nil
}

func decryptWebPushPayload(
	t *testing.T,
	receiver webPushReceiver,
	record []byte,
) map[string]any {
	t.Helper()
	require.GreaterOrEqual(t, len(record), 22)
	salt := record[:16]
	publicKeyLength := int(record[20])
	require.Greater(t, publicKeyLength, 0)
	require.Greater(t, len(record), 21+publicKeyLength)
	senderPublicKey := record[21 : 21+publicKeyLength]
	senderX, senderY := elliptic.Unmarshal(elliptic.P256(), senderPublicKey)
	require.NotNil(t, senderX)
	sharedX, sharedY := elliptic.P256().ScalarMult(senderX, senderY, receiver.privateKey)
	require.True(t, elliptic.P256().IsOnCurve(sharedX, sharedY))
	sharedSecret := make([]byte, elliptic.P256().Params().BitSize/8)
	sharedX.FillBytes(sharedSecret)
	auth, err := base64.RawURLEncoding.DecodeString(receiver.auth)
	require.NoError(t, err)
	info := bytes.NewBufferString("WebPush: info\x00")
	_, _ = info.Write(receiver.public)
	_, _ = info.Write(senderPublicKey)
	ikm := readWebPushHKDF(t, hkdf.New(sha256.New, sharedSecret, auth, info.Bytes()), 32)
	contentKey := readWebPushHKDF(
		t,
		hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")),
		16,
	)
	nonce := readWebPushHKDF(
		t,
		hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")),
		12,
	)
	block, err := aes.NewCipher(contentKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plaintext, err := gcm.Open(nil, nonce, record[21+publicKeyLength:], nil)
	require.NoError(t, err)
	delimiter := bytes.IndexByte(plaintext, 2)
	require.GreaterOrEqual(t, delimiter, 0)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(plaintext[:delimiter], &payload))
	return payload
}

func readWebPushHKDF(t *testing.T, reader io.Reader, size int) []byte {
	t.Helper()
	result := make([]byte, size)
	_, err := io.ReadFull(reader, result)
	require.NoError(t, err)
	return result
}

func TestPrunePushDeliverySuppressionKeepsOnlyBoundedActiveSet(t *testing.T) {
	delivered := make(map[string]uint64, 3000)
	active := make(map[string]struct{}, maxPushSuppressionEntries)
	for index := 0; index < 3000; index++ {
		id := fmt.Sprintf("dnt_%032x", index)
		delivered[id] = 1
		if index < maxPushSuppressionEntries {
			active[id] = struct{}{}
		}
	}
	state := pushState{Subscriptions: map[string]pushSubscription{
		"device": {ID: "device", Delivered: delivered},
	}}
	require.True(t, prunePushDeliverySuppression(&state, active))
	require.Len(t, state.Subscriptions["device"].Delivered, maxPushSuppressionEntries)
	require.False(t, prunePushDeliverySuppression(&state, active))
}

func TestDeliverDevelopmentPushSuccessDuplicatePrivacyAndGone(t *testing.T) {
	receiver := newWebPushReceiver(t)
	requests := make(chan capturedWebPushRequest, 4)
	server := newWebPushTestServer(t, http.StatusCreated, requests)
	privateKey, publicKey := newVAPIDKeyPair(t)
	state := pushState{
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		Subscriptions: map[string]pushSubscription{
			"device": {
				ID: "device", Name: "Phone", Endpoint: server.URL,
				Auth: receiver.auth, P256DH: receiver.publicKey, Enabled: true, Version: 1,
				Delivered: make(map[string]uint64),
			},
		},
	}
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	store := &developmentPushStateStub{document: eventing.DevelopmentPushStateDocument{
		Version: 7, State: encoded,
	}}
	notification := developmentnotifications.Notification{
		ID: "dnt_00000000000000000000000000000031", Generation: 1,
		Repository: "private/repository", Reason: developmentnotifications.ReasonProviderOutcomeUnknown,
		Priority: developmentnotifications.PriorityCritical, Status: developmentnotifications.StatusOpen,
	}

	require.True(t, deliverDevelopmentPush(nil, store, notification))
	firstRequest := <-requests
	require.Equal(t, notification.ID, firstRequest.header.Get("Topic"))
	require.Equal(t, "300", firstRequest.header.Get("TTL"))
	require.NotEmpty(t, firstRequest.header.Get("Authorization"))
	payload := decryptWebPushPayload(t, receiver, firstRequest.body)
	require.Equal(t, notification.ID, payload["notification_id"])
	require.Equal(t, string(notification.Reason), payload["reason"])
	require.NotContains(t, payload, "repository")
	persisted, err := decodePushState(store.document.State)
	require.NoError(t, err)
	require.Equal(t, notification.Generation, persisted.Subscriptions["device"].Delivered[notification.ID])
	require.NotNil(t, persisted.Subscriptions["device"].LastDeliveredAt)

	require.False(t, deliverDevelopmentPush(t.Context(), store, notification))
	select {
	case duplicate := <-requests:
		t.Fatalf("duplicate push request = %#v", duplicate)
	default:
	}

	persisted.IncludeRepository = true
	encoded, err = json.Marshal(persisted)
	require.NoError(t, err)
	store.document.State = encoded
	notification.Generation++
	require.True(t, deliverDevelopmentPush(t.Context(), store, notification))
	privacyRequest := <-requests
	payload = decryptWebPushPayload(t, receiver, privacyRequest.body)
	require.Equal(t, notification.Repository, payload["repository"])

	t.Run("gone subscription is disabled", func(t *testing.T) {
		goneReceiver := newWebPushReceiver(t)
		goneRequests := make(chan capturedWebPushRequest, 1)
		goneServer := newWebPushTestServer(t, http.StatusGone, goneRequests)
		goneState := pushState{
			PrivateKey: privateKey,
			PublicKey:  publicKey,
			Subscriptions: map[string]pushSubscription{
				"gone": {
					ID: "gone", Endpoint: goneServer.URL, Auth: goneReceiver.auth,
					P256DH: goneReceiver.publicKey, Enabled: true, Version: 1,
					Delivered: make(map[string]uint64),
				},
			},
		}
		goneEncoded, marshalErr := json.Marshal(goneState)
		require.NoError(t, marshalErr)
		goneStore := &developmentPushStateStub{document: eventing.DevelopmentPushStateDocument{
			Version: 1, State: goneEncoded,
		}}
		require.True(t, deliverDevelopmentPush(t.Context(), goneStore, notification))
		<-goneRequests
		gonePersisted, decodeErr := decodePushState(goneStore.document.State)
		require.NoError(t, decodeErr)
		require.False(t, gonePersisted.Subscriptions["gone"].Enabled)
		require.Equal(t, uint64(2), gonePersisted.Subscriptions["gone"].Version)
	})

	require.False(t, deliverDevelopmentPush(t.Context(), struct{}{}, notification))
	lowPriority := notification
	lowPriority.Priority = developmentnotifications.PriorityMedium
	require.False(t, deliverDevelopmentPush(t.Context(), store, lowPriority))
	failedStore := &developmentPushStateStub{getErr: errors.New("read failed")}
	require.False(t, deliverDevelopmentPush(t.Context(), failedStore, notification))
	failedStore = &developmentPushStateStub{document: eventing.DevelopmentPushStateDocument{
		Version: 1, State: json.RawMessage(`{"private_key":"broken"}`),
	}}
	require.False(t, deliverDevelopmentPush(t.Context(), failedStore, notification))
}

func TestPushStateDecodingAndSubscriptionValidation(t *testing.T) {
	state, err := decodePushState(nil)
	require.NoError(t, err)
	require.NotNil(t, state.Subscriptions)
	state, err = decodePushState(json.RawMessage(`{"subscriptions":{"device":{"id":"device"}}}`))
	require.NoError(t, err)
	require.NotNil(t, state.Subscriptions["device"].Delivered)
	_, err = decodePushState(json.RawMessage(`{"subscriptions":[]}`))
	require.Error(t, err)

	receiver := newWebPushReceiver(t)
	valid := PushSubscriptionInput{
		Endpoint: "https://push.example.test/device", Auth: receiver.auth,
		P256DH: receiver.publicKey, Name: "Phone",
	}
	require.NoError(t, validatePushSubscriptionInput(valid))
	invalid := []PushSubscriptionInput{
		{},
		{Endpoint: "http://push.example.test/device", Auth: receiver.auth, P256DH: receiver.publicKey, Name: "Phone"},
		{Endpoint: valid.Endpoint, Auth: "short", P256DH: receiver.publicKey, Name: "Phone"},
		{Endpoint: valid.Endpoint, Auth: "!!!!!!!!", P256DH: receiver.publicKey, Name: "Phone"},
	}
	for _, input := range invalid {
		require.ErrorIs(t, validatePushSubscriptionInput(input), ErrInvalid)
	}
	require.Equal(t, "Phone", publicPushSubscription(pushSubscription{Name: "Phone"}).Name)
	require.False(t, prunePushDeliverySuppression(nil, nil))

	service, err := NewService(ServiceConfig{Store: NewMemoryStore()})
	require.NoError(t, err)
	_, err = service.NotificationSettings(t.Context())
	require.ErrorContains(t, err, "unavailable")
	_, err = service.PutNotificationSettings(t.Context(), true, 1)
	require.ErrorIs(t, err, ErrConflict)
	_, err = service.ListPushSubscriptions(t.Context())
	require.ErrorContains(t, err, "unavailable")
	_, err = service.CreatePushSubscription(t.Context(), valid)
	require.ErrorContains(t, err, "unavailable")
	_, err = service.UpdatePushSubscription(t.Context(), "missing", "Phone", true, 1)
	require.ErrorContains(t, err, "unavailable")
	require.ErrorContains(t, service.DeletePushSubscription(t.Context(), "missing", 1), "unavailable")
}
