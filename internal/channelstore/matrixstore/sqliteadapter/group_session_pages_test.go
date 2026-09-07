package sqliteadapter

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestGroupSessionPaginationUsesImmutableSnapshot(t *testing.T) {
	harness := newMatrixHarness(t)
	roomID := id.RoomID("!snapshot:matrix.test")
	original := make(map[id.SessionID]int)
	originalSessions := make([]*crypto.InboundGroupSession, 0, 3)
	for _, senderKey := range []id.SenderKey{"sender-a", "sender-b", "sender-c"} {
		session := newInboundGroupSession(t, roomID, senderKey, time.Hour, "")
		if err := harness.store.PutGroupSession(t.Context(), session); err != nil {
			t.Fatal(err)
		}
		original[session.ID()] = 1
		originalSessions = append(originalSessions, session)
	}

	response, err := requestGroupSessionPage(t, harness, "crypto.get-all-group-sessions", matrixstore.Request{
		Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.More || !response.SnapshotID.Valid() || response.NextCursor != 1 ||
		len(response.GroupSessions) != 1 || harness.handler.pages.count() != 1 {
		t.Fatalf("first snapshot page = %#v, retained = %d", response, harness.handler.pages.count())
	}
	received := []id.SessionID{decodeGroupSessionID(t, response.GroupSessions[0])}

	var redacted id.SessionID
	for _, session := range originalSessions {
		if session.ID() != received[0] {
			redacted = session.ID()
			break
		}
	}
	if err = harness.store.RedactGroupSession(t.Context(), roomID, redacted, "snapshot test"); err != nil {
		t.Fatal(err)
	}
	inserted := newInboundGroupSession(t, roomID, "sender-after-snapshot", time.Hour, "")
	if err = harness.store.PutGroupSession(t.Context(), inserted); err != nil {
		t.Fatal(err)
	}

	snapshotID := response.SnapshotID
	for response.More {
		response, err = requestGroupSessionPage(t, harness, "crypto.get-all-group-sessions", matrixstore.Request{
			SnapshotID: snapshotID,
			Cursor:     response.NextCursor,
			Limit:      1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if response.More && response.SnapshotID != snapshotID {
			t.Fatalf("continuation changed snapshot ID: %q", response.SnapshotID)
		}
		for _, record := range response.GroupSessions {
			received = append(received, decodeGroupSessionID(t, record))
		}
	}
	if response.SnapshotID != "" || harness.handler.pages.count() != 0 {
		t.Fatalf("final snapshot page retained state: %#v, retained = %d", response, harness.handler.pages.count())
	}
	counts := make(map[id.SessionID]int, len(received))
	for _, sessionID := range received {
		counts[sessionID]++
	}
	if len(received) != len(original) {
		t.Fatalf("snapshot returned %d sessions, want %d: %v", len(received), len(original), received)
	}
	for sessionID := range original {
		if counts[sessionID] != 1 {
			t.Fatalf("original session %s returned %d times: %v", sessionID, counts[sessionID], received)
		}
	}
	if counts[inserted.ID()] != 0 {
		t.Fatalf("post-snapshot session %s leaked into result", inserted.ID())
	}
}

func TestGroupSessionSnapshotRejectsInvalidContinuation(t *testing.T) {
	harness := newMatrixHarness(t)
	roomID := id.RoomID("!validation:matrix.test")
	for _, senderKey := range []id.SenderKey{"sender-a", "sender-b"} {
		if err := harness.store.PutGroupSession(
			t.Context(), newInboundGroupSession(t, roomID, senderKey, time.Hour, ""),
		); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := harness.handler.bundle(t.Context(), harness.storeID, testDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	firstValue, err := dispatch(
		t.Context(), "crypto.get-all-group-sessions", matrixstore.Request{Limit: 1}, bundle,
	)
	if err != nil {
		t.Fatal(err)
	}
	first := firstValue.(matrixstore.Response)
	if !first.More || !first.SnapshotID.Valid() {
		t.Fatalf("first page = %#v", first)
	}

	assertInvalid := func(name, operation string, request matrixstore.Request, target *storeBundle) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if _, dispatchErr := dispatch(
				t.Context(), operation, request, target,
			); database.CodeOf(dispatchErr) != database.CodeInvalid {
				t.Fatalf("continuation error = %v", dispatchErr)
			}
		})
	}
	continuation := matrixstore.Request{
		SnapshotID: first.SnapshotID,
		Cursor:     first.NextCursor,
		Limit:      1,
	}
	assertInvalid("malformed token", "crypto.get-all-group-sessions", matrixstore.Request{
		SnapshotID: "not-a-canonical-snapshot-id", Cursor: first.NextCursor, Limit: 1,
	}, bundle)
	unknownID, err := newGroupSessionSnapshotID()
	if err != nil {
		t.Fatal(err)
	}
	assertInvalid("unknown token", "crypto.get-all-group-sessions", matrixstore.Request{
		SnapshotID: unknownID, Cursor: first.NextCursor, Limit: 1,
	}, bundle)
	assertInvalid("operation", "crypto.get-group-sessions-for-room", matrixstore.Request{
		SnapshotID: first.SnapshotID, Cursor: first.NextCursor, Limit: 1, RoomID: roomID,
	}, bundle)
	otherStore := *bundle
	otherStore.storeID = "channel/matrix/other"
	assertInvalid("store", "crypto.get-all-group-sessions", continuation, &otherStore)
	assertInvalid("cursor", "crypto.get-all-group-sessions", matrixstore.Request{
		SnapshotID: first.SnapshotID, Cursor: first.NextCursor + 1, Limit: 1,
	}, bundle)
	assertInvalid("oversized page", "crypto.get-all-group-sessions", matrixstore.Request{
		SnapshotID: first.SnapshotID, Cursor: first.NextCursor, Limit: maximumGroupSessionPageSize + 1,
	}, bundle)
	assertInvalid("cursor without token", "crypto.get-all-group-sessions", matrixstore.Request{
		Cursor: 1, Limit: 1,
	}, bundle)

	finalValue, err := dispatch(t.Context(), "crypto.get-all-group-sessions", continuation, bundle)
	if err != nil {
		t.Fatal(err)
	}
	final := finalValue.(matrixstore.Response)
	if final.More || final.SnapshotID != "" || final.NextCursor != 2 || bundle.pages.count() != 0 {
		t.Fatalf("final page = %#v, retained = %d", final, bundle.pages.count())
	}
	assertInvalid("reused final token", "crypto.get-all-group-sessions", continuation, bundle)

	roomFirstValue, err := dispatch(t.Context(), "crypto.get-group-sessions-for-room", matrixstore.Request{
		RoomID: roomID,
		Limit:  1,
	}, bundle)
	if err != nil {
		t.Fatal(err)
	}
	roomFirst := roomFirstValue.(matrixstore.Response)
	roomContinuation := matrixstore.Request{
		SnapshotID: roomFirst.SnapshotID,
		Cursor:     roomFirst.NextCursor,
		Limit:      1,
		RoomID:     "!different:matrix.test",
	}
	assertInvalid("query target", "crypto.get-group-sessions-for-room", roomContinuation, bundle)
	roomContinuation.RoomID = roomID
	if _, err = dispatch(
		t.Context(), "crypto.get-group-sessions-for-room", roomContinuation, bundle,
	); err != nil {
		t.Fatalf("valid room continuation error = %v", err)
	}
}

func TestGroupSessionSnapshotExpirationAndHandlerClose(t *testing.T) {
	t.Run("expiration", func(t *testing.T) {
		pages := newGroupSessionPages(5 * time.Millisecond)
		target := groupSessionPageTarget{
			storeID: "channel/matrix/expiration", deviceID: "DEVICE",
			operation: "crypto.get-all-group-sessions",
		}
		first, err := pages.first(target, make([]matrixstore.InboundGroupSessionRecord, 2), 1)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for pages.count() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if pages.count() != 0 {
			t.Fatal("expired snapshot was not cleaned up")
		}
		if _, err = pages.next(
			first.SnapshotID, target, first.NextCursor, 1,
		); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("expired continuation error = %v", err)
		}
		pages.close()
	})

	t.Run("handler close", func(t *testing.T) {
		harness := newMatrixHarness(t)
		roomID := id.RoomID("!close:matrix.test")
		for _, senderKey := range []id.SenderKey{"sender-a", "sender-b"} {
			if err := harness.store.PutGroupSession(
				t.Context(), newInboundGroupSession(t, roomID, senderKey, time.Hour, ""),
			); err != nil {
				t.Fatal(err)
			}
		}
		first, err := requestGroupSessionPage(t, harness, "crypto.get-all-group-sessions", matrixstore.Request{
			Limit: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		pages := harness.handler.pages
		if pages.count() != 1 {
			t.Fatalf("retained snapshots before close = %d", pages.count())
		}
		if err = harness.handler.Close(); err != nil {
			t.Fatal(err)
		}
		if pages.count() != 0 {
			t.Fatalf("retained snapshots after close = %d", pages.count())
		}
		target := groupSessionPageTarget{
			storeID: harness.storeID, deviceID: testDeviceID,
			operation: "crypto.get-all-group-sessions",
		}
		if _, err = pages.next(
			first.SnapshotID,
			target,
			first.NextCursor,
			1,
		); database.CodeOf(
			err,
		) != database.CodeUnavailable {
			t.Fatalf("closed continuation error = %v", err)
		}
		pages.close()
		(*groupSessionPages)(nil).close()
	})
}

func TestGroupSessionPagesBoundaryAndHelperPaths(t *testing.T) {
	target := groupSessionPageTarget{
		storeID:   "channel/matrix/helpers",
		deviceID:  "DEVICE",
		operation: "crypto.get-all-group-sessions",
	}

	var unavailable *groupSessionPages
	if _, err := unavailable.first(target, nil, 1); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil first error = %v", err)
	}
	if _, err := unavailable.next("", target, 0, 1); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil next error = %v", err)
	}
	if got := unavailable.count(); got != 0 {
		t.Fatalf("nil count = %d", got)
	}
	if clone := cloneInboundGroupSessionRecords(nil); clone != nil {
		t.Fatalf("nil clone = %#v", clone)
	}

	pages := newGroupSessionPages(0)
	if pages.ttl != defaultGroupSessionSnapshotTTL {
		t.Fatalf("default ttl = %v", pages.ttl)
	}
	if _, err := pages.first(target, nil, 0); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("zero-limit first error = %v", err)
	}
	records := []matrixstore.InboundGroupSessionRecord{{
		Pickle:           []byte("pickle"),
		ForwardingChains: []string{"forwarder"},
		RatchetSafety: crypto.RatchetSafety{
			MissedIndices: []uint{3},
			LostIndices:   []uint{7},
		},
	}}
	response, err := pages.first(target, records, 1)
	if err != nil {
		t.Fatal(err)
	}
	if response.More || response.SnapshotID != "" || response.NextCursor != 1 || len(response.GroupSessions) != 1 {
		t.Fatalf("single-page response = %#v", response)
	}
	response.GroupSessions[0].Pickle[0] = 'P'
	response.GroupSessions[0].ForwardingChains[0] = "changed"
	response.GroupSessions[0].RatchetSafety.MissedIndices[0] = 30
	response.GroupSessions[0].RatchetSafety.LostIndices[0] = 70
	if string(records[0].Pickle) != "pickle" || records[0].ForwardingChains[0] != "forwarder" ||
		records[0].RatchetSafety.MissedIndices[0] != 3 || records[0].RatchetSafety.LostIndices[0] != 7 {
		t.Fatalf("clone mutated source = %#v", records[0])
	}

	snapshotID, err := newGroupSessionSnapshotID()
	if err != nil || !snapshotID.Valid() {
		t.Fatalf("snapshot ID = %q, %v", snapshotID, err)
	}
	pages.close()
	pages.close()
	if _, err = pages.first(target, records, 1); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed first error = %v", err)
	}
	if _, err = pages.next(snapshotID, target, 0, 1); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed next error = %v", err)
	}
}

func TestGroupSessionPagesExplicitExpirationPaths(t *testing.T) {
	target := groupSessionPageTarget{
		storeID:   "channel/matrix/expire-paths",
		deviceID:  "DEVICE",
		operation: "crypto.get-all-group-sessions",
	}
	records := make([]matrixstore.InboundGroupSessionRecord, 2)

	t.Run("next observes expired snapshot", func(t *testing.T) {
		pages := newGroupSessionPages(time.Hour)
		first, err := pages.first(target, records, 1)
		if err != nil {
			t.Fatal(err)
		}
		pages.mu.Lock()
		snapshot := pages.snapshots[first.SnapshotID]
		snapshot.expiresAt = time.Now().Add(-time.Second)
		pages.mu.Unlock()
		if _, err = pages.next(
			first.SnapshotID,
			target,
			first.NextCursor,
			1,
		); database.CodeOf(
			err,
		) != database.CodeInvalid {
			t.Fatalf("expired next error = %v", err)
		}
		if pages.count() != 0 || snapshot.records != nil || snapshot.timer != nil {
			t.Fatalf("expired snapshot was not cleared: count=%d snapshot=%#v", pages.count(), snapshot)
		}
	})

	t.Run("expiry callback reschedules then deletes", func(t *testing.T) {
		pages := newGroupSessionPages(time.Hour)
		first, err := pages.first(target, records, 1)
		if err != nil {
			t.Fatal(err)
		}
		pages.mu.Lock()
		snapshot := pages.snapshots[first.SnapshotID]
		pages.mu.Unlock()
		pages.expire(first.SnapshotID, snapshot)
		if pages.count() != 1 {
			t.Fatal("unexpired snapshot was deleted")
		}
		pages.mu.Lock()
		snapshot.expiresAt = time.Now().Add(-time.Second)
		pages.mu.Unlock()
		pages.expire(first.SnapshotID, snapshot)
		if pages.count() != 0 {
			t.Fatal("expired snapshot was retained")
		}
		pages.expire(first.SnapshotID, snapshot)
	})
}

func requestGroupSessionPage(
	t *testing.T,
	harness *matrixHarness,
	operation string,
	request matrixstore.Request,
) (matrixstore.Response, error) {
	t.Helper()
	request.StoreID = harness.storeID
	request.DeviceID = testDeviceID
	var response matrixstore.Response
	err := harness.client.Call(
		t.Context(), matrixstore.Domain, matrixstore.Version, operation, request, &response,
	)
	return response, err
}

func decodeGroupSessionID(
	t *testing.T,
	record matrixstore.InboundGroupSessionRecord,
) id.SessionID {
	t.Helper()
	session, err := matrixstore.DecodeInboundGroupSession(&record, []byte(testPickleKey))
	if err != nil {
		t.Fatal(err)
	}
	return session.ID()
}
