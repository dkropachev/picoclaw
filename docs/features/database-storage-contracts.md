# Database Storage Contracts

## Feature ID

`FR-DATABASE-STORAGE-CONTRACTS`

## Behavior Summary

PicoClaw defines two dormant provider-neutral primitives for future database
ownership: stable opaque identities for existing local filesystem objects and
an explicitly assembled immutable registry of domain schema/readiness
contracts. Neither primitive opens a database or registers an application
adapter by side effect.

This stage has no production consumer, IPC publication, CLI, runtime wiring,
configuration change, migration execution, or persistence cutover.

## Reconstruction Notes

- Similarity target: give ownership code stable physical alias detection and
  typed domain expectations without path disclosure or global registration.
- Core APIs: `fileidentity.Existing`, `databaseadapter.NewRegistry`,
  `Registry.Domains`, and `Registry.Lookup`.
- Runtime ordering: validate exact input, derive or copy immutable values, sort
  registry domains, and expose only detached results.
- Non-obvious constraints: identities are comparable but opaque; contracts use
  typed schema objects and a declared empty-store policy; callbacks are stored
  but never invoked by registry construction.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-STORAGE-CONTRACTS-001` | MUST | Trusted infrastructure requests the identity of an existing local path. | `Existing` returns a comparable platform-qualified candidate identity for one real regular file or directory: Unix device/inode or Windows 64-bit volume serial plus complete 128-bit file ID. Missing paths return `exists=false`; strings reveal no pathname. A caller must separately canonicalize and validate ancestors, then recheck identity before accepting the candidate as authority. | Identity lookup performs metadata reads only. | Invalid path, final-leaf symlink/reparse, irregular object, identity drift across the lookup, an ambiguous Windows device namespace, or unsupported platform/filesystem identity fails closed with a typed sentinel. | Physical claims must detect hardlink and mount aliases without making paths capabilities. |
| `FR-DATABASE-STORAGE-CONTRACTS-002` | MUST | Trusted infrastructure explicitly constructs a registry of zero or more domain adapters. | At most 1,024 unique lowercase domains map to detached immutable contracts with schema version 1 through MaxInt32, one empty policy, bounded typed schema objects/columns, optional typed import horizon, and optional callback. Domains are sorted and lookup is exact/nil-safe. | Construction retains deep copies and callback identities only; it invokes nothing and creates no global registry. | Invalid, duplicate, excessive, padded, untyped, or inconsistent domain/contract input rejects the whole registry with a bounded provider-neutral error. | Domains need deterministic readiness and migration targets without hidden init order or schema invention. |
| `FR-DATABASE-STORAGE-CONTRACTS-003` | MUST | A caller reads registry domains or looks up an adapter. | Returned slices, required objects, columns, and targets are detached from both caller and retained state. | Reads mutate nothing. | Caller mutation, missing domain, or nil registry cannot alter or expose another contract. | Later composition must safely reuse one immutable configuration-derived registry. |

## Data And State Model

`fileidentity.Identity` contains one private comparable platform key.
`databaseadapter.Registry` contains a private sorted domain index of copied
`Adapter` values. A migration `Target` is a detached typed store ID plus trusted
generation and legacy paths supplied only by later infrastructure.

## Surface Ownership

Owns: CODE internal/fileidentity/**
Owns: TEST internal/fileidentity/*_test.go *
Owns: CODE internal/databaseadapter/**
Owns: TEST internal/databaseadapter/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `fileidentity.Existing`, `Identity.String`, `Identity.Valid` | Resolve a safe opaque stable identity for one existing local object. | `FR-DATABASE-STORAGE-CONTRACTS-001` |
| Internal Go API | `databaseadapter.NewRegistry`, `Registry.Domains`, `Registry.Lookup` | Explicitly assemble and query detached immutable domain contracts. | `FR-DATABASE-STORAGE-CONTRACTS-002`, `FR-DATABASE-STORAGE-CONTRACTS-003` |

## Algorithms And Ordering

1. Reject empty, invalid-UTF-8, or NUL-bearing identity paths.
2. Inspect the final leaf without following a link, obtain the platform stable
   identifier, and repeat identity/type checks before returning a candidate.
   Windows compares two independently opened handles using the complete
   `FILE_ID_INFO` value rather than the legacy 64-bit file index. A later
   consumer canonicalizes and validates ancestors, then repeats identity
   collection before treating that candidate as ownership authority.
3. Validate every explicit adapter domain, schema version, empty policy, typed
   required object, table-column set, and import-horizon dependency.
4. Deep-copy contracts, sort domains, build exact lookup state, and return only
   detached values.

## Cross-Feature Behavior

The provider catalog supplies typed store IDs and candidate namespaces. A later
physical-claims feature consumes file identities; readiness and migration later
consume adapter contracts. The base database protocol receives neither paths
nor callbacks.

## Failure And Edge Cases

- Missing filesystem objects are not errors and produce no identity.
- Final-leaf symlinks and Windows reparses never inherit the target identity.
  `Existing` alone does not validate ancestors; the provider catalog and the
  accepting physical-claim transition retain that responsibility.
- A view cannot substitute for a required table because schema objects carry
  explicit types.
- An import horizon is invalid unless the contract also requires the typed
  `storage_import_horizons` table.
- An empty registry is valid but cannot satisfy a nonempty future catalog.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-STORAGE-CONTRACTS-001` | [internal/fileidentity/identity_test.go](../../internal/fileidentity/identity_test.go), [internal/fileidentity/identity_unix_test.go](../../internal/fileidentity/identity_unix_test.go), [internal/fileidentity/identity_windows_test.go](../../internal/fileidentity/identity_windows_test.go) |
| `FR-DATABASE-STORAGE-CONTRACTS-002`, `FR-DATABASE-STORAGE-CONTRACTS-003` | [internal/databaseadapter/registry_test.go](../../internal/databaseadapter/registry_test.go) |

## Implementation Anchors

- [internal/fileidentity/identity.go](../../internal/fileidentity/identity.go)
- [internal/databaseadapter/registry.go](../../internal/databaseadapter/registry.go)
