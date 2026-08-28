# Safe log observations

`Observation` converts sensitive values into fixed scalar metadata and a
domain-separated SHA-256 digest. It never retains or returns the observed raw
value. Structured observations accept only detached, exclusively owned bounded
JSON-compatible graphs; concurrent caller mutation is outside the contract.

Digests provide stable correlation, not secrecy. Low-entropy values can be
guessed offline, so credential, environment, authorization, cookie, header, and
private-key values use presence/count/size observations without a digest.
Presence is encoded by a positive count, so a present empty value uses count
one and zero bytes while absence uses zero for both.

Arbitrary `error` values are not rendered or unwrapped. `ObserveErrorType`
records only a trusted fixed class and concrete named type identity. Callers
that already own a bounded materialized error string may observe it through the
separate non-previewable error-text domain. `ObservePanic` likewise records
only method-free concrete type identity and never formats a recovered value or
captures a stack. Scalar text, byte, path, URL, and identity inputs over 1 MiB
fail before content scanning or hashing.

Logger file replacement uses per-file emission leases. A retired file remains
open for records already admitted, then closes exactly once after the final
lease; replacement and reentrant disable do not wait for active writes. New
files request mode `0600`, and newly created parents request `0700`; existing
modes are not changed.

`DiagnosticPolicy` captures application-preview permission from stored config
only: permission and stored Debug must both be present. Root bindings and
origin-aware rebindings are explicitly revoked with their generation lease;
context cancellation alone does not revoke them. Revocation disables ordinary
lookup but intentionally retains the captured origin cap for a trusted later
rebind. Rebinding always meets origin, current, and parent policies, so a false
capability cannot be widened. `NarrowDiagnosticPolicy` is the synchronous form:
it meets the current live parent and supplied cap, preserves live ancestor
revocation through nested root binds, and must itself be revoked on return.
Lineage is flattened to at most 64 immutable bindings; missing, inactive, or
over-bound lineage fails closed. Rebind retains the established semantics for
ordinary snapshot bindings but cannot revive an inactive live-linked narrow
origin or parent.

Safe diagnostic records use fixed component/message IDs and immutable typed
`SafeFields`; arbitrary keys, strings, errors, maps, and interfaces are not
accepted. `DebugSensitiveCF` is the only preview sink. It supports only the
explicit application prompt/message/response/reasoning/tool-argument pairings,
always emits an observation, and places an escaped preview in a nested JSON
object whose complete wire is at most 4096 bytes. Escaping prevents control and
bidi record forgery; it does not hide secrets embedded in application text.
Tool-argument inputs must be detached, exclusively owned `map[string]any`
graphs. The exact grammar is nil, bool, built-in string, `int`, `int8`,
`int16`, `int32`, `int64`, `uint`, `uint8`, `uint16`, `uint32`, `uint64`,
finite float32/float64, valid RFC 8259 `json.Number`, `[]any`, and
`map[string]any`; `uintptr`, named types, pointers, arrays,
structs, methods, cycles, and other values fail closed. Float32 is normalized
to float64. Nil and empty containers remain distinct in the observation.
Bounds are depth 16, 4,096 nodes, 512 members per collection, and 1 MiB of
observed graph data. Invalid UTF-8 remains safely observed but is never
previewed. Preview admission also requires the fixed component, message,
sensitivity class, domain, and raw-value kind to match one explicit tuple.
