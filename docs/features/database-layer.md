# Database Protocol Foundation

## Feature ID

`FR-DATABASE`

## Behavior Summary

PicoClaw defines a provider-neutral value and wire contract for a future
single-owner database broker. This foundation supplies opaque logical store
identities, deterministic readiness, bounded structured errors, canonical JSON
frames, versioned request envelopes with an optional first-class logical store
target, handler interfaces, and bounded idempotency records. It does not
activate a transport, provider, catalog, migration, command, or application
persistence path.

## Reconstruction Notes

- Similarity target: recreate the protocol kernel without exposing SQL handles,
  provider names, file paths, DSNs, or driver errors.
- Core types/functions: `StoreID`, `StoreStatus`, `Error`, `RequestEnvelope`,
  `ResponseEnvelope`, `Request`, `Handler`, canonical JSON helpers, frame
  readers/writers, and the epoch idempotency registry.
- Runtime ordering: validate identities and envelopes, bind any logical
  `StoreID` target to the detached handler request, decode one canonical object,
  dispatch through a typed handler, encode exactly one payload or structured
  error, and retain store-bound keyed mutation outcomes for replay.
- Non-obvious constraints: ratios and transports are outside this layer;
  canonical bytes and bounded values are part of the future authentication and
  replay boundary.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-001` | MUST | A caller parses a logical store identity, validates a readiness snapshot, or requires broker readiness. | Store IDs are opaque, canonical lowercase slash-separated segments; readiness is detached, ID-sorted, and uses only the declared states. `RequireBrokerReady` revalidates the complete supplied snapshot before checking required stores. | Validation mutates no caller input or durable state. | Empty, oversized, padded, path-like, duplicated, or invalid IDs, invalid extra statuses, and inconsistent ready/error pairs fail with structured errors. | Applications must address logical stores without learning provider locations or trusting a partially validated snapshot. |
| `FR-DATABASE-002` | MUST | Protocol code creates, matches, or classifies an error. | Only the closed public error-code set and one bounded, single-line safe message cross the boundary. | No durable state changes. | Unknown codes collapse to `Internal`; nil, canceled, deadline, and provider-like errors map without leaking implementation details. | Provider failures and secrets must not become wire API. |
| `FR-DATABASE-003` | MUST | A value is encoded or decoded as protocol JSON or a length-prefixed frame. | JSON is compact, deterministically keyed, and number-canonical; frames use a four-byte big-endian length followed by one canonical JSON value. | Encoding and decoding mutate only caller-owned buffers and destinations. | Alternate encodings, duplicate keys, trailing data, empty frames, short I/O, and frames above 128 MiB fail before an oversized allocation. | Authentication, replay, and cross-process behavior require stable bytes and bounded memory. |
| `FR-DATABASE-004` | MUST | Protocol code structurally validates a versioned request or validates a response against its expected request and epoch. | Requests carry token and epoch fields for later transport admission and structurally require a safe request ID, domain, domain version, operation, positive deadline, optional canonical logical StoreID, optional idempotency key, and object payload; responses match the expected request and epoch and contain exactly one payload or error. | Successful validation copies the logical target and payload into a detached handler request. | Unsupported versions, malformed names or StoreIDs, non-positive deadlines, non-object payloads, mismatched response epochs/requests, or ambiguous responses fail closed. Whether a StoreID is required or authorized belongs to the later scoped server layer. | Later transports and providers need one stable structural dispatch contract without this dormant foundation claiming live authentication or storage authority. |
| `FR-DATABASE-005` | MUST | A keyed mutation begins or completes within one broker epoch. | Identical StoreID/operation/key/fingerprint requests coalesce and replay a detached retained response; the StoreID participates in both the operation key and request fingerprint, so the same idempotency-key text used for a different StoreID occupies a distinct target-bound record. | The bounded in-memory registry retains records and result bytes for the whole epoch and never evicts a completed key. | A full registry rejects before dispatch; same-target key reuse with a different fingerprint conflicts; canceled waiters receive `Deadline`; an outcome exceeding retained-result bounds becomes `OutcomeUnknown` and cannot trigger shutdown. | Eviction, cross-store replay, or ambiguous retries could duplicate a committed mutation or return another store's result. |
| `FR-DATABASE-006` | MUST | A caller constructs or receives a request with a logical store target. | The optional `store_id` wire member is copied exactly into `Request.StoreID`; it remains an opaque catalog identity and is authenticated with the rest of the admitted envelope. | Copying the target grants no provider, path, migration, claim, or readiness authority. | An invalid nonempty StoreID fails structural validation; omission is accepted only for policies such as control or legacy unscoped dispatch that explicitly permit it. Scoped transport rejects a second payload-local target or store-resolution operation before business logic. | Store authorization must be enforceable before a typed payload reaches business logic, without placing a path or provider selector in arbitrary adapter payloads. |

## Data And State Model

This layer defines only protocol values and an in-memory epoch registry.
`StoreID`, including a request's first-class target, is not a path, URI, DSN,
driver name, filename, or provider capability. Readiness and error values are
safe projections. No database file, discovery manifest, transport endpoint,
catalog, schema, claim, or migration state is introduced by this feature stage.

## Surface Ownership

Owns: CODE pkg/database/doc.go
Owns: CODE pkg/database/canonical.go
Owns: CODE pkg/database/errors.go
Owns: CODE pkg/database/frame.go
Owns: CODE pkg/database/idempotency.go
Owns: CODE pkg/database/protocol.go
Owns: CODE pkg/database/types.go
Owns: TEST pkg/database/protocol_test.go *
Owns: TEST pkg/database/protocol_coverage_test.go *
Owns: TEST pkg/database/protocol_store_test.go *
Owns: TEST pkg/database/idempotency_store_test.go *
Owns: TEST pkg/database/store_scope_architecture_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Go value | `StoreID`, `StoreStatus`, `BrokerStatus` | Opaque logical identity and deterministic readiness projection. | `FR-DATABASE-001` |
| Go error | `Error`, `ErrorCode`, `CodeOf` | Closed, bounded, provider-neutral failure vocabulary. | `FR-DATABASE-002` |
| Wire | `MarshalCanonical`, `UnmarshalCanonical`, `WriteFrame`, `ReadFrame` | Canonical JSON inside a bounded four-byte length prefix. | `FR-DATABASE-003` |
| Dispatch | `RequestEnvelope`, `ResponseEnvelope`, `Request`, `Handler` | Versioned, epoch-bound typed operation contract with an optional first-class logical StoreID target. | `FR-DATABASE-004`, `FR-DATABASE-006` |
| Registry | `idempotencyRegistry` | Whole-epoch coalescing and replay for keyed mutations. | `FR-DATABASE-005` |

## Algorithms And Ordering

1. Parse and validate every logical identity before it enters readiness or a
   protocol envelope.
2. Canonicalize JSON recursively, including stable number spellings, and reject
   any byte representation that differs from the canonical result.
3. Reject empty or oversized frame lengths before allocating or reading a
   payload, then decode exactly one canonical value.
4. Structurally validate protocol version, safe names, any nonempty StoreID, a
   positive deadline, idempotency key, and object payload before constructing a
   detached handler request; carry token and epoch values for the later server
   admission layer.
5. For a keyed mutation, derive a StoreID-bound operation key and request
   fingerprint, reserve or await its epoch record, and retain a detached bounded
   response before waking replay waiters.

## Cross-Feature Behavior

This stage changes no active persistence path. `FR-DATABASE-IPC` now supplies
the dormant local transport and single-owner server lifecycle behind these
values. `FR-DATABASE-PROVIDER-CATALOG` supplies a separate dormant internal
logical-to-physical inventory without exposing its candidate paths or activating
a provider. Later PRs add providers, readiness, migration, CLI composition, and
domain adoption without changing their application-facing meaning.

## Failure And Edge Cases

- Invalid or provider-shaped values never cross as raw errors or locations.
- Readiness fails closed when required identities are missing, duplicated, or
  not ready.
- Canonical JSON rejects whitespace, reordered or duplicate keys, noncanonical
  numbers, unknown strict fields, and trailing content.
- Frame reads and writes handle short I/O and enforce the size ceiling.
- Invalid nonempty request StoreIDs never reach a handler; an empty target has
  no implicit authority and is subject to the server's control/unscoped policy.
- Idempotency wait cancellation does not remove or alter the retained operation.
- Reusing an idempotency key cannot replay an outcome across StoreIDs.
- This foundation does not imply that a broker, provider, or migration command
  is active.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-001`, `FR-DATABASE-002` | [pkg/database/protocol_test.go](../../pkg/database/protocol_test.go), [pkg/database/protocol_coverage_test.go](../../pkg/database/protocol_coverage_test.go) |
| `FR-DATABASE-003`, `FR-DATABASE-004` | [pkg/database/protocol_test.go](../../pkg/database/protocol_test.go), [pkg/database/protocol_coverage_test.go](../../pkg/database/protocol_coverage_test.go), [pkg/database/protocol_store_test.go](../../pkg/database/protocol_store_test.go) |
| `FR-DATABASE-005` | [pkg/database/protocol_coverage_test.go](../../pkg/database/protocol_coverage_test.go), [pkg/database/idempotency_store_test.go](../../pkg/database/idempotency_store_test.go) |
| `FR-DATABASE-006` | [pkg/database/protocol_store_test.go](../../pkg/database/protocol_store_test.go), [pkg/database/store_scope_architecture_test.go](../../pkg/database/store_scope_architecture_test.go), [pkg/database/store_scope_integration_test.go](../../pkg/database/store_scope_integration_test.go) |

## Implementation Anchors

- [pkg/database/types.go](../../pkg/database/types.go)
- [pkg/database/errors.go](../../pkg/database/errors.go)
- [pkg/database/canonical.go](../../pkg/database/canonical.go)
- [pkg/database/frame.go](../../pkg/database/frame.go)
- [pkg/database/protocol.go](../../pkg/database/protocol.go)
- [pkg/database/idempotency.go](../../pkg/database/idempotency.go)
