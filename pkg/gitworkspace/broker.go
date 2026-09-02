package gitworkspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	InventoryStoreID database.StoreID = "global.git-workspace-inventory"
	BrokerDomain                      = "git-workspace-inventory"
	BrokerVersion                     = 1

	inventoryOperationPreflight    = "preflight"
	inventoryOperationAcquireLease = "acquire-lease"
	inventoryOperationRenewLease   = "renew-lease"
	inventoryOperationReleaseLease = "release-lease"
	inventoryOperationLoadChunk    = "load-chunk"
	inventoryOperationSaveBegin    = "save-begin"
	inventoryOperationSaveChunk    = "save-chunk"
	inventoryOperationSaveCommit   = "save-commit"

	inventoryBrokerChunkBytes        = 256 << 10
	inventoryBrokerMaximumStateBytes = 64 << 20
)

const defaultInventoryBrokerLeaseTTL = 30 * time.Second

type inventoryBrokerTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type inventoryBrokerLeaseRequest struct {
	StoreID database.StoreID `json:"store_id"`
	LeaseID string           `json:"lease_id"`
}

type inventoryBrokerLoadRequest struct {
	StoreID database.StoreID `json:"store_id"`
	LeaseID string           `json:"lease_id"`
	Offset  int              `json:"offset"`
}

type inventoryBrokerSaveBeginRequest struct {
	StoreID            database.StoreID `json:"store_id"`
	LeaseID            string           `json:"lease_id"`
	ExpectedGeneration int64            `json:"expected_generation"`
	TotalBytes         int              `json:"total_bytes"`
	Digest             string           `json:"digest"`
}

type inventoryBrokerSaveChunkRequest struct {
	StoreID database.StoreID `json:"store_id"`
	LeaseID string           `json:"lease_id"`
	Offset  int              `json:"offset"`
	Data    []byte           `json:"data"`
}

type inventoryBrokerSaveCommitRequest struct {
	StoreID            database.StoreID `json:"store_id"`
	LeaseID            string           `json:"lease_id"`
	ExpectedGeneration int64            `json:"expected_generation"`
	TotalBytes         int              `json:"total_bytes"`
	Digest             string           `json:"digest"`
}

type inventoryBrokerMutationResponse struct {
	Updated bool `json:"updated"`
}

type inventoryBrokerLeaseResponse struct {
	LeaseID        string `json:"lease_id"`
	TTLNanoSeconds int64  `json:"ttl_nanoseconds"`
}

type inventoryBrokerLoadResponse struct {
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalBytes int    `json:"total_bytes"`
	Generation int64  `json:"generation"`
	Digest     string `json:"digest"`
	Data       []byte `json:"data"`
	Done       bool   `json:"done"`
}

type inventoryBrokerSaveResponse struct {
	NextOffset int   `json:"next_offset"`
	Generation int64 `json:"generation"`
	Updated    bool  `json:"updated"`
}

type inventoryBrokerLease struct {
	id         string
	timer      *time.Timer
	timerEpoch uint64
	inflight   int

	loadData       []byte
	loadGeneration int64
	loadDigest     string
	loadNext       int

	saveData       []byte
	saveExpected   int64
	saveTotal      int
	saveDigest     string
	saveNext       int
	saveCommitting bool
}

// BrokerHandler owns the single retained pool and exclusive inventory lease
// for the trusted git-workspace root loaded from configuration.
type BrokerHandler struct {
	manager  *Manager
	database *sql.DB
	storeID  database.StoreID
	leaseTTL time.Duration

	mu        sync.Mutex
	lease     *inventoryBrokerLease
	changed   chan struct{}
	closed    bool
	closeOnce sync.Once
	closeErr  error

	loadChunkCalls int
	saveChunkCalls int
}

// NewBrokerHandler constructs the sole online inventory provider. Callers may
// choose only a canonical home and validated configuration; no request can
// submit a database path or alternate logical identity.
func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"git workspace inventory broker requires online database fencing",
		)
	}
	root, err := configuredInventoryRoot(home, cfg)
	if err != nil {
		return nil, database.NewError(
			database.CodeInvalid,
			"git workspace inventory configuration is invalid",
		)
	}
	manager, err := prepareManager(Options{RootDir: root})
	if err != nil {
		return nil, mapInventoryBrokerError(err)
	}
	databaseHandle, err := manager.openInventoryDatabase(context.Background())
	if err != nil {
		return nil, mapInventoryBrokerError(err)
	}
	return &BrokerHandler{
		manager:  manager,
		database: databaseHandle,
		storeID:  InventoryStoreID,
		leaseTTL: defaultInventoryBrokerLeaseTTL,
		changed:  make(chan struct{}),
	}, nil
}

func configuredInventoryRoot(home string, cfg *config.Config) (string, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	workspace := strings.TrimSpace(cfg.Agents.Defaults.Workspace)
	if workspace == "" {
		workspace = filepath.Join(canonicalHome, "workspace")
	} else {
		workspace, err = expandInventoryConfiguredPath(canonicalHome, workspace)
		if err != nil {
			return "", err
		}
	}
	root := cfg.GitWorkspaces.EffectiveRootDir(workspace)
	return expandInventoryConfiguredPath(canonicalHome, root)
}

func expandInventoryConfiguredPath(home, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("configured path is invalid")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(home, value)
	}
	return filepath.Abs(filepath.Clean(value))
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != BrokerDomain || request.Version != BrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(
			database.CodeDeadline,
			"git workspace inventory request deadline was exceeded",
		)
	}
	switch request.Operation {
	case inventoryOperationPreflight:
		var input inventoryBrokerTarget
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		if err := handler.available(); err != nil {
			return nil, err
		}
		return inventoryBrokerMutationResponse{Updated: true}, nil
	case inventoryOperationAcquireLease:
		var input inventoryBrokerTarget
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.acquireLease(ctx)
	case inventoryOperationRenewLease:
		var input inventoryBrokerLeaseRequest
		if request.DecodePayload(&input) != nil || !handler.validLeaseRequest(input) {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.renewLease(input.LeaseID)
	case inventoryOperationReleaseLease:
		var input inventoryBrokerLeaseRequest
		if request.DecodePayload(&input) != nil || !handler.validLeaseRequest(input) {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.releaseLease(input.LeaseID)
	case inventoryOperationLoadChunk:
		var input inventoryBrokerLoadRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			input.LeaseID == "" || input.Offset < 0 {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.loadChunk(ctx, input)
	case inventoryOperationSaveBegin:
		var input inventoryBrokerSaveBeginRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			input.LeaseID == "" || input.ExpectedGeneration < 0 ||
			input.TotalBytes < 1 || input.TotalBytes > inventoryBrokerMaximumStateBytes ||
			!validInventoryDigest(input.Digest) {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.beginSave(input)
	case inventoryOperationSaveChunk:
		var input inventoryBrokerSaveChunkRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			input.LeaseID == "" || input.Offset < 0 || len(input.Data) < 1 ||
			len(input.Data) > inventoryBrokerChunkBytes {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.saveChunk(input)
	case inventoryOperationSaveCommit:
		var input inventoryBrokerSaveCommitRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			input.LeaseID == "" || input.ExpectedGeneration < 0 || input.TotalBytes < 1 ||
			input.TotalBytes > inventoryBrokerMaximumStateBytes || !validInventoryDigest(input.Digest) {
			return nil, database.NewError(
				database.CodeInvalid,
				"git workspace inventory request is invalid",
			)
		}
		return handler.commitSave(ctx, input)
	default:
		return nil, database.NewError(
			database.CodeUnsupported,
			"git workspace inventory operation is unsupported",
		)
	}
}

func (handler *BrokerHandler) available() error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed || handler.database == nil {
		return database.NewError(
			database.CodeUnavailable,
			"git workspace inventory broker is unavailable",
		)
	}
	return nil
}

func (handler *BrokerHandler) validLeaseRequest(input inventoryBrokerLeaseRequest) bool {
	return input.StoreID == handler.storeID && input.LeaseID != ""
}

func (handler *BrokerHandler) effectiveLeaseTTL() time.Duration {
	if handler == nil || handler.leaseTTL <= 0 {
		return defaultInventoryBrokerLeaseTTL
	}
	return handler.leaseTTL
}

func (handler *BrokerHandler) acquireLease(ctx context.Context) (any, error) {
	for {
		handler.mu.Lock()
		if handler.closed || handler.database == nil {
			handler.mu.Unlock()
			return nil, database.NewError(
				database.CodeUnavailable,
				"git workspace inventory broker is unavailable",
			)
		}
		if handler.lease == nil {
			id, err := newInventoryLeaseID()
			if err != nil {
				handler.mu.Unlock()
				return nil, database.NewError(
					database.CodeInternal,
					"git workspace inventory lease identity failed",
				)
			}
			lease := &inventoryBrokerLease{id: id}
			handler.lease = lease
			handler.armLeaseLocked(lease)
			ttl := handler.effectiveLeaseTTL()
			handler.mu.Unlock()
			return inventoryBrokerLeaseResponse{LeaseID: id, TTLNanoSeconds: int64(ttl)}, nil
		}
		changed := handler.changed
		handler.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, database.NewError(
				database.CodeDeadline,
				"git workspace inventory lease deadline was exceeded",
			)
		case <-changed:
		}
	}
}

func (handler *BrokerHandler) renewLease(id string) (any, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed || handler.lease == nil || handler.lease.id != id {
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory lease is unavailable",
		)
	}
	handler.armLeaseLocked(handler.lease)
	return inventoryBrokerLeaseResponse{
		LeaseID: id, TTLNanoSeconds: int64(handler.effectiveLeaseTTL()),
	}, nil
}

func (handler *BrokerHandler) releaseLease(id string) (any, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed || handler.lease == nil || handler.lease.id != id {
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory lease is unavailable",
		)
	}
	handler.clearLeaseLocked()
	return inventoryBrokerMutationResponse{Updated: true}, nil
}

func (handler *BrokerHandler) beginLeaseOperation(id string) (*inventoryBrokerLease, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed || handler.lease == nil || handler.lease.id != id {
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory lease is unavailable",
		)
	}
	handler.lease.inflight++
	handler.armLeaseLocked(handler.lease)
	return handler.lease, nil
}

func (handler *BrokerHandler) endLeaseOperation(lease *inventoryBrokerLease) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.lease != lease {
		return
	}
	if lease.inflight > 0 {
		lease.inflight--
	}
	handler.armLeaseLocked(lease)
}

func (handler *BrokerHandler) loadChunk(
	ctx context.Context,
	input inventoryBrokerLoadRequest,
) (any, error) {
	if input.Offset == 0 {
		lease, err := handler.beginLeaseOperation(input.LeaseID)
		if err != nil {
			return nil, err
		}
		state, loadErr := loadInventoryState(ctx, handler.database)
		var encoded []byte
		if loadErr == nil {
			encoded, loadErr = json.Marshal(state)
		}
		handler.endLeaseOperation(lease)
		if loadErr != nil {
			return nil, mapInventoryBrokerError(loadErr)
		}
		if len(encoded) == 0 || len(encoded) > inventoryBrokerMaximumStateBytes {
			return nil, database.NewError(
				database.CodeIntegrity,
				"git workspace inventory snapshot exceeds its limit",
			)
		}
		digest := sha256.Sum256(encoded)
		handler.mu.Lock()
		if handler.lease != lease {
			handler.mu.Unlock()
			return nil, database.NewError(
				database.CodeConflict,
				"git workspace inventory lease is unavailable",
			)
		}
		lease.loadData = encoded
		lease.loadGeneration = state.generation
		lease.loadDigest = hex.EncodeToString(digest[:])
		lease.loadNext = 0
		handler.mu.Unlock()
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	lease := handler.lease
	if handler.closed || lease == nil || lease.id != input.LeaseID || len(lease.loadData) == 0 ||
		input.Offset != lease.loadNext {
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory snapshot is unavailable",
		)
	}
	end := input.Offset + inventoryBrokerChunkBytes
	if end > len(lease.loadData) {
		end = len(lease.loadData)
	}
	chunk := append([]byte(nil), lease.loadData[input.Offset:end]...)
	lease.loadNext = end
	handler.loadChunkCalls++
	handler.armLeaseLocked(lease)
	response := inventoryBrokerLoadResponse{
		Offset: input.Offset, NextOffset: end, TotalBytes: len(lease.loadData),
		Generation: lease.loadGeneration, Digest: lease.loadDigest, Data: chunk,
		Done: end == len(lease.loadData),
	}
	if response.Done {
		lease.loadData = nil
		lease.loadNext = 0
	}
	return response, nil
}

func (handler *BrokerHandler) beginSave(input inventoryBrokerSaveBeginRequest) (any, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	lease := handler.lease
	if handler.closed || lease == nil || lease.id != input.LeaseID {
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory lease is unavailable",
		)
	}
	lease.saveData = make([]byte, 0, input.TotalBytes)
	lease.saveExpected = input.ExpectedGeneration
	lease.saveTotal = input.TotalBytes
	lease.saveDigest = input.Digest
	lease.saveNext = 0
	lease.saveCommitting = false
	handler.armLeaseLocked(lease)
	return inventoryBrokerSaveResponse{NextOffset: 0, Generation: input.ExpectedGeneration}, nil
}

func (handler *BrokerHandler) saveChunk(input inventoryBrokerSaveChunkRequest) (any, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	lease := handler.lease
	if handler.closed || lease == nil || lease.id != input.LeaseID || lease.saveData == nil ||
		lease.saveCommitting || input.Offset != lease.saveNext ||
		len(lease.saveData)+len(input.Data) > lease.saveTotal {
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory upload is unavailable",
		)
	}
	lease.saveData = append(lease.saveData, input.Data...)
	lease.saveNext += len(input.Data)
	handler.saveChunkCalls++
	handler.armLeaseLocked(lease)
	return inventoryBrokerSaveResponse{
		NextOffset: lease.saveNext, Generation: lease.saveExpected,
	}, nil
}

func (handler *BrokerHandler) commitSave(
	ctx context.Context,
	input inventoryBrokerSaveCommitRequest,
) (any, error) {
	handler.mu.Lock()
	lease := handler.lease
	if handler.closed || lease == nil || lease.id != input.LeaseID || lease.saveData == nil ||
		lease.saveCommitting || input.ExpectedGeneration != lease.saveExpected ||
		input.TotalBytes != lease.saveTotal || input.Digest != lease.saveDigest ||
		lease.saveNext != lease.saveTotal {
		handler.mu.Unlock()
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory upload is unavailable",
		)
	}
	lease.saveCommitting = true
	lease.inflight++
	handler.armLeaseLocked(lease)
	encoded := append([]byte(nil), lease.saveData...)
	handler.mu.Unlock()

	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != input.Digest {
		handler.finishSave(lease, false)
		return nil, database.NewError(
			database.CodeIntegrity,
			"git workspace inventory upload digest does not match",
		)
	}
	var state storeState
	if err := json.Unmarshal(encoded, &state); err != nil {
		handler.finishSave(lease, false)
		return nil, database.NewError(
			database.CodeInvalid,
			"git workspace inventory upload is invalid",
		)
	}
	normalizeBrokerInventoryState(&state)
	state.generation = input.ExpectedGeneration
	if err := saveInventoryState(ctx, handler.database, &state); err != nil {
		handler.finishSave(lease, false)
		return nil, mapInventoryBrokerError(err)
	}
	handler.finishSave(lease, true)
	return inventoryBrokerSaveResponse{
		NextOffset: input.TotalBytes, Generation: input.ExpectedGeneration + 1, Updated: true,
	}, nil
}

func (handler *BrokerHandler) finishSave(lease *inventoryBrokerLease, committed bool) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.lease != lease {
		return
	}
	if lease.inflight > 0 {
		lease.inflight--
	}
	lease.saveData = nil
	lease.saveNext = 0
	lease.saveCommitting = false
	if committed {
		lease.loadData = nil
		lease.loadNext = 0
	}
	handler.armLeaseLocked(lease)
}

func (handler *BrokerHandler) armLeaseLocked(lease *inventoryBrokerLease) {
	lease.timerEpoch++
	epoch := lease.timerEpoch
	if lease.timer != nil {
		lease.timer.Stop()
	}
	lease.timer = time.AfterFunc(handler.effectiveLeaseTTL(), func() {
		handler.expireLease(lease, epoch)
	})
}

func (handler *BrokerHandler) expireLease(lease *inventoryBrokerLease, epoch uint64) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.lease != lease || lease.timerEpoch != epoch {
		return
	}
	if lease.inflight > 0 {
		handler.armLeaseLocked(lease)
		return
	}
	handler.clearLeaseLocked()
}

func (handler *BrokerHandler) clearLeaseLocked() {
	if handler.lease != nil && handler.lease.timer != nil {
		handler.lease.timer.Stop()
	}
	handler.lease = nil
	close(handler.changed)
	handler.changed = make(chan struct{})
}

// Close expires every lease and closes the retained provider pool once.
func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		handler.closed = true
		if handler.lease != nil {
			handler.clearLeaseLocked()
		} else {
			close(handler.changed)
			handler.changed = make(chan struct{})
		}
		databaseHandle := handler.database
		handler.database = nil
		handler.mu.Unlock()
		if databaseHandle != nil {
			handler.closeErr = databaseHandle.Close()
		}
	})
	return handler.closeErr
}

func newInventoryLeaseID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validInventoryDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func mapInventoryBrokerError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(
			database.CodeDeadline,
			"git workspace inventory request deadline was exceeded",
		)
	case errors.Is(err, errInventoryGenerationConflict):
		return database.NewError(
			database.CodeConflict,
			"git workspace inventory generation changed",
		)
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(
			database.CodeUnsupported,
			"git workspace inventory schema is newer than supported",
		)
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(
			database.CodeIntegrity,
			"git workspace inventory integrity validation failed",
		)
	default:
		return database.NewError(database.CodeInternal, "git workspace inventory operation failed")
	}
}

func (m *Manager) brokerPreflight(ctx context.Context) error {
	if m == nil || m.broker == nil || m.storeID != InventoryStoreID {
		return database.NewError(
			database.CodeUnavailable,
			"git workspace inventory broker is unavailable",
		)
	}
	var response inventoryBrokerMutationResponse
	err := m.broker.Call(
		ctx, BrokerDomain, BrokerVersion, inventoryOperationPreflight,
		inventoryBrokerTarget{StoreID: m.storeID}, &response,
	)
	if err == nil && !response.Updated {
		return database.NewError(
			database.CodeIntegrity,
			"git workspace inventory preflight response is invalid",
		)
	}
	return err
}

type inventoryBrokerClientLease struct {
	id   string
	ttl  time.Duration
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func (m *Manager) lockBrokerInventory(ctx context.Context) (func(), error) {
	if m == nil || m.broker == nil || m.storeID != InventoryStoreID {
		return nil, database.NewError(
			database.CodeUnavailable,
			"git workspace inventory broker is unavailable",
		)
	}
	m.brokerLeaseMu.Lock()
	if m.brokerLeaseErr != nil {
		err := m.brokerLeaseErr
		m.brokerLeaseErr = nil
		m.brokerLeaseMu.Unlock()
		return nil, err
	}
	if m.brokerLease != nil {
		m.brokerLeaseMu.Unlock()
		return nil, database.NewError(
			database.CodeConflict,
			"git workspace inventory lease is already held",
		)
	}
	m.brokerLeaseMu.Unlock()

	var response inventoryBrokerLeaseResponse
	err := m.broker.CallWithOptions(
		ctx, BrokerDomain, BrokerVersion, inventoryOperationAcquireLease,
		inventoryBrokerTarget{StoreID: m.storeID}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return nil, err
	}
	if response.LeaseID == "" || response.TTLNanoSeconds <= 0 {
		return nil, database.NewError(
			database.CodeIntegrity,
			"git workspace inventory lease response is invalid",
		)
	}
	lease := &inventoryBrokerClientLease{
		id: response.LeaseID, ttl: time.Duration(response.TTLNanoSeconds),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	m.brokerLeaseMu.Lock()
	m.brokerLease = lease
	m.brokerLeaseMu.Unlock()
	go m.renewBrokerInventoryLease(lease)
	return func() { m.releaseBrokerInventoryLease(lease) }, nil
}

func (m *Manager) renewBrokerInventoryLease(lease *inventoryBrokerClientLease) {
	defer close(lease.done)
	interval := lease.ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-lease.stop:
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), min(interval, 5*time.Second))
		var response inventoryBrokerLeaseResponse
		err := m.broker.CallWithOptions(
			ctx, BrokerDomain, BrokerVersion, inventoryOperationRenewLease,
			inventoryBrokerLeaseRequest{StoreID: m.storeID, LeaseID: lease.id}, &response,
			database.CallOptions{Mutation: true},
		)
		cancel()
		if err == nil && (response.LeaseID != lease.id || response.TTLNanoSeconds <= 0) {
			err = database.NewError(
				database.CodeIntegrity,
				"git workspace inventory lease renewal response is invalid",
			)
		}
		if err != nil {
			switch database.CodeOf(err) {
			case database.CodeConflict, database.CodeUnauthorized, database.CodeInvalid,
				database.CodeIntegrity, database.CodeUnsupported:
				m.recordBrokerInventoryLeaseError(err)
				return
			}
		}
		timer.Reset(interval)
	}
}

func (m *Manager) releaseBrokerInventoryLease(lease *inventoryBrokerClientLease) {
	lease.once.Do(func() {
		close(lease.stop)
		<-lease.done
		ctx, cancel := context.WithTimeout(context.Background(), min(lease.ttl/2, 5*time.Second))
		defer cancel()
		var response inventoryBrokerMutationResponse
		err := m.broker.CallWithOptions(
			ctx, BrokerDomain, BrokerVersion, inventoryOperationReleaseLease,
			inventoryBrokerLeaseRequest{StoreID: m.storeID, LeaseID: lease.id}, &response,
			database.CallOptions{Mutation: true},
		)
		if err == nil && !response.Updated {
			err = database.NewError(
				database.CodeIntegrity,
				"git workspace inventory lease release response is invalid",
			)
		}
		m.brokerLeaseMu.Lock()
		if m.brokerLease == lease {
			m.brokerLease = nil
		}
		if err != nil && m.brokerLeaseErr == nil {
			m.brokerLeaseErr = err
		}
		m.brokerLeaseMu.Unlock()
	})
}

func (m *Manager) recordBrokerInventoryLeaseError(err error) {
	if err == nil {
		return
	}
	m.brokerLeaseMu.Lock()
	if m.brokerLeaseErr == nil {
		m.brokerLeaseErr = err
	}
	m.brokerLeaseMu.Unlock()
}

func (m *Manager) brokerInventoryLeaseID() (string, error) {
	m.brokerLeaseMu.Lock()
	defer m.brokerLeaseMu.Unlock()
	if m.brokerLeaseErr != nil {
		return "", m.brokerLeaseErr
	}
	if m.brokerLease == nil || m.brokerLease.id == "" {
		return "", database.NewError(
			database.CodeConflict,
			"git workspace inventory lease is unavailable",
		)
	}
	return m.brokerLease.id, nil
}

func (m *Manager) loadBrokerInventory(ctx context.Context) (*storeState, error) {
	leaseID, err := m.brokerInventoryLeaseID()
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 0)
	var generation int64
	var total int
	var digest string
	for offset := 0; ; {
		var response inventoryBrokerLoadResponse
		err := m.broker.Call(
			ctx, BrokerDomain, BrokerVersion, inventoryOperationLoadChunk,
			inventoryBrokerLoadRequest{StoreID: m.storeID, LeaseID: leaseID, Offset: offset},
			&response,
		)
		if err != nil {
			return nil, err
		}
		if response.Offset != offset || response.NextOffset != offset+len(response.Data) ||
			len(response.Data) < 1 || len(response.Data) > inventoryBrokerChunkBytes ||
			response.TotalBytes < 1 || response.TotalBytes > inventoryBrokerMaximumStateBytes ||
			response.NextOffset > response.TotalBytes || !validInventoryDigest(response.Digest) {
			return nil, database.NewError(
				database.CodeIntegrity,
				"git workspace inventory snapshot response is invalid",
			)
		}
		if offset == 0 {
			total, generation, digest = response.TotalBytes, response.Generation, response.Digest
			if generation < 0 {
				return nil, database.NewError(
					database.CodeIntegrity,
					"git workspace inventory generation is invalid",
				)
			}
		} else if response.TotalBytes != total ||
			response.Generation != generation || response.Digest != digest {
			return nil, database.NewError(
				database.CodeIntegrity,
				"git workspace inventory snapshot changed during pagination",
			)
		}
		encoded = append(encoded, response.Data...)
		offset = response.NextOffset
		if response.Done {
			if offset != total {
				return nil, database.NewError(
					database.CodeIntegrity,
					"git workspace inventory snapshot response is incomplete",
				)
			}
			break
		}
		if offset >= total {
			return nil, database.NewError(
				database.CodeIntegrity,
				"git workspace inventory snapshot response is invalid",
			)
		}
	}
	actual := sha256.Sum256(encoded)
	if hex.EncodeToString(actual[:]) != digest {
		return nil, database.NewError(
			database.CodeIntegrity,
			"git workspace inventory snapshot digest does not match",
		)
	}
	var state storeState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"git workspace inventory snapshot is invalid",
		)
	}
	normalizeBrokerInventoryState(&state)
	state.generation = generation
	if err := validateInventoryRelationalState(&state); err != nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"git workspace inventory snapshot is invalid",
		)
	}
	if err := validateDevelopmentLineInventory(&state); err != nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"git workspace inventory snapshot is invalid",
		)
	}
	return &state, nil
}

func normalizeBrokerInventoryState(state *storeState) {
	if state.Repositories == nil {
		state.Repositories = make(map[string]*RepositoryRecord)
	}
	if state.Workspaces == nil {
		state.Workspaces = make(map[string]*WorkspaceRecord)
	}
	if state.DevelopmentLines == nil {
		state.DevelopmentLines = make(map[string]*developmentLineRecord)
	}
	if state.PinnedReservationRotations == nil {
		state.PinnedReservationRotations = make(map[string][]pinnedReservationRotationRecord)
	}
}

func (m *Manager) saveBrokerInventory(ctx context.Context, state *storeState) error {
	leaseID, err := m.brokerInventoryLeaseID()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) < 1 || len(encoded) > inventoryBrokerMaximumStateBytes {
		return database.NewError(
			database.CodeInvalid,
			"git workspace inventory snapshot exceeds its limit",
		)
	}
	digestValue := sha256.Sum256(encoded)
	digest := hex.EncodeToString(digestValue[:])
	begin := inventoryBrokerSaveBeginRequest{
		StoreID: m.storeID, LeaseID: leaseID, ExpectedGeneration: state.generation,
		TotalBytes: len(encoded), Digest: digest,
	}
	var response inventoryBrokerSaveResponse
	err = m.broker.CallWithOptions(
		ctx, BrokerDomain, BrokerVersion, inventoryOperationSaveBegin,
		begin, &response, database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if response.NextOffset != 0 || response.Generation != state.generation {
		return database.NewError(
			database.CodeIntegrity,
			"git workspace inventory upload response is invalid",
		)
	}
	for offset := 0; offset < len(encoded); {
		end := offset + inventoryBrokerChunkBytes
		if end > len(encoded) {
			end = len(encoded)
		}
		response = inventoryBrokerSaveResponse{}
		err = m.broker.CallWithOptions(
			ctx, BrokerDomain, BrokerVersion, inventoryOperationSaveChunk,
			inventoryBrokerSaveChunkRequest{
				StoreID: m.storeID, LeaseID: leaseID, Offset: offset, Data: encoded[offset:end],
			},
			&response, database.CallOptions{Mutation: true},
		)
		if err != nil {
			return err
		}
		if response.NextOffset != end || response.Generation != state.generation {
			return database.NewError(
				database.CodeIntegrity,
				"git workspace inventory upload response is invalid",
			)
		}
		offset = end
	}
	response = inventoryBrokerSaveResponse{}
	err = m.broker.CallWithOptions(
		ctx, BrokerDomain, BrokerVersion, inventoryOperationSaveCommit,
		inventoryBrokerSaveCommitRequest{
			StoreID: m.storeID, LeaseID: leaseID, ExpectedGeneration: state.generation,
			TotalBytes: len(encoded), Digest: digest,
		},
		&response, database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Updated || response.Generation != state.generation+1 ||
		response.NextOffset != len(encoded) {
		return database.NewError(
			database.CodeIntegrity,
			"git workspace inventory commit response is invalid",
		)
	}
	state.generation++
	return nil
}

var _ database.Handler = (*BrokerHandler)(nil)
