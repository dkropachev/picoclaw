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
separate non-previewable error-text domain.

Logger file replacement uses per-file emission leases. A retired file remains
open for records already admitted, then closes exactly once after the final
lease; replacement and reentrant disable do not wait for active writes. New
files request mode `0600`, and newly created parents request `0700`; existing
modes are not changed.
