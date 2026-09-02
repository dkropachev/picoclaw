package sqlbridge

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	RPCDomain  = "sqlbridge"
	RPCVersion = 1

	RPCOperationPing     = "ping"
	RPCOperationResolve  = "resolve-channel"
	RPCOperationExec     = "exec"
	RPCOperationQuery    = "query"
	RPCOperationBegin    = "begin"
	RPCOperationRenew    = "renew"
	RPCOperationCommit   = "commit"
	RPCOperationRollback = "rollback"
)

const (
	MaxArguments        = 32766
	MaxResultColumns    = 1024
	MaxResultRows       = 100000
	MaxValueBytes       = 16 << 20
	MaxResultValueBytes = 64 << 20
	// MaxResultEncodedBytes leaves headroom for the authenticated response
	// envelope beneath the protocol's 128 MiB hard frame ceiling.
	MaxResultEncodedBytes = 96 << 20
)

// ValueKind is the closed wire representation of database/sql driver values.
type ValueKind string

const (
	ValueNull    ValueKind = "null"
	ValueInteger ValueKind = "integer"
	ValueFloat   ValueKind = "float"
	ValueBoolean ValueKind = "boolean"
	ValueBytes   ValueKind = "bytes"
	ValueString  ValueKind = "string"
	ValueTime    ValueKind = "time"
)

// Value is a detached typed SQL value. Kind selects exactly one scalar field;
// zero scalar values remain unambiguous because Kind is always present.
type Value struct {
	Kind    ValueKind `json:"kind"`
	Integer int64     `json:"integer,omitempty"`
	Float   float64   `json:"float,omitempty"`
	Boolean bool      `json:"boolean,omitempty"`
	Bytes   []byte    `json:"bytes,omitempty"`
	String  string    `json:"string,omitempty"`
	Time    string    `json:"time,omitempty"`
}

// Argument preserves database/sql ordinal and optional named-argument identity.
type Argument struct {
	Name    string `json:"name,omitempty"`
	Ordinal int    `json:"ordinal"`
	Value   Value  `json:"value"`
}

type PingRequest struct {
	Target Target `json:"target"`
}

type PingResponse struct {
	Ready bool `json:"ready"`
}

type ResolveChannelRequest struct {
	ChannelType string `json:"channel_type"`
	ChannelName string `json:"channel_name"`
}

type ResolveChannelResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type ExecRequest struct {
	Target        Target     `json:"target"`
	Statement     string     `json:"statement"`
	Arguments     []Argument `json:"arguments,omitempty"`
	TransactionID string     `json:"transaction_id,omitempty"`
}

type ExecResponse struct {
	LastInsertID int64 `json:"last_insert_id"`
	RowsAffected int64 `json:"rows_affected"`
}

type QueryRequest struct {
	Target        Target     `json:"target"`
	Statement     string     `json:"statement"`
	Arguments     []Argument `json:"arguments,omitempty"`
	TransactionID string     `json:"transaction_id,omitempty"`
}

type QueryResponse struct {
	Columns []string  `json:"columns"`
	Rows    [][]Value `json:"rows"`
}

type BeginRequest struct {
	Target    Target `json:"target"`
	Isolation int    `json:"isolation"`
	ReadOnly  bool   `json:"read_only"`
}

type BeginResponse struct {
	TransactionID  string `json:"transaction_id"`
	TTLNanoSeconds int64  `json:"ttl_nanoseconds"`
}

type TransactionRequest struct {
	Target        Target `json:"target"`
	TransactionID string `json:"transaction_id"`
}

type TransactionResponse struct {
	Accepted bool `json:"accepted"`
}

type TransactionLeaseResponse struct {
	Accepted       bool  `json:"accepted"`
	TTLNanoSeconds int64 `json:"ttl_nanoseconds"`
}

// RPC is the narrow transport contract consumed by the private SQL driver.
// Implementations dispatch typed bridge operations; they never receive a
// physical path or provider handle.
type RPC interface {
	Ping(ctx context.Context, request PingRequest) (PingResponse, error)
	Exec(ctx context.Context, request ExecRequest) (ExecResponse, error)
	Query(ctx context.Context, request QueryRequest) (QueryResponse, error)
	Begin(ctx context.Context, request BeginRequest) (BeginResponse, error)
	Renew(ctx context.Context, request TransactionRequest) (TransactionLeaseResponse, error)
	Commit(ctx context.Context, request TransactionRequest) (TransactionResponse, error)
	Rollback(ctx context.Context, request TransactionRequest) (TransactionResponse, error)
}

// ResolveChannelStore asks the trusted broker catalog for one bridge-enabled
// channel identity. Runtime callers never rebuild provider paths from config.
func ResolveChannelStore(
	ctx context.Context,
	client *database.Client,
	channelType,
	channelName string,
) (database.StoreID, error) {
	if client == nil || strings.TrimSpace(channelType) == "" || strings.TrimSpace(channelName) == "" ||
		channelType != strings.TrimSpace(channelType) || channelName != strings.TrimSpace(channelName) ||
		strings.ContainsRune(channelType, 0) || strings.ContainsRune(channelName, 0) {
		return "", database.NewError(database.CodeInvalid, "SQL bridge channel identity is invalid")
	}
	var response ResolveChannelResponse
	if err := client.Call(
		ctx,
		RPCDomain,
		RPCVersion,
		RPCOperationResolve,
		ResolveChannelRequest{ChannelType: channelType, ChannelName: channelName},
		&response,
	); err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "SQL bridge channel store identity is invalid")
	}
	return response.StoreID, nil
}

// ValidatePingRequest validates the logical authority of a bridge ping.
func ValidatePingRequest(request PingRequest) error {
	return validateTarget(request.Target)
}

// ValidateExecRequest validates authority, policy, transaction identity, and
// bounded detached arguments. Broker-side handlers must repeat this validation;
// the private driver also applies it before transport dispatch.
func ValidateExecRequest(request ExecRequest) error {
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if err := ValidateStatement(request.Statement, request.Target.Mode); err != nil {
		return err
	}
	if request.TransactionID != "" && !validTransactionID(request.TransactionID) {
		return database.NewError(database.CodeInvalid, "SQL bridge transaction identity is invalid")
	}
	return validateRPCArguments(request.Arguments)
}

// ValidateQueryRequest validates one query request before broker dispatch.
func ValidateQueryRequest(request QueryRequest) error {
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if err := ValidateStatement(request.Statement, request.Target.Mode); err != nil {
		return err
	}
	if request.TransactionID != "" && !validTransactionID(request.TransactionID) {
		return database.NewError(database.CodeInvalid, "SQL bridge transaction identity is invalid")
	}
	return validateRPCArguments(request.Arguments)
}

// ValidateBeginRequest validates the closed database/sql isolation range and
// logical bridge target. Provider transaction details remain broker-side.
func ValidateBeginRequest(request BeginRequest) error {
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if request.Isolation < 0 || request.Isolation > 7 {
		return database.NewError(database.CodeUnsupported, "SQL bridge isolation level is unsupported")
	}
	return nil
}

// ValidateTransactionRequest validates commit/rollback logical authority.
func ValidateTransactionRequest(request TransactionRequest) error {
	if err := validateTarget(request.Target); err != nil {
		return err
	}
	if !validTransactionID(request.TransactionID) {
		return database.NewError(database.CodeInvalid, "SQL bridge transaction identity is invalid")
	}
	return nil
}

func validateRPCArguments(arguments []Argument) error {
	if len(arguments) > MaxArguments {
		return database.NewError(database.CodeInvalid, "SQL bridge has too many arguments")
	}
	seenOrdinals := make(map[int]struct{}, len(arguments))
	totalBytes := 0
	for _, argument := range arguments {
		if argument.Ordinal <= 0 || len(argument.Name) > maximumArgumentNameBytes ||
			!utf8.ValidString(argument.Name) || containsNUL(argument.Name) {
			return database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
		}
		if _, duplicate := seenOrdinals[argument.Ordinal]; duplicate {
			return database.NewError(database.CodeInvalid, "SQL bridge argument ordinal is duplicated")
		}
		seenOrdinals[argument.Ordinal] = struct{}{}
		size, valid := validWireValue(argument.Value)
		if !valid {
			return database.NewError(database.CodeInvalid, "SQL bridge argument is invalid")
		}
		totalBytes += size + len(argument.Name)
		if totalBytes > MaxResultValueBytes {
			return database.NewError(database.CodeInvalid, "SQL bridge arguments are too large")
		}
	}
	return nil
}

func validWireValue(value Value) (int, bool) {
	bytesAbsent := value.Bytes == nil
	switch value.Kind {
	case ValueNull:
		return 0, value.Integer == 0 && value.Float == 0 && !value.Boolean && bytesAbsent &&
			value.String == "" && value.Time == ""
	case ValueInteger:
		return 8, value.Float == 0 && !value.Boolean && bytesAbsent && value.String == "" && value.Time == ""
	case ValueFloat:
		return 8, value.Integer == 0 && !value.Boolean && bytesAbsent && value.String == "" &&
			value.Time == "" && !math.IsNaN(value.Float) && !math.IsInf(value.Float, 0)
	case ValueBoolean:
		return 1, value.Integer == 0 && value.Float == 0 && bytesAbsent && value.String == "" && value.Time == ""
	case ValueBytes:
		return len(value.Bytes), value.Integer == 0 && value.Float == 0 && !value.Boolean &&
			value.String == "" && value.Time == "" && len(value.Bytes) <= MaxValueBytes
	case ValueString:
		return len(value.String), value.Integer == 0 && value.Float == 0 && !value.Boolean &&
			bytesAbsent && value.Time == "" && len(value.String) <= MaxValueBytes &&
			utf8.ValidString(value.String) && !containsNUL(value.String)
	case ValueTime:
		if value.Integer != 0 || value.Float != 0 || value.Boolean || !bytesAbsent || value.String != "" {
			return 0, false
		}
		_, err := time.Parse(time.RFC3339Nano, value.Time)
		return len(value.Time), err == nil && strings.TrimSpace(value.Time) == value.Time
	default:
		return 0, false
	}
}

// BrokerRPC adapts the authenticated database client to the typed bridge
// operation set. The broker-side handler is intentionally wired separately.
type BrokerRPC struct {
	client *database.Client
}

func NewBrokerRPC(client *database.Client) *BrokerRPC {
	return &BrokerRPC{client: client}
}

func (rpc *BrokerRPC) Ping(ctx context.Context, request PingRequest) (PingResponse, error) {
	var response PingResponse
	if err := ValidatePingRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(ctx, RPCOperationPing, request, &response, false)
	return response, err
}

func (rpc *BrokerRPC) Exec(ctx context.Context, request ExecRequest) (ExecResponse, error) {
	var response ExecResponse
	if err := ValidateExecRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(ctx, RPCOperationExec, request, &response, true)
	return response, err
}

func (rpc *BrokerRPC) Query(ctx context.Context, request QueryRequest) (QueryResponse, error) {
	var response QueryResponse
	if err := ValidateQueryRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(
		ctx,
		RPCOperationQuery,
		request,
		&response,
		StatementMayMutate(request.Statement, request.Target.Mode),
	)
	return response, err
}

func (rpc *BrokerRPC) Begin(ctx context.Context, request BeginRequest) (BeginResponse, error) {
	var response BeginResponse
	if err := ValidateBeginRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(ctx, RPCOperationBegin, request, &response, true)
	return response, err
}

func (rpc *BrokerRPC) Renew(
	ctx context.Context,
	request TransactionRequest,
) (TransactionLeaseResponse, error) {
	var response TransactionLeaseResponse
	if err := ValidateTransactionRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(ctx, RPCOperationRenew, request, &response, true)
	return response, err
}

func (rpc *BrokerRPC) Commit(
	ctx context.Context,
	request TransactionRequest,
) (TransactionResponse, error) {
	var response TransactionResponse
	if err := ValidateTransactionRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(ctx, RPCOperationCommit, request, &response, true)
	return response, err
}

func (rpc *BrokerRPC) Rollback(
	ctx context.Context,
	request TransactionRequest,
) (TransactionResponse, error) {
	var response TransactionResponse
	if err := ValidateTransactionRequest(request); err != nil {
		return response, err
	}
	err := rpc.call(ctx, RPCOperationRollback, request, &response, true)
	return response, err
}

func (rpc *BrokerRPC) call(
	ctx context.Context,
	operation string,
	request any,
	response any,
	mutation bool,
) error {
	if rpc == nil || rpc.client == nil {
		return database.NewError(database.CodeUnavailable, "SQL bridge broker client is unavailable")
	}
	if mutation {
		return rpc.client.CallWithOptions(
			ctx,
			RPCDomain,
			RPCVersion,
			operation,
			request,
			response,
			database.CallOptions{Mutation: true},
		)
	}
	return rpc.client.Call(ctx, RPCDomain, RPCVersion, operation, request, response)
}

var _ RPC = (*BrokerRPC)(nil)
