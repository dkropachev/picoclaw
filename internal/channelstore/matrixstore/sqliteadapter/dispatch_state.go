package sqliteadapter

import (
	"context"

	"maunium.net/go/mautrix/event"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func dispatchIdentitiesAndState(
	ctx context.Context,
	operation string,
	input matrixstore.Request,
	bundle *storeBundle,
) (any, error) {
	cryptoStore := bundle.cryptoStore()
	switch operation {
	case "crypto.get-devices":
		devices, err := cryptoStore.GetDevices(ctx, input.UserID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Devices: devices}, nil
	case "crypto.get-device":
		device, err := cryptoStore.GetDevice(ctx, input.UserID, input.TargetDeviceID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Device: device}, nil
	case "crypto.put-device":
		if input.Device == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix device is missing")
		}
		return mutationResult(cryptoStore.PutDevice(ctx, input.UserID, input.Device))
	case "crypto.put-devices":
		return mutationResult(cryptoStore.PutDevices(ctx, input.UserID, input.Devices))
	case "crypto.find-device-by-key":
		device, err := cryptoStore.FindDeviceByKey(ctx, input.UserID, input.IdentityKey)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Device: device}, nil
	case "crypto.filter-tracked-users":
		users, err := cryptoStore.FilterTrackedUsers(ctx, input.Users)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Users: users}, nil
	case "crypto.mark-tracked-users-outdated":
		return mutationResult(cryptoStore.MarkTrackedUsersOutdated(ctx, input.Users))
	case "crypto.get-outdated-tracked-users":
		users, err := cryptoStore.GetOutdatedTrackedUsers(ctx)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Users: users}, nil
	case "crypto.put-cross-signing-key":
		return mutationResult(cryptoStore.PutCrossSigningKey(
			ctx, input.UserID, input.CrossUsage, input.CrossSigningKey,
		))
	case "crypto.get-cross-signing-keys":
		keys, err := cryptoStore.GetCrossSigningKeys(ctx, input.UserID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{CrossSigningKeys: keys}, nil
	case "crypto.put-signature":
		return mutationResult(cryptoStore.PutSignature(
			ctx, input.UserID, input.SignedKey, input.SignerUserID, input.SignerKey, input.Signature,
		))
	case "crypto.is-key-signed-by":
		valid, err := cryptoStore.IsKeySignedBy(
			ctx, input.UserID, input.SignedKey, input.SignerUserID, input.SignerKey,
		)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{OK: valid}, nil
	case "crypto.drop-signatures-by-key":
		count, err := cryptoStore.DropSignaturesByKey(ctx, input.UserID, input.SignedKey)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Count: count}, nil
	case "crypto.get-signatures-for-key-by":
		signatures, err := cryptoStore.GetSignaturesForKeyBy(
			ctx, input.UserID, input.SignedKey, input.SignerUserID,
		)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Signatures: signatures}, nil
	case "crypto.put-secret":
		return mutationResult(cryptoStore.PutSecret(ctx, input.SecretName, input.Value))
	case "crypto.get-secret":
		value, err := cryptoStore.GetSecret(ctx, input.SecretName)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Value: value}, nil
	case "crypto.delete-secret":
		return mutationResult(cryptoStore.DeleteSecret(ctx, input.SecretName))
	case "sync.save-filter-id":
		return mutationResult(cryptoStore.SaveFilterID(ctx, input.UserID, input.FilterID))
	case "sync.load-filter-id":
		value, err := cryptoStore.LoadFilterID(ctx, input.UserID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Value: value}, nil
	case "sync.save-next-batch":
		return mutationResult(cryptoStore.SaveNextBatch(ctx, input.UserID, input.NextBatch))
	case "sync.load-next-batch":
		if _, err := cryptoStore.GetAccount(ctx); err != nil {
			return nil, backendError(err)
		}
		value, err := cryptoStore.LoadNextBatch(ctx, input.UserID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Value: value}, nil
	case "state.is-in-room":
		return matrixstore.Response{OK: bundle.state.IsInRoom(ctx, input.RoomID, input.UserID)}, nil
	case "state.is-invited":
		return matrixstore.Response{OK: bundle.state.IsInvited(ctx, input.RoomID, input.UserID)}, nil
	case "state.is-membership":
		return matrixstore.Response{
			OK: bundle.state.IsMembership(ctx, input.RoomID, input.UserID, input.Memberships...),
		}, nil
	case "state.get-member", "state.try-get-member":
		var (
			member *event.MemberEventContent
			err    error
		)
		if operation == "state.get-member" {
			member, err = bundle.state.GetMember(ctx, input.RoomID, input.UserID)
		} else {
			member, err = bundle.state.TryGetMember(ctx, input.RoomID, input.UserID)
		}
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Member: member}, nil
	case "state.set-membership":
		return mutationResult(bundle.state.SetMembership(
			ctx, input.RoomID, input.UserID, input.Membership,
		))
	case "state.set-member":
		if input.Member == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix member is missing")
		}
		return mutationResult(bundle.state.SetMember(ctx, input.RoomID, input.UserID, input.Member))
	case "state.is-confusable-name":
		users, err := bundle.state.IsConfusableName(ctx, input.RoomID, input.UserID, input.Name)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Users: users}, nil
	case "state.clear-cached-members":
		return mutationResult(bundle.state.ClearCachedMembers(ctx, input.RoomID, input.Memberships...))
	case "state.replace-cached-members":
		events := make([]*event.Event, 0, len(input.MemberEvents))
		for _, record := range input.MemberEvents {
			stateKey := record.UserID.String()
			content := record.Content
			events = append(events, &event.Event{
				Type:     event.StateMember,
				RoomID:   input.RoomID,
				StateKey: &stateKey,
				Content:  event.Content{Parsed: &content},
			})
		}
		return mutationResult(bundle.state.ReplaceCachedMembers(
			ctx, input.RoomID, events, input.OnlyMemberships...,
		))
	case "state.set-power-levels":
		if input.PowerLevels == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix power levels are missing")
		}
		return mutationResult(bundle.state.SetPowerLevels(ctx, input.RoomID, input.PowerLevels))
	case "state.get-power-levels":
		levels, err := bundle.state.GetPowerLevels(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{PowerLevels: levels}, nil
	case "state.set-create":
		if input.Create == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix create event is missing")
		}
		return mutationResult(bundle.state.SetCreate(ctx, input.Create))
	case "state.get-create":
		evt, err := bundle.state.GetCreate(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Create: evt}, nil
	case "state.get-join-rules":
		rules, err := bundle.state.GetJoinRules(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{JoinRules: rules}, nil
	case "state.set-join-rules":
		if input.JoinRules == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix join rules are missing")
		}
		return mutationResult(bundle.state.SetJoinRules(ctx, input.RoomID, input.JoinRules))
	case "state.has-fetched-members":
		fetched, err := bundle.state.HasFetchedMembers(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{OK: fetched}, nil
	case "state.mark-members-fetched":
		return mutationResult(bundle.state.MarkMembersFetched(ctx, input.RoomID))
	case "state.get-all-members":
		members, err := bundle.state.GetAllMembers(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Members: members}, nil
	case "state.set-encryption-event":
		if input.Encryption == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix encryption event is missing")
		}
		return mutationResult(bundle.state.SetEncryptionEvent(ctx, input.RoomID, input.Encryption))
	case "state.get-encryption-event":
		content, err := bundle.state.GetEncryptionEvent(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Encryption: content}, nil
	case "state.is-encrypted":
		encrypted, err := bundle.state.IsEncrypted(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{OK: encrypted}, nil
	case "state.find-shared-rooms":
		rooms, err := bundle.state.FindSharedRooms(ctx, input.UserID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{RoomIDs: rooms}, nil
	case "state.get-room-joined-or-invited-members":
		users, err := bundle.state.GetRoomJoinedOrInvitedMembers(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Users: users}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "Matrix store operation is unsupported")
	}
}
