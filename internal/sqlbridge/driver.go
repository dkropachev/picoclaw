//nolint:govet // Driver validation stages intentionally use narrow error scopes.
package sqlbridge

import (
	"context"
	"database/sql/driver"
	"io"
	"math"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	maximumArgumentNameBytes  = 128
	maximumColumnNameBytes    = 1024
	maximumTransactionIDBytes = 128
	driverCloseTimeout        = 5 * time.Second
)

// Driver is the private database/sql compatibility driver. Callers register it
// under a private name; this package deliberately performs no global
// sql.Register side effect.
type Driver struct {
	rpc RPC
}

func NewDriver(rpc RPC) *Driver { return &Driver{rpc: rpc} }

func (bridge *Driver) Open(name string) (driver.Conn, error) {
	connector, err := bridge.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

func (bridge *Driver) OpenConnector(name string) (driver.Connector, error) {
	if bridge == nil || bridge.rpc == nil {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge RPC is unavailable")
	}
	target, err := ParseDSN(name)
	if err != nil {
		return nil, err
	}
	return &connector{driver: bridge, target: target}, nil
}

type connector struct {
	driver *Driver
	target Target
}

func (item *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if item == nil || item.driver == nil || item.driver.rpc == nil {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge RPC is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request := PingRequest{Target: item.target}
	if err := ValidatePingRequest(request); err != nil {
		return nil, err
	}
	response, err := item.driver.rpc.Ping(ctx, request)
	if err != nil {
		return nil, err
	}
	if !response.Ready {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge store is unavailable")
	}
	return &connection{rpc: item.driver.rpc, target: item.target}, nil
}

func (item *connector) Driver() driver.Driver { return item.driver }

type connection struct {
	rpc    RPC
	target Target

	mu            sync.Mutex
	closed        bool
	unusable      bool
	transactionID string
	heartbeat     *transactionHeartbeat
}

func (item *connection) Prepare(query string) (driver.Stmt, error) {
	return item.PrepareContext(context.Background(), query)
}

func (item *connection) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	if err := ValidateStatement(query, item.target.Mode); err != nil {
		return nil, err
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return nil, err
	}
	return &statement{connection: item, query: query}, nil
}

func (item *connection) Close() error {
	if item == nil {
		return nil
	}
	item.mu.Lock()
	if item.closed {
		item.mu.Unlock()
		return nil
	}
	item.closed = true
	transactionID := item.transactionID
	heartbeat := item.heartbeat
	item.transactionID = ""
	item.heartbeat = nil
	item.mu.Unlock()
	heartbeat.stopAndWait()
	if transactionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), driverCloseTimeout)
	defer cancel()
	response, err := item.rpc.Rollback(ctx, TransactionRequest{
		Target: item.target, TransactionID: transactionID,
	})
	if err != nil {
		return err
	}
	if !response.Accepted {
		return database.NewError(database.CodeIntegrity, "SQL bridge rollback response is invalid")
	}
	return nil
}

func (item *connection) Begin() (driver.Tx, error) {
	return item.BeginTx(context.Background(), driver.TxOptions{})
}

func (item *connection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return nil, err
	}
	if item.transactionID != "" {
		return nil, database.NewError(database.CodeConflict, "SQL bridge transaction is already active")
	}
	request := BeginRequest{
		Target: item.target, Isolation: int(options.Isolation), ReadOnly: options.ReadOnly,
	}
	if err := ValidateBeginRequest(request); err != nil {
		return nil, err
	}
	response, err := item.rpc.Begin(ctx, request)
	if err != nil {
		return nil, err
	}
	if !validTransactionID(response.TransactionID) || response.TTLNanoSeconds <= 0 {
		item.unusable = true
		return nil, database.NewError(database.CodeIntegrity, "SQL bridge transaction response is invalid")
	}
	item.transactionID = response.TransactionID
	item.heartbeat = newTransactionHeartbeat(
		item.rpc, item.target, response.TransactionID, time.Duration(response.TTLNanoSeconds),
	)
	return &transaction{connection: item, id: response.TransactionID}, nil
}

func (item *connection) ExecContext(
	ctx context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Result, error) {
	if err := ValidateStatement(query, item.target.Mode); err != nil {
		return nil, err
	}
	encoded, err := encodeArguments(arguments)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return nil, err
	}
	request := ExecRequest{
		Target: item.target, Statement: query, Arguments: encoded,
		TransactionID: item.transactionID,
	}
	if err := ValidateExecRequest(request); err != nil {
		return nil, err
	}
	response, err := item.rpc.Exec(ctx, request)
	if err != nil {
		return nil, err
	}
	if response.RowsAffected < 0 {
		return nil, database.NewError(database.CodeIntegrity, "SQL bridge exec response is invalid")
	}
	return bridgeResult{
		lastInsertID: response.LastInsertID,
		rowsAffected: response.RowsAffected,
	}, nil
}

func (item *connection) QueryContext(
	ctx context.Context,
	query string,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	if err := ValidateStatement(query, item.target.Mode); err != nil {
		return nil, err
	}
	encoded, err := encodeArguments(arguments)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return nil, err
	}
	request := QueryRequest{
		Target: item.target, Statement: query, Arguments: encoded,
		TransactionID: item.transactionID,
	}
	if err := ValidateQueryRequest(request); err != nil {
		return nil, err
	}
	response, err := item.rpc.Query(ctx, request)
	if err != nil {
		return nil, err
	}
	return decodeRows(response)
}

func (item *connection) CheckNamedValue(value *driver.NamedValue) error {
	if value == nil {
		return database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
	}
	converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
	if err != nil {
		return database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
	}
	value.Value = converted
	return nil
}

func (item *connection) Ping(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return err
	}
	request := PingRequest{Target: item.target}
	if err := ValidatePingRequest(request); err != nil {
		return err
	}
	response, err := item.rpc.Ping(ctx, request)
	if err != nil {
		return err
	}
	if !response.Ready {
		return database.NewError(database.CodeUnavailable, "SQL bridge store is unavailable")
	}
	return nil
}

func (item *connection) ResetSession(context.Context) error {
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return driver.ErrBadConn
	}
	if item.transactionID != "" {
		return driver.ErrBadConn
	}
	return nil
}

func (item *connection) IsValid() bool {
	item.mu.Lock()
	defer item.mu.Unlock()
	return !item.closed && !item.unusable && item.transactionID == ""
}

func (item *connection) usableLocked() error {
	if item == nil || item.closed || item.unusable || item.rpc == nil {
		return database.NewError(database.CodeUnavailable, "SQL bridge connection is unavailable")
	}
	if err := item.heartbeat.failure(); err != nil {
		return err
	}
	return nil
}

type statement struct {
	connection *connection
	query      string
	mu         sync.Mutex
	closed     bool
}

func (item *statement) Close() error {
	item.mu.Lock()
	item.closed = true
	item.mu.Unlock()
	return nil
}

func (item *statement) NumInput() int { return -1 }

func (item *statement) Exec(arguments []driver.Value) (driver.Result, error) {
	return item.ExecContext(context.Background(), namedValues(arguments))
}

func (item *statement) Query(arguments []driver.Value) (driver.Rows, error) {
	return item.QueryContext(context.Background(), namedValues(arguments))
}

func (item *statement) ExecContext(
	ctx context.Context,
	arguments []driver.NamedValue,
) (driver.Result, error) {
	item.mu.Lock()
	closed := item.closed
	item.mu.Unlock()
	if closed || item.connection == nil {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge statement is closed")
	}
	return item.connection.ExecContext(ctx, item.query, arguments)
}

func (item *statement) QueryContext(
	ctx context.Context,
	arguments []driver.NamedValue,
) (driver.Rows, error) {
	item.mu.Lock()
	closed := item.closed
	item.mu.Unlock()
	if closed || item.connection == nil {
		return nil, database.NewError(database.CodeUnavailable, "SQL bridge statement is closed")
	}
	return item.connection.QueryContext(ctx, item.query, arguments)
}

type transaction struct {
	connection *connection
	id         string
	once       sync.Once
	err        error
}

func (item *transaction) Commit() error   { return item.finish(true) }
func (item *transaction) Rollback() error { return item.finish(false) }

func (item *transaction) finish(commit bool) error {
	if item == nil || item.connection == nil {
		return database.NewError(database.CodeUnavailable, "SQL bridge transaction is unavailable")
	}
	called := false
	item.once.Do(func() {
		called = true
		item.err = item.connection.finishTransaction(item.id, commit)
	})
	if !called {
		return database.NewError(database.CodeConflict, "SQL bridge transaction is already complete")
	}
	return item.err
}

func (item *connection) finishTransaction(id string, commit bool) error {
	item.mu.Lock()
	defer item.mu.Unlock()
	if err := item.usableLocked(); err != nil {
		return err
	}
	if item.transactionID == "" || item.transactionID != id {
		return database.NewError(database.CodeConflict, "SQL bridge transaction identity changed")
	}
	request := TransactionRequest{Target: item.target, TransactionID: id}
	if err := ValidateTransactionRequest(request); err != nil {
		item.unusable = true
		item.transactionID = ""
		heartbeat := item.heartbeat
		item.heartbeat = nil
		heartbeat.stopAndWait()
		return err
	}
	var response TransactionResponse
	var err error
	heartbeat := item.heartbeat
	item.heartbeat = nil
	heartbeat.stopAndWait()
	if commit {
		response, err = item.rpc.Commit(context.Background(), request)
	} else {
		response, err = item.rpc.Rollback(context.Background(), request)
	}
	item.transactionID = ""
	if err != nil {
		item.unusable = true
		return err
	}
	if !response.Accepted {
		item.unusable = true
		return database.NewError(database.CodeIntegrity, "SQL bridge transaction response is invalid")
	}
	return nil
}

type bridgeResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (result bridgeResult) LastInsertId() (int64, error) { return result.lastInsertID, nil }
func (result bridgeResult) RowsAffected() (int64, error) { return result.rowsAffected, nil }

type bridgeRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
	closed  bool
}

func (rows *bridgeRows) Columns() []string { return append([]string(nil), rows.columns...) }

func (rows *bridgeRows) Close() error {
	rows.closed = true
	rows.rows = nil
	return nil
}

func (rows *bridgeRows) Next(destination []driver.Value) error {
	if rows.closed || rows.index >= len(rows.rows) {
		return io.EOF
	}
	if len(destination) != len(rows.columns) {
		return database.NewError(database.CodeIntegrity, "SQL bridge row width is invalid")
	}
	for index, value := range rows.rows[rows.index] {
		if bytes, ok := value.([]byte); ok {
			destination[index] = append([]byte(nil), bytes...)
		} else {
			destination[index] = value
		}
	}
	rows.index++
	return nil
}

func encodeArguments(values []driver.NamedValue) ([]Argument, error) {
	if len(values) > MaxArguments {
		return nil, database.NewError(database.CodeInvalid, "SQL bridge has too many arguments")
	}
	result := make([]Argument, len(values))
	totalBytes := 0
	for index, value := range values {
		if value.Ordinal <= 0 || len(value.Name) > maximumArgumentNameBytes ||
			!utf8.ValidString(value.Name) || containsNUL(value.Name) {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
		}
		converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
		if err != nil {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
		}
		encoded, size, err := encodeValue(converted)
		if err != nil {
			return nil, err
		}
		totalBytes += size + len(value.Name)
		if totalBytes > MaxResultValueBytes {
			return nil, database.NewError(database.CodeInvalid, "SQL bridge arguments are too large")
		}
		result[index] = Argument{
			Name: value.Name, Ordinal: value.Ordinal, Value: encoded,
		}
	}
	return result, nil
}

func encodeValue(value driver.Value) (Value, int, error) {
	switch value := value.(type) {
	case nil:
		return Value{Kind: ValueNull}, 0, nil
	case int64:
		return Value{Kind: ValueInteger, Integer: value}, 8, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return Value{}, 0, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
		}
		return Value{Kind: ValueFloat, Float: value}, 8, nil
	case bool:
		return Value{Kind: ValueBoolean, Boolean: value}, 1, nil
	case []byte:
		if len(value) > MaxValueBytes {
			return Value{}, 0, database.NewError(database.CodeInvalid, "SQL bridge argument is too large")
		}
		return Value{Kind: ValueBytes, Bytes: append([]byte(nil), value...)}, len(value), nil
	case string:
		if len(value) > MaxValueBytes || !utf8.ValidString(value) || containsNUL(value) {
			return Value{}, 0, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
		}
		return Value{Kind: ValueString, String: value}, len(value), nil
	case time.Time:
		if value.Year() < 0 || value.Year() > 9999 {
			return Value{}, 0, database.NewError(database.CodeInvalid, "SQL bridge time argument is invalid")
		}
		encoded := value.Format(time.RFC3339Nano)
		return Value{Kind: ValueTime, Time: encoded}, len(encoded), nil
	default:
		return Value{}, 0, database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
	}
}

func decodeRows(response QueryResponse) (driver.Rows, error) {
	if len(response.Columns) > MaxResultColumns || len(response.Rows) > MaxResultRows {
		return nil, database.NewError(database.CodeIntegrity, "SQL bridge query response exceeds bounds")
	}
	columns := append([]string(nil), response.Columns...)
	for _, column := range columns {
		if len(column) > maximumColumnNameBytes || !utf8.ValidString(column) || containsNUL(column) {
			return nil, database.NewError(database.CodeIntegrity, "SQL bridge column is invalid")
		}
	}
	decoded := make([][]driver.Value, len(response.Rows))
	totalBytes := 0
	for rowIndex, row := range response.Rows {
		if len(row) != len(columns) {
			return nil, database.NewError(database.CodeIntegrity, "SQL bridge row width is invalid")
		}
		decoded[rowIndex] = make([]driver.Value, len(row))
		for columnIndex, value := range row {
			item, size, err := decodeValue(value)
			if err != nil {
				return nil, err
			}
			totalBytes += size
			if totalBytes > MaxResultValueBytes {
				return nil, database.NewError(database.CodeIntegrity, "SQL bridge query response exceeds bounds")
			}
			decoded[rowIndex][columnIndex] = item
		}
	}
	return &bridgeRows{columns: columns, rows: decoded}, nil
}

func decodeValue(value Value) (driver.Value, int, error) {
	invalid := func() (driver.Value, int, error) {
		return nil, 0, database.NewError(database.CodeIntegrity, "SQL bridge value is invalid")
	}
	otherScalar := func() bool {
		return value.Integer != 0 || value.Float != 0 || value.Boolean || value.Bytes != nil ||
			value.String != "" || value.Time != ""
	}
	switch value.Kind {
	case ValueNull:
		if otherScalar() {
			return invalid()
		}
		return nil, 0, nil
	case ValueInteger:
		if value.Float != 0 || value.Boolean || value.Bytes != nil || value.String != "" || value.Time != "" {
			return invalid()
		}
		return value.Integer, 8, nil
	case ValueFloat:
		if value.Integer != 0 || value.Boolean || value.Bytes != nil || value.String != "" ||
			value.Time != "" || math.IsNaN(value.Float) || math.IsInf(value.Float, 0) {
			return invalid()
		}
		return value.Float, 8, nil
	case ValueBoolean:
		if value.Integer != 0 || value.Float != 0 || value.Bytes != nil || value.String != "" || value.Time != "" {
			return invalid()
		}
		return value.Boolean, 1, nil
	case ValueBytes:
		if value.Integer != 0 || value.Float != 0 || value.Boolean || value.String != "" ||
			value.Time != "" || len(value.Bytes) > MaxValueBytes {
			return invalid()
		}
		return append([]byte(nil), value.Bytes...), len(value.Bytes), nil
	case ValueString:
		if value.Integer != 0 || value.Float != 0 || value.Boolean || value.Bytes != nil ||
			value.Time != "" || len(value.String) > MaxValueBytes || !utf8.ValidString(value.String) ||
			containsNUL(value.String) {
			return invalid()
		}
		return value.String, len(value.String), nil
	case ValueTime:
		if value.Integer != 0 || value.Float != 0 || value.Boolean || value.Bytes != nil || value.String != "" {
			return invalid()
		}
		parsed, err := time.Parse(time.RFC3339Nano, value.Time)
		if err != nil {
			return invalid()
		}
		return parsed, len(value.Time), nil
	default:
		return invalid()
	}
}

func namedValues(values []driver.Value) []driver.NamedValue {
	result := make([]driver.NamedValue, len(values))
	for index, value := range values {
		result[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return result
}

func validTransactionID(value string) bool {
	if value == "" || len(value) > maximumTransactionIDBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func containsNUL(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] == 0 {
			return true
		}
	}
	return false
}

var (
	_ driver.Driver             = (*Driver)(nil)
	_ driver.DriverContext      = (*Driver)(nil)
	_ driver.Connector          = (*connector)(nil)
	_ driver.Conn               = (*connection)(nil)
	_ driver.ConnPrepareContext = (*connection)(nil)
	_ driver.ConnBeginTx        = (*connection)(nil)
	_ driver.ExecerContext      = (*connection)(nil)
	_ driver.QueryerContext     = (*connection)(nil)
	_ driver.NamedValueChecker  = (*connection)(nil)
	_ driver.Pinger             = (*connection)(nil)
	_ driver.SessionResetter    = (*connection)(nil)
	_ driver.Validator          = (*connection)(nil)
	_ driver.Stmt               = (*statement)(nil)
	_ driver.StmtExecContext    = (*statement)(nil)
	_ driver.StmtQueryContext   = (*statement)(nil)
	_ driver.Tx                 = (*transaction)(nil)
	_ driver.Result             = bridgeResult{}
	_ driver.Rows               = (*bridgeRows)(nil)
)
