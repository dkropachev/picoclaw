package database

import (
	"strings"
	"testing"
	"time"
)

func TestRequestEnvelopeStoreIDValidation(t *testing.T) {
	t.Parallel()

	valid := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "request", Token: "token", BrokerEpoch: "epoch",
		Domain: "repository-reviews", DomainVersion: 1, Operation: "read",
		DeadlineUnixNs: time.Now().Add(time.Minute).UnixNano(), Payload: []byte(`{}`),
	}
	if err := validRequestEnvelope(valid); err != nil {
		t.Fatalf("unscoped compatibility envelope error = %v", err)
	}

	scoped := valid
	scoped.StoreID = "workspace/repository-reviews"
	if err := validRequestEnvelope(scoped); err != nil {
		t.Fatalf("scoped envelope error = %v", err)
	}

	for _, storeID := range []StoreID{
		"Workspace/repository-reviews",
		" workspace/repository-reviews",
		"workspace//repository-reviews",
		"workspace/../repository-reviews",
		StoreID(strings.Repeat("a", maxStoreIDBytes+1)),
	} {
		candidate := valid
		candidate.StoreID = storeID
		if err := validRequestEnvelope(candidate); CodeOf(err) != CodeInvalid {
			t.Errorf("StoreID %q error = %v, want Invalid", storeID, err)
		}
	}

	control := valid
	control.Domain = ControlDomain
	control.DomainVersion = ControlVersion
	control.Operation = ControlOperationPing
	if err := validRequestEnvelope(control); err != nil {
		t.Fatalf("unscoped control envelope error = %v", err)
	}
	control.StoreID = "global/auth"
	if err := validRequestEnvelope(control); CodeOf(err) != CodeInvalid {
		t.Fatalf("scoped control envelope error = %v, want Invalid", err)
	}
}

func TestRequestEnvelopeStoreIDEncodingIsBackwardCompatible(t *testing.T) {
	t.Parallel()

	envelope := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "request", Token: "token", BrokerEpoch: "epoch",
		Domain: "domain", DomainVersion: 1, Operation: "read", DeadlineUnixNs: 1,
		Payload: []byte(`{}`),
	}
	unscoped, err := MarshalCanonical(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unscoped), `"store_id"`) {
		t.Fatalf("unscoped envelope unexpectedly encoded StoreID: %s", unscoped)
	}

	envelope.StoreID = "workspace/repository-reviews"
	scoped, err := MarshalCanonical(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(scoped),
		`"store_id":"workspace/repository-reviews"`,
	) {
		t.Fatalf("scoped envelope omitted StoreID: %s", scoped)
	}
	var decoded RequestEnvelope
	if err := UnmarshalCanonical(scoped, &decoded); err != nil || decoded.StoreID != envelope.StoreID {
		t.Fatalf("decoded scoped envelope = %#v, %v", decoded, err)
	}
}
