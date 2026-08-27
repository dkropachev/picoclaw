package logger

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestObservationAdversarialGoldenVectorsAndFraming(t *testing.T) {
	tests := []struct {
		name string
		got  Observation
		want string
	}{
		{
			name: "empty text",
			got:  ObserveText(ObservationDomainPrompt, ""),
			want: "sha256:03bd5f96d9838ae9f0f8b9679ea96d7f8c375c1c98c061ee97dde3eadc5935cc",
		},
		{
			name: "text abc",
			got:  ObserveText(ObservationDomainPrompt, "abc"),
			want: "sha256:420e450743bcd116184446e57ab503db4cd7be73153c5235a4cb0da2b2cdaf44",
		},
		{
			name: "nil bytes",
			got:  ObserveBytes(ObservationDomainPrompt, nil),
			want: "sha256:fb039f7bb7d1f0f90d1cf4b9357b4fd2b9ee2937b166bab5600f9b97ca402c65",
		},
		{
			name: "empty nonnil bytes",
			got:  ObserveBytes(ObservationDomainPrompt, []byte{}),
			want: "sha256:708b287ce55ae8af53171c87697aba2d21ffaa92d74f304238ab3c3c6f759bf6",
		},
		{
			name: "structured graph",
			got: ObserveJSONValue(ObservationDomainToolArguments, map[string]any{
				"b": []any{true, nil},
				"a": int64(1),
			}),
			want: "sha256:17edcd02d02f5d784e1ce8dd003dcc1364fa29c4f8ce534616dc6467cf13f57b",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got.State != observationStateComplete || test.got.Digest != test.want {
				t.Fatalf("observation = %#v; want digest %q", test.got, test.want)
			}
		})
	}

	assertDifferentObservationDigests(t,
		[]any{"ab", "c"},
		[]any{"a", "bc"},
	)
	assertDifferentObservationDigests(t,
		map[string]any{"ab": "c"},
		map[string]any{"a": "bc"},
	)
	assertDifferentObservationDigests(t,
		[]any{[]any{"a"}, "b"},
		[]any{"a", []any{"b"}},
	)

	invalidUTF8Key := string([]byte{0xff, 0x00, 'k'})
	orderedFirst := map[string]any{invalidUTF8Key: json.Number("1.0"), "\x00": int64(2)}
	orderedSecond := map[string]any{"\x00": int64(2), invalidUTF8Key: json.Number("1.0")}
	first := ObserveJSONValue(ObservationDomainToolArguments, orderedFirst)
	second := ObserveJSONValue(ObservationDomainToolArguments, orderedSecond)
	if first.State != observationStateComplete || first.Digest != second.Digest {
		t.Fatalf("exact-byte map ordering changed digest: first=%#v second=%#v", first, second)
	}
}

func TestObservationAdversarialExactBoundsAndSharedAliases(t *testing.T) {
	atDepthLimit := any("leaf")
	for range maxObservationDepth - 1 {
		atDepthLimit = []any{atDepthLimit}
	}
	if got := ObserveJSONValue(ObservationDomainToolArguments, atDepthLimit); got.State != observationStateComplete {
		t.Fatalf("exact depth limit rejected: %#v", got)
	}

	atMemberLimit := make([]any, maxObservationMembers)
	if got := ObserveJSONValue(ObservationDomainToolArguments, atMemberLimit); got.State != observationStateComplete {
		t.Fatalf("exact member limit rejected: %#v", got)
	}

	atByteLimit := strings.Repeat("x", maxObservationBytes-9)
	if got := ObserveJSONValue(ObservationDomainToolArguments, atByteLimit); got.State != observationStateComplete ||
		got.Bytes != maxObservationBytes {
		t.Fatalf("exact byte limit = %#v", got)
	}

	// Root + 512 child slices + 3,583 nil scalars = exactly 4,096 nodes.
	atNodeLimit := make([]any, maxObservationMembers)
	for index := range atNodeLimit {
		memberCount := 7
		if index == len(atNodeLimit)-1 {
			memberCount = 6
		}
		atNodeLimit[index] = make([]any, memberCount)
	}
	if got := ObserveJSONValue(ObservationDomainToolArguments, atNodeLimit); got.State != observationStateComplete {
		t.Fatalf("exact node limit rejected: %#v", got)
	}
	atNodeLimit[len(atNodeLimit)-1] = make([]any, 7)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, atNodeLimit),
		reasonNodeLimit,
	)

	shared := []any{"shared"}
	graph := []any{shared, shared}
	if got := ObserveJSONValue(ObservationDomainToolArguments, graph); got.State != observationStateComplete {
		t.Fatalf("shared acyclic alias classified as cycle: %#v", got)
	}
}

func TestObservationAdversarialScalarWidthsAndClosedValidation(t *testing.T) {
	domain := ObservationDomainToolArguments
	signedDigest := ObserveJSONValue(domain, int64(-7)).Digest
	for _, value := range []any{int16(-7), int32(-7)} {
		if got := ObserveJSONValue(domain, value); got.Digest != signedDigest {
			t.Fatalf("signed scalar %T digest = %q, want %q", value, got.Digest, signedDigest)
		}
	}
	unsignedDigest := ObserveJSONValue(domain, uint64(7)).Digest
	for _, value := range []any{uint(7), uint16(7), uint32(7)} {
		if got := ObserveJSONValue(domain, value); got.Digest != unsignedDigest {
			t.Fatalf("unsigned scalar %T digest = %q, want %q", value, got.Digest, unsignedDigest)
		}
	}

	assertUnavailableObservation(
		t,
		ObserveBytes(ObservationDomainErrorType, []byte("canary")),
		reasonInvalidDomain,
	)
	for _, value := range []string{"1://host", "a_://host"} {
		if got := ObservePath(value); got.Class != "relative" {
			t.Fatalf("path %q class = %q, want relative", value, got.Class)
		}
	}

	base := ObserveText(ObservationDomainPrompt, "wire-canary")
	mutations := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "complete reason", mutate: func(value *Observation) { value.ReasonCode = reasonInvalidBound }},
		{name: "unknown state", mutate: func(value *Observation) { value.State = "future" }},
		{name: "malformed digest", mutate: func(value *Observation) {
			value.Digest = value.Digest[:len(value.Digest)-1] + "g"
		}},
		{name: "unknown class", mutate: func(value *Observation) { value.Class = "future" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			sealObservation(&changed)
			if validObservation(changed) {
				t.Fatalf("invalid sealed wire accepted: %#v", changed)
			}
		})
	}
}

func TestObservationAdversarialAggregateByteAndMapBounds(t *testing.T) {
	domain := ObservationDomainToolArguments

	tooManyMapMembers := make(map[string]any, maxObservationMembers+1)
	for index := 0; index <= maxObservationMembers; index++ {
		tooManyMapMembers[fmt.Sprintf("key-%03d", index)] = nil
	}
	assertUnavailableObservation(
		t,
		ObserveJSONValue(domain, tooManyMapMembers),
		reasonMemberLimit,
	)

	maxEncodedString := strings.Repeat("s", maxObservationBytes-9)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(domain, []any{maxEncodedString}),
		reasonByteLimit,
	)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(domain, map[string]any{strings.Repeat("k", maxObservationBytes): nil}),
		reasonByteLimit,
	)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(domain, map[string]any{maxEncodedString: nil}),
		reasonByteLimit,
	)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(domain, map[string]any{"": maxEncodedString}),
		reasonByteLimit,
	)

	// The child fits the canonical byte budget, but its collection frame does
	// not. This exercises the aggregate frame bound separately from append.
	frameOverflowString := strings.Repeat("f", maxObservationBytes-26)
	assertUnavailableObservation(
		t,
		ObserveJSONValue(domain, []any{frameOverflowString}),
		reasonByteLimit,
	)
}

type observationAdversarialMethodValue struct {
	calls *atomic.Int64
}

func (value observationAdversarialMethodValue) String() string {
	value.calls.Add(1)
	panic("String must not be called")
}

func (value observationAdversarialMethodValue) GoString() string {
	value.calls.Add(1)
	panic("GoString must not be called")
}

func (value observationAdversarialMethodValue) MarshalJSON() ([]byte, error) {
	value.calls.Add(1)
	panic("MarshalJSON must not be called")
}

type observationAdversarialMethodError struct {
	calls *atomic.Int64
}

func (err *observationAdversarialMethodError) Error() string {
	err.calls.Add(1)
	panic("Error must not be called")
}

func (err *observationAdversarialMethodError) Unwrap() error {
	err.calls.Add(1)
	panic("Unwrap must not be called")
}

func (err *observationAdversarialMethodError) Is(error) bool {
	err.calls.Add(1)
	panic("Is must not be called")
}

func (err *observationAdversarialMethodError) As(any) bool {
	err.calls.Add(1)
	panic("As must not be called")
}

func (err *observationAdversarialMethodError) Format(fmt.State, rune) {
	err.calls.Add(1)
	panic("Format must not be called")
}

func TestObservationAdversarialMethodsAreNeverInvoked(t *testing.T) {
	var valueCalls atomic.Int64
	value := observationAdversarialMethodValue{calls: &valueCalls}
	assertUnavailableObservation(
		t,
		ObserveJSONValue(ObservationDomainToolArguments, map[string]any{"value": value}),
		reasonUnsupportedType,
	)
	if got := valueCalls.Load(); got != 0 {
		t.Fatalf("structured observation invoked %d hostile method(s)", got)
	}

	var errorCalls atomic.Int64
	errorValue := &observationAdversarialMethodError{calls: &errorCalls}
	observed := ObserveErrorType(ErrorClassProvider, errorValue)
	if observed.State != observationStateComplete || observed.Count != 1 {
		t.Fatalf("error observation = %#v", observed)
	}
	if got := errorCalls.Load(); got != 0 {
		t.Fatalf("error observation invoked %d hostile method(s)", got)
	}

	var typedNil *observationAdversarialMethodError
	observed = ObserveErrorType(ErrorClassUnknown, typedNil)
	if observed.State != observationStateComplete || observed.Count != 0 || !validObservationDigest(observed.Digest) {
		t.Fatalf("typed-nil error observation = %#v", observed)
	}
	if got := errorCalls.Load(); got != 0 {
		t.Fatalf("typed-nil observation invoked %d hostile method(s)", got)
	}
}

func TestObservationFieldsRejectValidShapedPublicMutation(t *testing.T) {
	base := ObserveText(ObservationDomainPrompt, "mutation-canary")
	otherDigest := ObserveText(ObservationDomainPrompt, "other-value").Digest
	mutations := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "class", mutate: func(value *Observation) { value.Class = "present" }},
		{name: "bytes", mutate: func(value *Observation) { value.Bytes++ }},
		{name: "runes", mutate: func(value *Observation) { value.Runes-- }},
		{name: "utf8", mutate: func(value *Observation) { value.UTF8Valid = false }},
		{name: "count", mutate: func(value *Observation) { value.Count = 0 }},
		{name: "valid digest", mutate: func(value *Observation) { value.Digest = otherDigest }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			fields := ObservationFields(ObservationPrefixPrompt, changed)
			if fields["prompt_state"] != observationStateUnavailable ||
				fields["prompt_reason_code"] != reasonInvalidBound {
				t.Fatalf("valid-shaped public mutation was accepted: %#v", fields)
			}
		})
	}
}

func assertDifferentObservationDigests(t *testing.T, left, right any) {
	t.Helper()
	leftObservation := ObserveJSONValue(ObservationDomainToolArguments, left)
	rightObservation := ObserveJSONValue(ObservationDomainToolArguments, right)
	if leftObservation.State != observationStateComplete ||
		rightObservation.State != observationStateComplete ||
		leftObservation.Digest == rightObservation.Digest {
		t.Fatalf("framing collision: left=%#v right=%#v", leftObservation, rightObservation)
	}
}
