package matrixstore

import (
	"context"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (store *Store) IsInRoom(ctx context.Context, roomID id.RoomID, userID id.UserID) bool {
	var response Response
	return store.call(ctx, opIsInRoom, Request{RoomID: roomID, UserID: userID}, &response, false) == nil && response.OK
}

func (store *Store) IsInvited(ctx context.Context, roomID id.RoomID, userID id.UserID) bool {
	var response Response
	return store.call(ctx, opIsInvited, Request{RoomID: roomID, UserID: userID}, &response, false) == nil && response.OK
}

func (store *Store) IsMembership(
	ctx context.Context,
	roomID id.RoomID,
	userID id.UserID,
	allowed ...event.Membership,
) bool {
	var response Response
	return store.call(ctx, opIsMembership, Request{
		RoomID: roomID, UserID: userID, Memberships: allowed,
	}, &response, false) == nil && response.OK
}

func (store *Store) GetMember(
	ctx context.Context,
	roomID id.RoomID,
	userID id.UserID,
) (*event.MemberEventContent, error) {
	var response Response
	err := store.call(ctx, opGetMember, Request{RoomID: roomID, UserID: userID}, &response, false)
	return response.Member, err
}

func (store *Store) TryGetMember(
	ctx context.Context,
	roomID id.RoomID,
	userID id.UserID,
) (*event.MemberEventContent, error) {
	var response Response
	err := store.call(ctx, opTryGetMember, Request{RoomID: roomID, UserID: userID}, &response, false)
	return response.Member, err
}

func (store *Store) SetMembership(
	ctx context.Context,
	roomID id.RoomID,
	userID id.UserID,
	membership event.Membership,
) error {
	return store.call(ctx, opSetMembership, Request{
		RoomID: roomID, UserID: userID, Membership: membership,
	}, &Response{}, true)
}

func (store *Store) SetMember(
	ctx context.Context,
	roomID id.RoomID,
	userID id.UserID,
	member *event.MemberEventContent,
) error {
	return store.call(ctx, opSetMember, Request{
		RoomID: roomID, UserID: userID, Member: member,
	}, &Response{}, true)
}

func (store *Store) IsConfusableName(
	ctx context.Context,
	roomID id.RoomID,
	currentUser id.UserID,
	name string,
) ([]id.UserID, error) {
	var response Response
	err := store.call(ctx, opIsConfusableName, Request{
		RoomID: roomID, UserID: currentUser, Name: name,
	}, &response, false)
	return response.Users, err
}

func (store *Store) ClearCachedMembers(
	ctx context.Context,
	roomID id.RoomID,
	memberships ...event.Membership,
) error {
	return store.call(ctx, opClearCachedMembers, Request{
		RoomID: roomID, Memberships: memberships,
	}, &Response{}, true)
}

func (store *Store) ReplaceCachedMembers(
	ctx context.Context,
	roomID id.RoomID,
	events []*event.Event,
	onlyMemberships ...event.Membership,
) error {
	records := make([]MemberEventRecord, 0, len(events))
	for _, evt := range events {
		if evt == nil || evt.StateKey == nil {
			continue
		}
		content, ok := evt.Content.Parsed.(*event.MemberEventContent)
		if !ok || content == nil {
			if parseErr := evt.Content.ParseRaw(event.StateMember); parseErr != nil {
				continue
			}
			content, ok = evt.Content.Parsed.(*event.MemberEventContent)
		}
		if !ok || content == nil {
			continue
		}
		records = append(records, MemberEventRecord{
			UserID: id.UserID(evt.GetStateKey()), Content: *content,
		})
	}
	return store.call(ctx, opReplaceCachedMembers, Request{
		RoomID: roomID, MemberEvents: records, OnlyMemberships: onlyMemberships,
	}, &Response{}, true)
}

func (store *Store) SetPowerLevels(
	ctx context.Context,
	roomID id.RoomID,
	levels *event.PowerLevelsEventContent,
) error {
	return store.call(ctx, opSetPowerLevels, Request{
		RoomID: roomID, PowerLevels: levels,
	}, &Response{}, true)
}

func (store *Store) GetPowerLevels(
	ctx context.Context,
	roomID id.RoomID,
) (*event.PowerLevelsEventContent, error) {
	var response Response
	err := store.call(ctx, opGetPowerLevels, Request{RoomID: roomID}, &response, false)
	return response.PowerLevels, err
}

func (store *Store) SetCreate(ctx context.Context, evt *event.Event) error {
	return store.call(ctx, opSetCreate, Request{Create: evt}, &Response{}, true)
}

func (store *Store) GetCreate(ctx context.Context, roomID id.RoomID) (*event.Event, error) {
	var response Response
	if err := store.call(ctx, opGetCreate, Request{RoomID: roomID}, &response, false); err != nil {
		return nil, err
	}
	if response.Create != nil {
		if err := response.Create.Content.ParseRaw(event.StateCreate); err != nil {
			return nil, err
		}
	}
	return response.Create, nil
}

func (store *Store) GetJoinRules(
	ctx context.Context,
	roomID id.RoomID,
) (*event.JoinRulesEventContent, error) {
	var response Response
	err := store.call(ctx, opGetJoinRules, Request{RoomID: roomID}, &response, false)
	return response.JoinRules, err
}

func (store *Store) SetJoinRules(
	ctx context.Context,
	roomID id.RoomID,
	content *event.JoinRulesEventContent,
) error {
	return store.call(ctx, opSetJoinRules, Request{
		RoomID: roomID, JoinRules: content,
	}, &Response{}, true)
}

func (store *Store) HasFetchedMembers(ctx context.Context, roomID id.RoomID) (bool, error) {
	var response Response
	err := store.call(ctx, opHasFetchedMembers, Request{RoomID: roomID}, &response, false)
	return response.OK, err
}

func (store *Store) MarkMembersFetched(ctx context.Context, roomID id.RoomID) error {
	return store.call(ctx, opMarkMembersFetched, Request{RoomID: roomID}, &Response{}, true)
}

func (store *Store) GetAllMembers(
	ctx context.Context,
	roomID id.RoomID,
) (map[id.UserID]*event.MemberEventContent, error) {
	var response Response
	err := store.call(ctx, opGetAllMembers, Request{RoomID: roomID}, &response, false)
	return response.Members, err
}

func (store *Store) SetEncryptionEvent(
	ctx context.Context,
	roomID id.RoomID,
	content *event.EncryptionEventContent,
) error {
	return store.call(ctx, opSetEncryptionEvent, Request{
		RoomID: roomID, Encryption: content,
	}, &Response{}, true)
}

func (store *Store) GetEncryptionEvent(
	ctx context.Context,
	roomID id.RoomID,
) (*event.EncryptionEventContent, error) {
	var response Response
	err := store.call(ctx, opGetEncryptionEvent, Request{RoomID: roomID}, &response, false)
	return response.Encryption, err
}

func (store *Store) IsEncrypted(ctx context.Context, roomID id.RoomID) (bool, error) {
	var response Response
	err := store.call(ctx, opIsEncrypted, Request{RoomID: roomID}, &response, false)
	return response.OK, err
}

func (store *Store) FindSharedRooms(ctx context.Context, userID id.UserID) ([]id.RoomID, error) {
	var response Response
	err := store.call(ctx, opFindSharedRooms, Request{UserID: userID}, &response, false)
	return response.RoomIDs, err
}

func (store *Store) GetRoomJoinedOrInvitedMembers(
	ctx context.Context,
	roomID id.RoomID,
) ([]id.UserID, error) {
	var response Response
	err := store.call(ctx, opGetRoomJoinedOrInvitedMembers, Request{RoomID: roomID}, &response, false)
	return response.Users, err
}

func (store *Store) SaveFilterID(
	ctx context.Context,
	userID id.UserID,
	filterID string,
) error {
	return store.call(ctx, opSaveFilterID, Request{
		UserID: userID, FilterID: filterID,
	}, &Response{}, true)
}

func (store *Store) LoadFilterID(ctx context.Context, userID id.UserID) (string, error) {
	var response Response
	err := store.call(ctx, opLoadFilterID, Request{UserID: userID}, &response, false)
	return response.Value, err
}

func (store *Store) SaveNextBatch(
	ctx context.Context,
	userID id.UserID,
	nextBatch string,
) error {
	return store.call(ctx, opSaveNextBatch, Request{
		UserID: userID, NextBatch: nextBatch,
	}, &Response{}, true)
}

func (store *Store) LoadNextBatch(ctx context.Context, userID id.UserID) (string, error) {
	var response Response
	err := store.call(ctx, opLoadNextBatch, Request{UserID: userID}, &response, false)
	return response.Value, err
}

var (
	_ mautrix.StateStore = (*Store)(nil)
	_ mautrix.SyncStore  = (*Store)(nil)
	_ crypto.StateStore  = (*Store)(nil)
)
