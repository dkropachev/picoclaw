package matrixstore

import (
	"context"
	"strings"

	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/pkg/database"
)

// Store is a typed Matrix storage client. It implements the mautrix crypto,
// state, and sync store contracts without exposing a generic query facility.
type Store struct {
	client    *database.Client
	storeID   database.StoreID
	deviceID  id.DeviceID
	pickleKey []byte
}

func New(
	client *database.Client,
	storeID database.StoreID,
	deviceID id.DeviceID,
	pickleKey []byte,
) (*Store, error) {
	if client == nil {
		return nil, database.NewError(database.CodeUnavailable, "Matrix database broker client is unavailable")
	}
	if !storeID.Valid() || !strings.HasPrefix(storeID.String(), "channel/matrix/") {
		return nil, database.NewError(database.CodeInvalid, "Matrix store identity is invalid")
	}
	if deviceID == "" || len(pickleKey) == 0 {
		return nil, database.NewError(database.CodeInvalid, "Matrix crypto store identity is invalid")
	}
	return &Store{
		client: client, storeID: storeID, deviceID: deviceID,
		pickleKey: append([]byte(nil), pickleKey...),
	}, nil
}

func ResolveStore(
	ctx context.Context,
	client *database.Client,
	channelName string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "Matrix database broker client is unavailable")
	}
	if strings.TrimSpace(channelName) == "" || channelName != strings.TrimSpace(channelName) ||
		strings.ContainsRune(channelName, 0) {
		return "", database.NewError(database.CodeInvalid, "Matrix channel identity is invalid")
	}
	var response ResolveResponse
	if err := client.Call(
		ctx, Domain, Version, ResolveOperation, ResolveRequest{ChannelName: channelName}, &response,
	); err != nil {
		return "", err
	}
	if !response.StoreID.Valid() || !strings.HasPrefix(response.StoreID.String(), "channel/matrix/") {
		return "", database.NewError(database.CodeIntegrity, "Matrix broker returned an invalid store identity")
	}
	return response.StoreID, nil
}

func Preflight(ctx context.Context, client *database.Client, storeID database.StoreID) error {
	if client == nil || !storeID.Valid() {
		return database.NewError(database.CodeInvalid, "Matrix preflight target is invalid")
	}
	var response PreflightResponse
	if err := client.Call(
		ctx, Domain, Version, PreflightOperation, StoreTarget{StoreID: storeID}, &response,
	); err != nil {
		return err
	}
	if !response.Ready {
		return database.NewError(database.CodeIntegrity, "Matrix broker returned an invalid preflight response")
	}
	return nil
}

func (store *Store) call(
	ctx context.Context,
	operation string,
	request Request,
	response *Response,
	mutation bool,
) error {
	if store == nil || store.client == nil || !store.storeID.Valid() || store.deviceID == "" {
		return database.NewError(database.CodeUnavailable, "Matrix typed store is unavailable")
	}
	request.StoreID = store.storeID
	request.DeviceID = store.deviceID
	if !mutation {
		return store.client.Call(ctx, Domain, Version, operation, request, response)
	}
	return store.client.CallWithOptions(
		ctx, Domain, Version, operation, request, response,
		database.CallOptions{Mutation: true},
	)
}
