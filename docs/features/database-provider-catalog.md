# Database Provider Catalog Foundation

## Feature ID

`FR-DATABASE-PROVIDER-CATALOG`

## Behavior Summary

PicoClaw defines a dormant internal catalog for the future single-owner
database provider. Given an existing canonical PicoClaw home and one validated
configuration snapshot, it deterministically inventories opaque logical store
IDs, their provider-private candidate generation paths, legacy-input roots, and
required-store policy.

This stage does not bind or open a database provider, inspect schema readiness,
claim a physical generation, publish a catalog fingerprint, start a broker, or
change an application persistence path. No production consumer imports the
catalog yet.

## Reconstruction Notes

- Similarity target: recreate a trusted internal logical-to-physical inventory
  without making filesystem locations part of the database protocol or an
  application API.
- Core types/functions: internal `Options`, `Spec`, and `Catalog` values,
  `Build`, `Project`, exact lookup, detached snapshots, and deterministic
  dynamic channel identities.
- Runtime ordering: accept the already trusted canonical home, resolve every
  configured path in its declared context, generate canonical slash-separated
  store IDs, canonicalize existing leaves when building, reject catalog
  collisions, sort by ID, and publish one immutable in-memory snapshot.
- Non-obvious constraints: a catalog path is a candidate provider identity, not
  authority to open it; missing generation and legacy leaves are valid; legacy
  directory roots may intentionally contain catalogued generations; a future
  provider must revalidate the final generation identity at its point of use.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-PROVIDER-CATALOG-001` | MUST | Privileged database infrastructure calls `Build` or `Project` with `Options`. | `Options.Home` is an existing directory whose absolute, cleaned, resolved identity is the same trusted identity accepted by `CanonicalHome`; `Options.Config` is non-nil and treated as one validated immutable snapshot. `ConfigPath` and `UserHome` supply explicit path-resolution context and are not read from ambient environment. | Catalog construction creates, chmods, opens, or removes nothing. | A missing, relative, padded, NUL-bearing, symlinked, non-directory, or canonically different home; a nil configuration; an invalid config path; or a non-absolute explicit user home fails before inventory publication. | Every later provider decision must share the IPC owner's exact home identity and configuration generation without ambient path authority. |
| `FR-DATABASE-PROVIDER-CATALOG-002` | MUST | The catalog derives fixed, workspace, agent-workspace, or enabled-channel entries. | Every ID follows the `FR-DATABASE` lowercase slash-segment contract. Fixed IDs use their declared names; additional workspaces use `workspace/<path-digest>/<store>`; Matrix and WhatsApp use `channel/<type>/<slug>-<digest>`. Entries are ID-sorted, exact-looked-up, and returned only as detached copies, including detached legacy-root slices. | Construction mutates only a new process-local snapshot. | Invalid, padded, duplicate, empty-segment, traversal-like, oversized, or nondeterministic identities fail closed; caller mutation of a returned `Spec` cannot alter retained catalog state. | Logical authority must remain stable, provider-neutral, and safe to transmit later without revealing a path. |
| `FR-DATABASE-PROVIDER-CATALOG-003` | MUST | Catalog construction resolves a candidate generation or legacy input from trusted configuration and explicit `Options` context. | Home-scoped stores resolve beneath the canonical home; primary workspace stores resolve from the configured/default primary workspace; additional workspace stores resolve from each distinct configured agent workspace; Git inventory/checkpoints resolve from the effective Git-workspace root; event, evolution, Matrix, and WhatsApp overrides resolve in their documented workspace or explicit user-home context. A relative config path is anchored to the canonical home, and its directory supplies launcher legacy context. `Project` produces absolute cleaned identities without inspecting mutable leaves, while `Build` canonicalizes every existing ancestor and leaf. | Resolution performs metadata reads only. | Surrounding whitespace, NUL bytes, ambiguous platform spellings, implicit `~` expansion, unsafe existing ancestors, symlink/reparse aliases, an existing generation that is not regular, or an existing legacy leaf that is neither a regular file nor a directory fails closed. Missing candidate generation and legacy leaves remain representable. | Relative configuration has meaning only within an explicit trusted context, and inventory must not create state while resolving it. |
| `FR-DATABASE-PROVIDER-CATALOG-004` | MUST | The complete candidate inventory is validated before publication. | Logical IDs are unique. Each database, WAL, SHM, and rollback-journal namespace is reserved together; lexical, platform case-folded, ancestor/descendant, and existing same-file or hardlink aliases across generation reservations are rejected. A generation member whose platform path key exactly equals any declared legacy root, or whose existing file is a hardlink alias of an existing legacy file, is rejected. Exact or existing same-file legacy roots across stores are rejected; ancestor/descendant legacy overlap remains allowed, and a legacy directory may contain a catalogued generation because downstream enumeration must exclude generation members explicitly. | Validation retains no filesystem handle and changes no file. | A main path aliasing another main or sidecar, overlapping generation namespaces, an irregular generation member, a duplicate legacy identity, or an exact generation-to-legacy identity aborts the whole catalog. | One future provider owner cannot safely assign two logical authorities to one physical generation or mistake a live generation for legacy input. |
| `FR-DATABASE-PROVIDER-CATALOG-005` | MUST | Repository code attempts to consume or extend this foundation. | Production imports remain confined to `internal/storecatalog` itself; an architecture guard rejects outside production imports during this dormant stage. A future provider must consume the complete trusted `Spec` and revalidate ownership, type, link count, canonical path, and generation identity immediately before and after opening rather than treating the catalog snapshot as a capability. | This stage publishes no process-global catalog and opens no provider handle. | Direct application consumption, a public path projection, provider fallback, readiness claim, fingerprint publication, or use of a stale catalog path as sufficient authority is rejected or remains unavailable. | Landing inventory separately must not create a second database owner or weaken the protocol's opaque `StoreID` boundary. |

## Data And State Model

`Options` supplies the canonical home, validated config snapshot, config-file
path, and optional explicit user-home expansion root. None is discovered from
process environment. `Catalog` is one immutable process-local snapshot. Its
home, ordered specs, and lookup map are private; `Home`, `All`, and `Lookup`
return scalar values or detached copies. Each internal `Spec` contains a
logical ID, domain identifier, candidate
database path, zero or more legacy roots, and required-store policy. Neither
the protocol nor any application package receives a `Spec`.

Path contexts are fixed as follows:

| Logical namespace | Candidate path context |
| --- | --- |
| `global/auth`, `global/model-catalogs`, `global/tool-adaptation`, `launcher/auth`, `channel/wecom`, `channel/weixin` | Existing canonical PicoClaw home. |
| `global/git-workspace-inventory`, `global/pr-workspace-checkpoints` | Effective Git-workspace root: the default is beneath the primary workspace; a relative configured root is canonical-home-relative. |
| `workspace/<store>` | Configured or default primary workspace. |
| `workspace/<path-digest>/<store>` | One distinct configured agent workspace; the digest identifies its absolute cleaned path without exposing that path in the ID. |
| `channel/matrix/<name-digest>`, `channel/whatsapp/<name-digest>` | Channel-configured storage root; a relative override is anchored to the primary workspace and an empty override uses its channel default there. |

The event database override is relative to its workspace unless absolute or
explicitly user-home-relative. The primary evolution override is relative to
the primary workspace unless absolute. `ConfigPath` defaults to
`<canonical-home>/config.json`; a relative value is canonical-home-relative,
and the launcher legacy configuration candidate is adjacent to that resolved
path. A `~` path is accepted only with an explicit absolute `UserHome`. These
rules do not make any candidate exist and do not authorize reading its contents.

## Surface Ownership

Owns: CODE internal/storecatalog/catalog.go
Owns: CODE internal/storecatalog/path_*.go
Owns: TEST internal/storecatalog/*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `Build(options)` | Canonicalize existing catalog leaves and reject logical, generation, and exact legacy-file collisions without opening a provider. | `FR-DATABASE-PROVIDER-CATALOG-001`, `FR-DATABASE-PROVIDER-CATALOG-003`, `FR-DATABASE-PROVIDER-CATALOG-004` |
| Internal Go API | `Project(options)` | Produce the same logical inventory and absolute candidate paths without inspecting mutable generation or legacy leaves. | `FR-DATABASE-PROVIDER-CATALOG-001`, `FR-DATABASE-PROVIDER-CATALOG-002`, `FR-DATABASE-PROVIDER-CATALOG-003` |
| Internal Go value | `Catalog`, `Spec` | Retain one ID-sorted immutable inventory and expose detached exact-lookup snapshots only inside the repository. | `FR-DATABASE-PROVIDER-CATALOG-002` |
| Internal Go API | `ChannelStoreID(type, name)` | Derive deterministic Matrix or WhatsApp slash-namespaced IDs; unsupported channel types produce no ID. | `FR-DATABASE-PROVIDER-CATALOG-002` |
| Architecture gate | Internal catalog import guard | Keep the foundation unconsumed until a separately specified owner/provider composition lands. | `FR-DATABASE-PROVIDER-CATALOG-005` |

## Algorithms And Ordering

1. Require a non-nil validated configuration and revalidate that the absolute
   supplied home is the existing canonical directory already trusted by local
   IPC. Resolve config-file and optional user-home context only from `Options`.
2. Resolve the primary workspace, effective Git-workspace root, configured
   event/evolution paths, and each distinct agent workspace in their declared
   contexts.
3. Add fixed home and workspace entries, then enabled dynamic channel entries
   in stable name order. Derive only slash-separated protocol-valid IDs.
4. Resolve each candidate database and legacy path. `Project` stops at an
   absolute lexical identity; `Build` rejects unsafe existing ancestry and
   canonicalizes existing leaves without following an alias.
5. Reserve each main, WAL, SHM, and rollback-journal namespace, then reject
   duplicate, case-folded, overlapping, or same-file generation reservations
   and exact or hardlinked generation-to-legacy identities. Reject exact or
   hardlinked legacy duplicates while preserving directory containment.
6. Sort specs by logical ID, build the exact lookup index, and retain detached
   legacy-root slices.
7. A later provider repeats final path, owner, type, link, and generation checks
   around its open; it never infers authority from this earlier snapshot alone.

## Cross-Feature Behavior

`FR-DATABASE` defines the opaque `StoreID` grammar consumed here, and
`FR-DATABASE-IPC` supplies the trusted canonical-home identity. This feature
keeps physical candidates internal and does not connect the catalog to the IPC
server. The base `pkg/database` protocol/IPC package stays below this catalog in
the dependency graph and never imports it. `FR-SQLITE` remains the active
subsystem-owned persistence behavior until later provider, readiness, migration,
supervisor, and domain-adapter features explicitly replace it.

## Failure And Edge Cases

- A valid catalog may describe missing generation and legacy leaves; inventory
  construction never initializes them.
- Multiple agents sharing the exact same workspace produce one workspace store
  set; distinct paths receive distinct digest namespaces.
- Dynamic channel names normalize deterministically and retain a digest so
  punctuation changes cannot invent an unbounded or path-shaped ID.
- A disappearing existing leaf fails or is represented as missing without
  following a replacement symlink; it never authorizes a later open.
- Legacy directories may contain catalogued database files. Later legacy
  enumeration must skip those generation members instead of treating directory
  containment alone as a collision.
- An internal catalog error does not fall back to caller-supplied paths or an
  existing application store.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-PROVIDER-CATALOG-001`, `FR-DATABASE-PROVIDER-CATALOG-003` | [internal/storecatalog/catalog_test.go](../../internal/storecatalog/catalog_test.go), [internal/storecatalog/catalog_boundaries_test.go](../../internal/storecatalog/catalog_boundaries_test.go), [internal/storecatalog/catalog_security_test.go](../../internal/storecatalog/catalog_security_test.go) |
| `FR-DATABASE-PROVIDER-CATALOG-002` | [internal/storecatalog/catalog_test.go](../../internal/storecatalog/catalog_test.go), [internal/storecatalog/catalog_boundaries_test.go](../../internal/storecatalog/catalog_boundaries_test.go), [internal/storecatalog/catalog_security_test.go](../../internal/storecatalog/catalog_security_test.go) |
| `FR-DATABASE-PROVIDER-CATALOG-004` | [internal/storecatalog/catalog_test.go](../../internal/storecatalog/catalog_test.go), [internal/storecatalog/catalog_boundaries_test.go](../../internal/storecatalog/catalog_boundaries_test.go), [internal/storecatalog/catalog_security_test.go](../../internal/storecatalog/catalog_security_test.go) |
| `FR-DATABASE-PROVIDER-CATALOG-005` | [internal/storecatalog/import_guard_test.go](../../internal/storecatalog/import_guard_test.go) |

## Implementation Anchors

- [internal/storecatalog/catalog.go](../../internal/storecatalog/catalog.go)
- [internal/storecatalog/catalog_test.go](../../internal/storecatalog/catalog_test.go)
- [internal/storecatalog/catalog_boundaries_test.go](../../internal/storecatalog/catalog_boundaries_test.go)
- [internal/storecatalog/catalog_security_test.go](../../internal/storecatalog/catalog_security_test.go)
- [internal/storecatalog/import_guard_test.go](../../internal/storecatalog/import_guard_test.go)
