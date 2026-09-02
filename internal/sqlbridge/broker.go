package sqlbridge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const bridgeBusyTimeout = 5 * time.Second

const defaultBridgeTransactionTTL = 30 * time.Second

type brokerTransaction struct {
	target     Target
	value      *sql.Tx
	mu         sync.Mutex
	timer      *time.Timer
	generation uint64
}

// BrokerHandler is the broker-side half of the temporary Matrix/WhatsApp SQL
// bridge. It resolves only trusted catalog IDs and retains one provider pool
// per store until broker shutdown.
type BrokerHandler struct {
	catalog *storecatalog.Catalog

	mu             sync.Mutex
	pools          map[database.StoreID]*sql.DB
	transactions   map[string]*brokerTransaction
	transactionTTL time.Duration
	closed         bool
	closeOnce      sync.Once
	closeErr       error
}

// NewBrokerHandler builds the allow-listed physical catalog without opening a
// database generation.
func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"SQL bridge handler requires database broker authority",
		)
	}
	catalog, err := storecatalog.Build(home, cfg)
	if err != nil {
		return nil, err
	}
	return &BrokerHandler{
		catalog: catalog, pools: make(map[database.StoreID]*sql.DB),
		transactions:   make(map[string]*brokerTransaction),
		transactionTTL: defaultBridgeTransactionTTL,
	}, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != RPCDomain || request.Version != RPCVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	switch request.Operation {
	case RPCOperationResolve:
		var input ResolveChannelRequest
		if request.DecodePayload(&input) != nil || strings.TrimSpace(input.ChannelType) == "" ||
			strings.TrimSpace(input.ChannelName) == "" || input.ChannelType != strings.TrimSpace(input.ChannelType) ||
			input.ChannelName != strings.TrimSpace(input.ChannelName) {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge channel identity is invalid")
		}
		logicalID, ok := storecatalog.ChannelStoreID(input.ChannelType, input.ChannelName)
		if !ok {
			return nil, database.NewError(database.CodeUnsupported, "channel has no SQL bridge store")
		}
		spec, ok := handler.catalog.Lookup(logicalID)
		if !ok || spec.Domain != "channel-matrix" && spec.Domain != "channel-whatsapp" {
			return nil, database.NewError(database.CodeUnauthorized, "channel store is not broker-cataloged")
		}
		storeID, parseErr := database.ParseStoreID(logicalID)
		if parseErr != nil {
			return nil, database.NewError(database.CodeIntegrity, "channel store identity is invalid")
		}
		return ResolveChannelResponse{StoreID: storeID}, nil
	case RPCOperationPing:
		var input PingRequest
		if err := request.DecodePayload(&input); err != nil || ValidatePingRequest(input) != nil {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge request is invalid")
		}
		db, err := handler.pool(input.Target)
		if err != nil {
			return nil, err
		}
		if err := db.PingContext(ctx); err != nil {
			return nil, database.NewError(database.CodeUnavailable, "SQL bridge store is unavailable")
		}
		return PingResponse{Ready: true}, nil
	case RPCOperationExec:
		var input ExecRequest
		if err := request.DecodePayload(&input); err != nil || ValidateExecRequest(input) != nil {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge request is invalid")
		}
		return handler.exec(ctx, input)
	case RPCOperationQuery:
		var input QueryRequest
		if err := request.DecodePayload(&input); err != nil || ValidateQueryRequest(input) != nil {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge request is invalid")
		}
		return handler.query(ctx, input)
	case RPCOperationBegin:
		var input BeginRequest
		if err := request.DecodePayload(&input); err != nil || ValidateBeginRequest(input) != nil {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge request is invalid")
		}
		return handler.begin(ctx, input)
	case RPCOperationRenew, RPCOperationCommit, RPCOperationRollback:
		var input TransactionRequest
		if err := request.DecodePayload(&input); err != nil || ValidateTransactionRequest(input) != nil {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge request is invalid")
		}
		if request.Operation == RPCOperationRenew {
			return handler.renew(input)
		}
		return handler.finish(input, request.Operation == RPCOperationCommit)
	default:
		return nil, database.NewError(database.CodeUnsupported, "SQL bridge operation is unsupported")
	}
}

func (handler *BrokerHandler) pool(target Target) (*sql.DB, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"SQL bridge provider access requires database broker authority",
		)
	}
	if err := validateTarget(target); err != nil || target.Mode != ModeRuntime {
		return nil, database.NewError(database.CodeUnauthorized, "SQL bridge target is not available online")
	}
	spec, ok := handler.catalog.Lookup(string(target.StoreID))
	if !ok || (spec.Domain != "channel-matrix" && spec.Domain != "channel-whatsapp") {
		return nil, database.NewError(database.CodeUnauthorized, "SQL bridge store is not cataloged")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge broker is closed")
	}
	if existing := handler.pools[target.StoreID]; existing != nil {
		return existing, nil
	}
	db, err := sqliteprovider.OpenStore(spec.Path, bridgeBusyTimeout)
	if err != nil {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge store is unavailable")
	}
	if err := sqliteprovider.Configure(context.Background(), db, bridgeBusyTimeout, false); err != nil {
		_ = db.Close()
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge store is unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	handler.pools[target.StoreID] = db
	return db, nil
}

func (handler *BrokerHandler) exec(ctx context.Context, request ExecRequest) (ExecResponse, error) {
	executor, release, err := handler.executor(request.Target, request.TransactionID)
	if err != nil {
		return ExecResponse{}, err
	}
	defer release()
	arguments, err := decodeArguments(request.Arguments)
	if err != nil {
		return ExecResponse{}, err
	}
	result, err := executor.ExecContext(ctx, request.Statement, arguments...)
	if err != nil {
		return ExecResponse{}, bridgeBackendError(err)
	}
	lastInsertID, err := result.LastInsertId()
	if err != nil {
		lastInsertID = 0
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected < 0 {
		return ExecResponse{}, database.NewError(database.CodeInternal, "SQL bridge result is unavailable")
	}
	return ExecResponse{LastInsertID: lastInsertID, RowsAffected: rowsAffected}, nil
}

type bridgeExecutor interface {
	ExecContext(ctx context.Context, statement string, arguments ...any) (sql.Result, error)
	QueryContext(ctx context.Context, statement string, arguments ...any) (*sql.Rows, error)
}

func (handler *BrokerHandler) executor(
	target Target,
	transactionID string,
) (bridgeExecutor, func(), error) {
	if transactionID == "" {
		pool, err := handler.pool(target)
		return pool, func() {}, err
	}
	handler.mu.Lock()
	transaction, ok := handler.transactions[transactionID]
	if !ok || transaction.target != target || transaction.value == nil {
		handler.mu.Unlock()
		return nil, nil, database.NewError(
			database.CodeConflict,
			"SQL bridge transaction is unavailable",
		)
	}
	if !transaction.mu.TryLock() {
		handler.mu.Unlock()
		return nil, nil, database.NewError(database.CodeConflict, "SQL bridge transaction is busy")
	}
	handler.mu.Unlock()
	return transaction.value, transaction.mu.Unlock, nil
}

func (handler *BrokerHandler) query(ctx context.Context, request QueryRequest) (QueryResponse, error) {
	executor, release, err := handler.executor(request.Target, request.TransactionID)
	if err != nil {
		return QueryResponse{}, err
	}
	defer release()
	arguments, err := decodeArguments(request.Arguments)
	if err != nil {
		return QueryResponse{}, err
	}
	rows, err := executor.QueryContext(ctx, request.Statement, arguments...)
	if err != nil {
		return QueryResponse{}, bridgeBackendError(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || len(columns) > MaxResultColumns {
		return QueryResponse{}, database.NewError(database.CodeInternal, "SQL bridge columns are invalid")
	}
	for _, column := range columns {
		if len(column) > maximumColumnNameBytes || !utf8.ValidString(column) {
			return QueryResponse{}, database.NewError(database.CodeIntegrity, "SQL bridge column is invalid")
		}
	}
	response := QueryResponse{Columns: append([]string(nil), columns...)}
	encodedColumns, encodeErr := database.MarshalCanonical(response.Columns)
	if encodeErr != nil {
		return QueryResponse{}, database.NewError(database.CodeInternal, "SQL bridge columns are invalid")
	}
	// Account for the QueryResponse object keys, arrays, commas, and closing
	// delimiters in addition to the exact canonical encoding of every value.
	encodedBytes := len(encodedColumns) + len(`{"columns":,"rows":[]}`)
	totalBytes := 0
	for rows.Next() {
		if len(response.Rows) >= MaxResultRows {
			return QueryResponse{}, database.NewError(database.CodeIntegrity, "SQL bridge result exceeds row limit")
		}
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return QueryResponse{}, bridgeBackendError(err)
		}
		encoded := make([]Value, len(values))
		rowEncodedBytes := 2
		for index, value := range values {
			wire, size, encodeErr := encodeBrokerValue(value)
			if encodeErr != nil {
				return QueryResponse{}, encodeErr
			}
			totalBytes += size
			if totalBytes > MaxResultValueBytes {
				return QueryResponse{}, database.NewError(
					database.CodeIntegrity,
					"SQL bridge result exceeds value limit",
				)
			}
			wireJSON, marshalErr := database.MarshalCanonical(wire)
			if marshalErr != nil {
				return QueryResponse{}, database.NewError(
					database.CodeIntegrity,
					"SQL bridge result value is invalid",
				)
			}
			rowEncodedBytes += len(wireJSON)
			if index > 0 {
				rowEncodedBytes++
			}
			encoded[index] = wire
		}
		if len(response.Rows) > 0 {
			rowEncodedBytes++
		}
		if rowEncodedBytes > MaxResultEncodedBytes-encodedBytes {
			return QueryResponse{}, database.NewError(
				database.CodeIntegrity,
				"SQL bridge result exceeds transport limit",
			)
		}
		encodedBytes += rowEncodedBytes
		response.Rows = append(response.Rows, encoded)
	}
	if err := rows.Err(); err != nil {
		return QueryResponse{}, bridgeBackendError(err)
	}
	return response, nil
}

func (handler *BrokerHandler) begin(_ context.Context, request BeginRequest) (BeginResponse, error) {
	if request.Target.Mode != ModeRuntime {
		return BeginResponse{}, database.NewError(
			database.CodeUnauthorized,
			"offline SQL bridge mode requires migration fencing",
		)
	}
	db, err := handler.pool(request.Target)
	if err != nil {
		return BeginResponse{}, err
	}
	// The RPC request context ends after Begin returns. Transaction lifetime is
	// instead fenced by the broker connection/handler and explicit commit,
	// rollback, or broker shutdown.
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{
		Isolation: sql.IsolationLevel(request.Isolation), ReadOnly: request.ReadOnly,
	})
	if err != nil {
		return BeginResponse{}, bridgeBackendError(err)
	}
	id, err := newBridgeTransactionID()
	if err != nil {
		_ = tx.Rollback()
		return BeginResponse{}, database.NewError(database.CodeInternal, "SQL bridge transaction identity failed")
	}
	transaction := &brokerTransaction{target: request.Target, value: tx}
	handler.mu.Lock()
	if handler.closed {
		handler.mu.Unlock()
		_ = tx.Rollback()
		return BeginResponse{}, database.NewError(database.CodeUnavailable, "SQL bridge broker is closed")
	}
	handler.transactions[id] = transaction
	handler.armTransactionLocked(id, transaction, handler.effectiveTransactionTTL())
	ttl := handler.effectiveTransactionTTL()
	handler.mu.Unlock()
	return BeginResponse{TransactionID: id, TTLNanoSeconds: int64(ttl)}, nil
}

func (handler *BrokerHandler) finish(request TransactionRequest, commit bool) (TransactionResponse, error) {
	handler.mu.Lock()
	transaction, ok := handler.transactions[request.TransactionID]
	if ok && transaction != nil && transaction.target == request.Target && transaction.mu.TryLock() {
		delete(handler.transactions, request.TransactionID)
		transaction.generation++
		if transaction.timer != nil {
			transaction.timer.Stop()
		}
	} else {
		ok = false
	}
	handler.mu.Unlock()
	if !ok || transaction.value == nil {
		return TransactionResponse{}, database.NewError(database.CodeConflict, "SQL bridge transaction is unavailable")
	}
	defer transaction.mu.Unlock()
	var err error
	if commit {
		err = transaction.value.Commit()
	} else {
		err = transaction.value.Rollback()
	}
	if err != nil {
		return TransactionResponse{}, bridgeBackendError(err)
	}
	return TransactionResponse{Accepted: true}, nil
}

func (handler *BrokerHandler) renew(request TransactionRequest) (TransactionLeaseResponse, error) {
	handler.mu.Lock()
	transaction := handler.transactions[request.TransactionID]
	if handler.closed || transaction == nil || transaction.target != request.Target {
		handler.mu.Unlock()
		return TransactionLeaseResponse{}, database.NewError(
			database.CodeConflict, "SQL bridge transaction is unavailable",
		)
	}
	ttl := handler.effectiveTransactionTTL()
	handler.armTransactionLocked(request.TransactionID, transaction, ttl)
	handler.mu.Unlock()
	return TransactionLeaseResponse{Accepted: true, TTLNanoSeconds: int64(ttl)}, nil
}

func (handler *BrokerHandler) effectiveTransactionTTL() time.Duration {
	if handler == nil || handler.transactionTTL <= 0 {
		return defaultBridgeTransactionTTL
	}
	return handler.transactionTTL
}

func (handler *BrokerHandler) armTransactionLocked(
	id string,
	transaction *brokerTransaction,
	delay time.Duration,
) {
	transaction.generation++
	generation := transaction.generation
	if transaction.timer != nil {
		transaction.timer.Stop()
	}
	transaction.timer = time.AfterFunc(delay, func() {
		handler.expireTransaction(id, transaction, generation)
	})
}

func (handler *BrokerHandler) expireTransaction(
	id string,
	transaction *brokerTransaction,
	generation uint64,
) {
	handler.mu.Lock()
	if handler.transactions[id] != transaction || transaction.generation != generation {
		handler.mu.Unlock()
		return
	}
	if !transaction.mu.TryLock() {
		handler.armTransactionLocked(id, transaction, 25*time.Millisecond)
		handler.mu.Unlock()
		return
	}
	delete(handler.transactions, id)
	transaction.generation++
	handler.mu.Unlock()
	if transaction.value != nil {
		_ = transaction.value.Rollback()
	}
	transaction.mu.Unlock()
}

// Close rolls back live transactions and closes every stable bridge pool. It
// must run before the broker releases its online storage fence.
func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		handler.closed = true
		transactions := handler.transactions
		pools := handler.pools
		handler.transactions = make(map[string]*brokerTransaction)
		handler.pools = make(map[database.StoreID]*sql.DB)
		for _, transaction := range transactions {
			if transaction != nil {
				transaction.generation++
				if transaction.timer != nil {
					transaction.timer.Stop()
				}
			}
		}
		handler.mu.Unlock()
		for _, transaction := range transactions {
			if transaction != nil && transaction.value != nil {
				transaction.mu.Lock()
				handler.closeErr = errors.Join(handler.closeErr, transaction.value.Rollback())
				transaction.mu.Unlock()
			}
		}
		for _, pool := range pools {
			if pool != nil {
				handler.closeErr = errors.Join(handler.closeErr, pool.Close())
			}
		}
	})
	return handler.closeErr
}

func decodeArguments(arguments []Argument) ([]any, error) {
	if err := validateRPCArguments(arguments); err != nil {
		return nil, err
	}
	result := make([]any, len(arguments))
	for index, argument := range arguments {
		value, err := decodeArgumentValue(argument.Value)
		if err != nil {
			return nil, err
		}
		if argument.Name != "" {
			result[index] = sql.Named(argument.Name, value)
		} else {
			result[index] = value
		}
	}
	return result, nil
}

func decodeArgumentValue(value Value) (any, error) {
	if _, valid := validWireValue(value); !valid {
		return nil, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
	}
	switch value.Kind {
	case ValueNull:
		return nil, nil
	case ValueInteger:
		return value.Integer, nil
	case ValueFloat:
		return value.Float, nil
	case ValueBoolean:
		return value.Boolean, nil
	case ValueBytes:
		return append([]byte(nil), value.Bytes...), nil
	case ValueString:
		return value.String, nil
	case ValueTime:
		return time.Parse(time.RFC3339Nano, value.Time)
	default:
		return nil, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
	}
}

func encodeBrokerValue(value any) (Value, int, error) {
	switch value := value.(type) {
	case nil:
		return Value{Kind: ValueNull}, 0, nil
	case int64:
		return Value{Kind: ValueInteger, Integer: value}, 8, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return Value{}, 0, database.NewError(database.CodeIntegrity, "SQL bridge value is invalid")
		}
		return Value{Kind: ValueFloat, Float: value}, 8, nil
	case bool:
		return Value{Kind: ValueBoolean, Boolean: value}, 1, nil
	case []byte:
		if len(value) > MaxResultValueBytes {
			return Value{}, 0, database.NewError(database.CodeIntegrity, "SQL bridge value is too large")
		}
		return Value{Kind: ValueBytes, Bytes: append([]byte(nil), value...)}, len(value), nil
	case string:
		if len(value) > MaxResultValueBytes || !utf8.ValidString(value) {
			return Value{}, 0, database.NewError(database.CodeIntegrity, "SQL bridge value is invalid")
		}
		return Value{Kind: ValueString, String: value}, len(value), nil
	case time.Time:
		encoded := value.Format(time.RFC3339Nano)
		return Value{Kind: ValueTime, Time: encoded}, len(encoded), nil
	default:
		return Value{}, 0, database.NewError(database.CodeIntegrity, "SQL bridge value type is unsupported")
	}
}

func newBridgeTransactionID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "tx-" + hex.EncodeToString(random), nil
}

func bridgeBackendError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return database.NewError(database.CodeDeadline, "SQL bridge operation deadline was exceeded")
	}
	return database.NewError(database.CodeInternal, "SQL bridge provider operation failed")
}

var _ database.Handler = (*BrokerHandler)(nil)
