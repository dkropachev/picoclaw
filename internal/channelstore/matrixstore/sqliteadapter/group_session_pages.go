package sqliteadapter

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	defaultGroupSessionSnapshotTTL = 5 * time.Minute
	maximumGroupSessionPageSize    = 256
	groupSessionSnapshotIDBytes    = 24
)

type groupSessionPageTarget struct {
	storeID       database.StoreID
	deviceID      id.DeviceID
	operation     string
	roomID        id.RoomID
	backupVersion id.KeyBackupVersion
}

type groupSessionSnapshot struct {
	target    groupSessionPageTarget
	records   []matrixstore.InboundGroupSessionRecord
	cursor    int
	expiresAt time.Time
	timer     *time.Timer
}

// groupSessionPages owns immutable, typed result snapshots inside the broker.
// Only opaque IDs and encoded Matrix records cross the process boundary.
type groupSessionPages struct {
	mu        sync.Mutex
	ttl       time.Duration
	snapshots map[matrixstore.GroupSessionSnapshotID]*groupSessionSnapshot
	closed    bool
}

func newGroupSessionPages(ttl time.Duration) *groupSessionPages {
	if ttl <= 0 {
		ttl = defaultGroupSessionSnapshotTTL
	}
	return &groupSessionPages{
		ttl:       ttl,
		snapshots: make(map[matrixstore.GroupSessionSnapshotID]*groupSessionSnapshot),
	}
}

func (pages *groupSessionPages) first(
	target groupSessionPageTarget,
	records []matrixstore.InboundGroupSessionRecord,
	limit int,
) (matrixstore.Response, error) {
	if pages == nil {
		return matrixstore.Response{}, database.NewError(
			database.CodeUnavailable, "Matrix group session snapshots are unavailable",
		)
	}
	if limit < 1 || limit > maximumGroupSessionPageSize {
		return matrixstore.Response{}, invalidGroupSessionPage()
	}
	pages.mu.Lock()
	if pages.closed {
		pages.mu.Unlock()
		return matrixstore.Response{}, database.NewError(
			database.CodeUnavailable, "Matrix group session snapshots are closed",
		)
	}
	if len(records) <= limit {
		response := matrixstore.Response{
			GroupSessions: cloneInboundGroupSessionRecords(records),
			NextCursor:    len(records),
		}
		pages.mu.Unlock()
		return response, nil
	}
	pages.mu.Unlock()
	snapshotID, err := newGroupSessionSnapshotID()
	if err != nil {
		return matrixstore.Response{}, database.NewError(
			database.CodeUnavailable, "Matrix group session snapshot could not be created",
		)
	}
	snapshot := &groupSessionSnapshot{
		target:  target,
		records: cloneInboundGroupSessionRecords(records),
	}

	pages.mu.Lock()
	defer pages.mu.Unlock()
	if pages.closed {
		return matrixstore.Response{}, database.NewError(
			database.CodeUnavailable, "Matrix group session snapshots are closed",
		)
	}
	for pages.snapshots[snapshotID] != nil {
		snapshotID, err = newGroupSessionSnapshotID()
		if err != nil {
			return matrixstore.Response{}, database.NewError(
				database.CodeUnavailable, "Matrix group session snapshot could not be created",
			)
		}
	}
	pages.snapshots[snapshotID] = snapshot
	return pages.pageLocked(snapshotID, snapshot, target, 0, limit)
}

func (pages *groupSessionPages) next(
	snapshotID matrixstore.GroupSessionSnapshotID,
	target groupSessionPageTarget,
	cursor int,
	limit int,
) (matrixstore.Response, error) {
	if pages == nil {
		return matrixstore.Response{}, database.NewError(
			database.CodeUnavailable, "Matrix group session snapshots are unavailable",
		)
	}
	if !snapshotID.Valid() || cursor < 0 || limit < 1 || limit > maximumGroupSessionPageSize {
		return matrixstore.Response{}, invalidGroupSessionPage()
	}

	pages.mu.Lock()
	defer pages.mu.Unlock()
	if pages.closed {
		return matrixstore.Response{}, database.NewError(
			database.CodeUnavailable, "Matrix group session snapshots are closed",
		)
	}
	snapshot := pages.snapshots[snapshotID]
	if snapshot == nil {
		return matrixstore.Response{}, invalidGroupSessionSnapshot()
	}
	if !time.Now().Before(snapshot.expiresAt) {
		pages.deleteLocked(snapshotID, snapshot)
		return matrixstore.Response{}, invalidGroupSessionSnapshot()
	}
	return pages.pageLocked(snapshotID, snapshot, target, cursor, limit)
}

func (pages *groupSessionPages) pageLocked(
	snapshotID matrixstore.GroupSessionSnapshotID,
	snapshot *groupSessionSnapshot,
	target groupSessionPageTarget,
	cursor int,
	limit int,
) (matrixstore.Response, error) {
	if snapshot.target != target || cursor != snapshot.cursor {
		return matrixstore.Response{}, invalidGroupSessionSnapshot()
	}
	end := min(cursor+limit, len(snapshot.records))
	response := matrixstore.Response{
		GroupSessions: cloneInboundGroupSessionRecords(snapshot.records[cursor:end]),
		NextCursor:    end,
		More:          end < len(snapshot.records),
	}
	if !response.More {
		pages.deleteLocked(snapshotID, snapshot)
		return response, nil
	}

	response.SnapshotID = snapshotID
	snapshot.cursor = end
	snapshot.expiresAt = time.Now().Add(pages.ttl)
	if snapshot.timer == nil {
		snapshot.timer = time.AfterFunc(pages.ttl, func() {
			pages.expire(snapshotID, snapshot)
		})
	} else {
		snapshot.timer.Reset(pages.ttl)
	}
	return response, nil
}

func (pages *groupSessionPages) expire(
	snapshotID matrixstore.GroupSessionSnapshotID,
	snapshot *groupSessionSnapshot,
) {
	pages.mu.Lock()
	defer pages.mu.Unlock()
	if pages.closed || pages.snapshots[snapshotID] != snapshot {
		return
	}
	if remaining := time.Until(snapshot.expiresAt); remaining > 0 {
		snapshot.timer.Reset(remaining)
		return
	}
	pages.deleteLocked(snapshotID, snapshot)
}

func (pages *groupSessionPages) deleteLocked(
	snapshotID matrixstore.GroupSessionSnapshotID,
	snapshot *groupSessionSnapshot,
) {
	delete(pages.snapshots, snapshotID)
	if snapshot.timer != nil {
		snapshot.timer.Stop()
		snapshot.timer = nil
	}
	clear(snapshot.records)
	snapshot.records = nil
}

func (pages *groupSessionPages) close() {
	if pages == nil {
		return
	}
	pages.mu.Lock()
	defer pages.mu.Unlock()
	if pages.closed {
		return
	}
	pages.closed = true
	for snapshotID, snapshot := range pages.snapshots {
		pages.deleteLocked(snapshotID, snapshot)
	}
}

func (pages *groupSessionPages) count() int {
	if pages == nil {
		return 0
	}
	pages.mu.Lock()
	defer pages.mu.Unlock()
	return len(pages.snapshots)
}

func newGroupSessionSnapshotID() (matrixstore.GroupSessionSnapshotID, error) {
	raw := make([]byte, groupSessionSnapshotIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate Matrix group session snapshot ID: %w", err)
	}
	return matrixstore.GroupSessionSnapshotID(base64.RawURLEncoding.EncodeToString(raw)), nil
}

func cloneInboundGroupSessionRecords(
	records []matrixstore.InboundGroupSessionRecord,
) []matrixstore.InboundGroupSessionRecord {
	if records == nil {
		return nil
	}
	result := make([]matrixstore.InboundGroupSessionRecord, len(records))
	copy(result, records)
	for index := range result {
		result[index].Pickle = append([]byte(nil), records[index].Pickle...)
		result[index].ForwardingChains = append([]string(nil), records[index].ForwardingChains...)
		result[index].RatchetSafety.MissedIndices = append(
			[]uint(nil), records[index].RatchetSafety.MissedIndices...,
		)
		result[index].RatchetSafety.LostIndices = append(
			[]uint(nil), records[index].RatchetSafety.LostIndices...,
		)
	}
	return result
}

func invalidGroupSessionPage() error {
	return database.NewError(database.CodeInvalid, "Matrix group session page is invalid")
}

func invalidGroupSessionSnapshot() error {
	return database.NewError(database.CodeInvalid, "Matrix group session snapshot is invalid or expired")
}
