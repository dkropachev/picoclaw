package sqlbridge

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestTypedRPCValidationRepeatsAuthorityAndStatementPolicy(t *testing.T) {
	t.Parallel()

	rpc := NewBrokerRPC(nil)
	runtimeTarget := Target{
		StoreID: mustStoreID(t, "channel.matrix.primary-a1b2c3d4"),
		Mode:    ModeRuntime,
	}
	offlineTarget := runtimeTarget
	offlineTarget.Mode = ModeOffline

	_, err := rpc.Exec(context.Background(), ExecRequest{
		Target:    Target{StoreID: mustStoreID(t, "global.auth"), Mode: ModeRuntime},
		Statement: "SELECT 1",
	})
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("unknown target error = %v", err)
	}
	_, err = rpc.Exec(context.Background(), ExecRequest{
		Target: runtimeTarget, Statement: "CREATE TABLE forbidden(id INTEGER)",
	})
	if database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("runtime DDL error = %v", err)
	}
	_, err = rpc.Exec(context.Background(), ExecRequest{
		Target: offlineTarget, Statement: "CREATE TABLE allowed(id INTEGER)",
	})
	if database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("valid offline request reached nil transport with error = %v", err)
	}
	_, err = rpc.Query(context.Background(), QueryRequest{
		Target:    runtimeTarget,
		Statement: "SELECT ?",
		Arguments: []Argument{
			{Ordinal: 1, Value: Value{Kind: ValueInteger, Integer: 1}},
			{Ordinal: 1, Value: Value{Kind: ValueInteger, Integer: 2}},
		},
	})
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("duplicate argument error = %v", err)
	}
	_, err = rpc.Commit(context.Background(), TransactionRequest{
		Target: runtimeTarget, TransactionID: "../../physical",
	})
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid transaction identity error = %v", err)
	}
}

func TestWireValueValidationRejectsConflictingOrOversizedShapes(t *testing.T) {
	t.Parallel()

	target := Target{
		StoreID: mustStoreID(t, "channel.whatsapp.primary-01234567"),
		Mode:    ModeRuntime,
	}
	requests := []QueryRequest{
		{
			Target: target, Statement: "SELECT ?",
			Arguments: []Argument{{
				Ordinal: 1,
				Value:   Value{Kind: ValueNull, String: "conflict"},
			}},
		},
		{
			Target: target, Statement: "SELECT ?",
			Arguments: []Argument{{
				Ordinal: 1,
				Value:   Value{Kind: ValueString, String: string([]byte{0xff})},
			}},
		},
		{
			Target: target, Statement: "SELECT ?",
			Arguments: []Argument{{
				Ordinal: 1,
				Value:   Value{Kind: ValueBytes, Bytes: make([]byte, MaxValueBytes+1)},
			}},
		},
	}
	for index, request := range requests {
		if err := ValidateQueryRequest(request); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("request %d error = %v", index, err)
		}
	}
}
