# Database Provider Catalog Foundation

## Feature ID

`FR-DATABASE-PROVIDER-CATALOG`

## Behavior Summary

PicoClaw defines a dormant internal catalog for the future single-owner
database provider. Given an existing canonical PicoClaw home and one validated
configuration snapshot, it deterministically inventories opaque logical store
IDs, their provider-private candidate generation paths, legacy-input roots, and
required-store policy. A provider-neutral logical facade projects only store
ID, domain, and required-store policy from that inventory. An internal catalog
can also derive an opaque deterministic fingerprint binding its complete
physical inventory to the exact configuration revision used to create it. A
dormant atomic constructor derives the logical facade and that fingerprint from
one internal projection so the two outputs cannot describe different
inventories.

This stage does not bind or open a database provider, inspect schema readiness,
claim a physical generation, publish its fingerprint through IPC, start a
broker, load configuration, or change an application persistence path. The
logical facade is the internal catalog's only production importer, and no
production package consumes the facade yet.

## Reconstruction Notes

- Similarity target: recreate a trusted internal logical-to-physical inventory
  without making filesystem locations part of the database protocol or an
  application API.
- Core types/functions: internal `Options`, `Spec`, and `Catalog` values,
  `Build`, `Project`, `Catalog.Fingerprint`, exact lookup, detached snapshots,
  deterministic dynamic channel identities, and the logical `catalog.Options`,
  `Entry`, `Catalog`, `New`, `NewSnapshot`, `Entries`, `Lookup`, `Entry`,
  `LookupChannel`, `Contains`, and `RequiredStores` facade.
- Runtime ordering: accept the already trusted canonical home, resolve every
  configured path in its declared context, generate canonical slash-separated
  store IDs, canonicalize existing leaves when building, reject catalog
  collisions, sort by ID, and either retain the internal snapshot or discard
  every physical field while constructing one immutable logical snapshot.
- Non-obvious constraints: a catalog path is a candidate provider identity, not
  authority to open it; missing generation and legacy leaves are valid; legacy
  directory roots may intentionally contain catalogued generations; a future
  provider must revalidate the final generation identity at its point of use;
  and a fingerprint is only a versioned equality token, never provider or
  catalog authority. `NewSnapshot` trusts its privileged caller to pair one
  immutable validated configuration value with its exact revision because this
  dormant layer deliberately does not load configuration.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-PROVIDER-CATALOG-001` | MUST | Privileged database infrastructure calls `Build` or `Project` with `Options`. | `Options.Home` is an existing directory whose absolute, cleaned, resolved identity is the same trusted identity accepted by `CanonicalHome`; `Options.Config` is non-nil and treated as one validated immutable snapshot. `ConfigPath` and `UserHome` supply explicit path-resolution context and are not read from ambient environment. | Catalog construction creates, chmods, opens, or removes nothing. | A missing, relative, padded, NUL-bearing, symlinked, non-directory, or canonically different home; a nil configuration; an invalid config path; or a non-absolute explicit user home fails before inventory publication. | Every later provider decision must share the IPC owner's exact home identity and configuration generation without ambient path authority. |
| `FR-DATABASE-PROVIDER-CATALOG-002` | MUST | The catalog derives fixed, workspace, agent-workspace, or enabled-channel entries. | Every ID follows the `FR-DATABASE` lowercase slash-segment contract. Fixed IDs use their declared names; additional workspaces use `workspace/<path-digest>/<store>`; Matrix and WhatsApp use `channel/<type>/<slug>-<digest>`. Entries are ID-sorted, exact-looked-up, and returned only as detached copies, including detached legacy-root slices. | Construction mutates only a new process-local snapshot. | Invalid, padded, duplicate, empty-segment, traversal-like, oversized, or nondeterministic identities fail closed; caller mutation of a returned `Spec` cannot alter retained catalog state. | Logical authority must remain stable, provider-neutral, and safe to transmit later without revealing a path. |
| `FR-DATABASE-PROVIDER-CATALOG-003` | MUST | Catalog construction resolves a candidate generation or legacy input from trusted configuration and explicit `Options` context. | Home-scoped stores resolve beneath the canonical home; primary workspace stores resolve from the configured/default primary workspace; additional workspace stores resolve from each distinct configured agent workspace; Git inventory/checkpoints resolve from the effective Git-workspace root; event, evolution, Matrix, and WhatsApp overrides resolve in their documented workspace or explicit user-home context. A relative config path is anchored to the canonical home, and its directory supplies launcher legacy context. `Project` produces absolute cleaned identities without inspecting mutable leaves, while `Build` canonicalizes every existing ancestor and leaf. | Resolution performs metadata reads only. | Surrounding whitespace, NUL bytes, ambiguous platform spellings, implicit `~` expansion, unsafe existing ancestors, symlink/reparse aliases, an existing generation that is not regular, or an existing legacy leaf that is neither a regular file nor a directory fails closed. Missing candidate generation and legacy leaves remain representable. | Relative configuration has meaning only within an explicit trusted context, and inventory must not create state while resolving it. |
| `FR-DATABASE-PROVIDER-CATALOG-004` | MUST | The complete candidate inventory is validated before publication. | Logical IDs are unique. Each database, WAL, SHM, and rollback-journal namespace is reserved together; lexical, platform case-folded, ancestor/descendant, and existing same-file or hardlink aliases across generation reservations are rejected. A generation member whose platform path key exactly equals any declared legacy root, or whose existing file is a hardlink alias of an existing legacy file, is rejected. Exact or existing same-file legacy roots across stores are rejected; ancestor/descendant legacy overlap remains allowed, and a legacy directory may contain a catalogued generation because downstream enumeration must exclude generation members explicitly. | Validation retains no filesystem handle and changes no file. | A main path aliasing another main or sidecar, overlapping generation namespaces, an irregular generation member, a duplicate legacy identity, or an exact generation-to-legacy identity aborts the whole catalog. | One future provider owner cannot safely assign two logical authorities to one physical generation or mistake a live generation for legacy input. |
| `FR-DATABASE-PROVIDER-CATALOG-005` | MUST | Repository code attempts to consume or extend this foundation. | `pkg/database/catalog/catalog.go` is the only production file permitted to import `internal/storecatalog`, and no production file may import `pkg/database/catalog`; exact-file architecture guards enforce both boundaries. A future provider must consume the complete trusted internal `Spec` and revalidate ownership, type, link count, canonical path, and generation identity immediately before and after opening rather than treating either snapshot or fingerprint as a capability. | This stage publishes no process-global catalog or IPC fingerprint and opens no provider handle. | Any other internal-catalog importer, any logical-facade consumer, direct application consumption, public path projection, provider fallback, readiness claim, IPC fingerprint publication, or use of a stale catalog path or matching digest as sufficient authority is rejected or remains unavailable. | Landing inert catalog metadata separately must not create a second database owner or weaken the protocol's opaque `StoreID` boundary. |
| `FR-DATABASE-PROVIDER-CATALOG-006` | MUST | Privileged future infrastructure constructs or queries the provider-neutral logical facade using an existing canonical home, one validated immutable configuration snapshot, and explicit path-resolution context. | Construction uses `Project`, not `Build`, and publishes an ID-sorted immutable snapshot containing exactly logical `StoreID`, domain, and required-store policy. A domain is at most 64 bytes and contains only lowercase ASCII letters, digits, and non-edge hyphens. `Entries` returns a detached slice; `Lookup` accepts only an exact canonical catalog ID; `Entry` returns a detached exact-ID record; `LookupChannel` derives a supported Matrix or WhatsApp ID and then requires that enabled entry to exist; `Contains` accepts only a valid ID in the exact snapshot. `RequiredStores` returns a detached ID-sorted slice containing exactly the IDs whose retained entries carry required-store policy; a nil catalog or catalog with no required entries returns nil. Nil receivers remain safe. | Construction retains no configuration pointer, internal `Spec`, candidate path, legacy root, or filesystem handle and changes no filesystem or application state. Query methods mutate nothing. | Nil or invalid options, empty inventory, invalid or duplicate projected ID, invalid domain, projection failure, malformed, padded, path-shaped, URI/DSN-shaped, disabled, unsupported, or unknown lookup identity fails without returning physical locations or provider diagnostics. Existing generation members and sidecars are not inspected for the logical projection. Required-store policy does not assert readiness, status completeness, initialization, availability, provider ownership, or IPC publication. | Future commands and composition need deterministic logical selection without gaining physical provider authority or reconstructing database filenames. |
| `FR-DATABASE-PROVIDER-CATALOG-007` | MUST | Trusted future composition asks one constructed internal `Catalog` to fingerprint the exact configuration revision from which that inventory was derived. | `Fingerprint` returns lowercase `sha256:` plus 64 hexadecimal digits. The digest uses a fixed version tag and unambiguous length/count framing to bind the canonical catalog home, exact configuration revision, ID-sorted complete specs, each ID/domain/candidate path/required flag, and every retained legacy root in its declared order. The accepted revision is exactly `missing` or lowercase `sha256:` plus 64 hexadecimal digits; input is never trimmed. Before hashing, the detached inventory must satisfy the same lexical ID, platform-path, generation-namespace, and legacy-alias rules as `Project`. | Fingerprinting clones and sorts detached specs, mutates neither retained catalog nor caller state, performs no filesystem or configuration read, and retains no digest state. | A nil or empty catalog; invalid revision, home, store ID, domain, candidate path, or legacy path; duplicate ID or legacy identity; generation overlap; or exact generation-to-legacy alias returns one generic error without echoing input. A matching fingerprint grants no catalog, path, provider, readiness, migration, transport, or application authority. | A later broker generation needs one collision-resistant equality token identifying which complete catalog inventory belongs to which atomic configuration revision without publishing physical fields. |
| `FR-DATABASE-PROVIDER-CATALOG-008` | MUST | Trusted future composition supplies `NewSnapshot` the same explicit `Options` accepted by `New` plus the exact revision paired with that already validated immutable `Options.Config`. | One and only one `Project` result is used first to derive its internal `Fingerprint` and then to construct the logical `Catalog`; success returns both, and failure returns neither. The logical output uses the same conversion as `New`, while the fingerprint binds that exact projection's complete provider-private inventory under `FR-DATABASE-PROVIDER-CATALOG-007`. | Construction changes no filesystem, configuration, provider, transport, readiness, or application state. The returned object retains only logical catalog metadata, while the fingerprint is returned separately; neither retains a configuration pointer, internal catalog, spec, path, or legacy root. | The caller is responsible for pairing the exact revision with `Options.Config`; this primitive cannot prove that relationship. Invalid projection, revision, fingerprint input, or logical projection returns a nil catalog and empty fingerprint through one bounded provider-neutral error without exposing physical fields. No partial output is usable. | Future owner composition needs an indivisible logical-catalog/fingerprint pair without independently rebuilding an inventory or making its physical paths part of a public API. |

## Data And State Model

`Options` supplies the canonical home, validated config snapshot, config-file
path, and optional explicit user-home expansion root. None is discovered from
process environment. `Catalog` is one immutable process-local snapshot. Its
home, ordered specs, and lookup map are private; `Home`, `All`, and `Lookup`
return scalar values or detached copies. Each internal `Spec` contains a
logical ID, domain identifier, candidate
database path, zero or more legacy roots, and required-store policy. Neither
the protocol nor any application package receives a `Spec`.

The provider-neutral facade accepts equivalent explicit construction context
but retains only a private ordered set and exact-ID index of logical `Entry`
values. Each entry contains only `StoreID`, a bounded lowercase ASCII domain,
and required-store policy.
Required is catalog policy, not a computed readiness state. The facade retains
no internal catalog, configuration, database-generation identity, legacy root,
or provider handle, and its returned entry and required-ID slices are detached.
Filtering required IDs reads only the immutable sorted logical entries; it does
not validate or derive a `StoreStatus`, inspect a provider, or change policy.

An internal catalog fingerprint is a derived value only. Its SHA-256 preimage
is domain-separated and length-framed; it includes the canonical home, exact
configuration revision, and every provider-private field in the complete
inventory. Paths and legacy roots influence equality but never appear in the
returned token or its generic validation error. The catalog retains no
fingerprint cache, and callers cannot use a matching token to obtain a `Spec`.

`NewSnapshot` returns the logical `Catalog` and fingerprint as one all-or-none
construction result. The pair has no retained wrapper or additional mutable
state. Its transient internal catalog exists only long enough to compute both
outputs from the same detached spec set; no provider-private value is copied
into the logical catalog. The supplied configuration revision is a trusted
association asserted by the caller, not a value loaded or verified against a
configuration file by this feature.

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
Owns: CODE internal/storecatalog/fingerprint.go
Owns: CODE internal/storecatalog/path_*.go
Owns: TEST internal/storecatalog/*_test.go *
Owns: TEST internal/storecatalog/fingerprint_test.go *
Owns: CODE pkg/database/catalog/catalog.go
Owns: CODE pkg/database/catalog/snapshot.go
Owns: TEST pkg/database/catalog/catalog_test.go *
Owns: TEST pkg/database/public_api_test.go *
Owns: TEST pkg/database/catalog/snapshot_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `Build(options)` | Canonicalize existing catalog leaves and reject logical, generation, and exact legacy-file collisions without opening a provider. | `FR-DATABASE-PROVIDER-CATALOG-001`, `FR-DATABASE-PROVIDER-CATALOG-003`, `FR-DATABASE-PROVIDER-CATALOG-004` |
| Internal Go API | `Project(options)` | Produce the same logical inventory and absolute candidate paths without inspecting mutable generation or legacy leaves. | `FR-DATABASE-PROVIDER-CATALOG-001`, `FR-DATABASE-PROVIDER-CATALOG-002`, `FR-DATABASE-PROVIDER-CATALOG-003` |
| Internal Go value | `Catalog`, `Spec` | Retain one ID-sorted immutable inventory and expose detached exact-lookup snapshots only inside the repository. | `FR-DATABASE-PROVIDER-CATALOG-002` |
| Internal Go API | `ChannelStoreID(type, name)` | Derive deterministic Matrix or WhatsApp slash-namespaced IDs; unsupported channel types produce no ID. | `FR-DATABASE-PROVIDER-CATALOG-002` |
| Internal Go API | `Catalog.Fingerprint(configRevision)` | Derive a versioned opaque equality binding for the complete physical inventory and exact configuration revision without reading or publishing either source. | `FR-DATABASE-PROVIDER-CATALOG-007` |
| Provider-neutral Go value | `catalog.Entry`, `catalog.Catalog` | Retain only detached logical ID, domain, and required-store policy without physical provider fields or readiness. | `FR-DATABASE-PROVIDER-CATALOG-006` |
| Provider-neutral Go API | `catalog.New`, `Entries`, `Lookup`, `Entry`, `LookupChannel`, `Contains`, `RequiredStores` | Project and query exact logical membership and required-store policy without inspecting generation members, exposing internal specs, or deriving readiness. | `FR-DATABASE-PROVIDER-CATALOG-006` |
| Provider-neutral Go API | `catalog.NewSnapshot(options, configRevision)` | Atomically derive one logical catalog and its opaque complete-inventory fingerprint from one internal projection supplied with a trusted revision/config pairing. | `FR-DATABASE-PROVIDER-CATALOG-008` |
| Architecture gates | Internal-catalog and logical-facade import guards | Permit the exact facade implementation to consume the internal inventory while keeping the facade itself unconsumed until a separately specified owner composition lands. | `FR-DATABASE-PROVIDER-CATALOG-005` |

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
7. When fingerprinting, validate the exact configuration revision, canonical
   home, absolute platform-safe paths, domains, IDs, lexical generation
   reservations, and legacy identities against a detached complete spec set.
   Sort the clone by logical ID, then feed the version tag, canonical home,
   revision, spec count, every scalar spec field, each required byte, and every
   ordered legacy root through unsigned 64-bit big-endian length/count frames
   into SHA-256.
8. When constructing a paired snapshot, call `Project` exactly once, fingerprint
   that result with the caller-supplied revision, construct the logical facade
   from the same result's detached specs, and publish both outputs only after
   every step succeeds.
9. When constructing the logical facade, copy only ID, domain, and
   required-store policy into its own sorted exact-ID index, then discard every
   internal spec and physical field. Resolve lookups by exact membership; never
   trim, normalize, or interpret caller input as a path or DSN. To project
   required policy, scan those already sorted entries and copy only IDs whose
   required flag is true; do not consult configuration, status, or provider
   state.
10. A later provider repeats final path, owner, type, link, and generation checks
   around its open; it never infers authority from this earlier snapshot alone.

## Cross-Feature Behavior

`FR-DATABASE` defines the opaque `StoreID` grammar consumed here, and
`FR-DATABASE-IPC` supplies the trusted canonical-home identity. This feature
keeps physical candidates internal and exposes only its dormant logical facade;
neither catalog connects to the IPC server. The base `pkg/database`
protocol/IPC package stays below both catalogs in the dependency graph and
never imports its `catalog` subpackage, avoiding the cycle through
`internal/storecatalog`. The internal fingerprint uses the format accepted by
`FR-DATABASE-IPC`, but this stage neither supplies it to `StartServer` nor
publishes it in broker status. `NewSnapshot` does not call any configuration
loader; later trusted owner composition must obtain one atomic current
configuration/revision pair and pass both without mutation. It also does not
derive readiness or required-status claims from the returned logical policy.
`RequiredStores` is not the `BrokerStatus.RequiredStores` publication step and
does not prove that any matching status exists; later trusted composition must
pair policy, readiness, and fingerprint without changing this dormant query.
`FR-DATABASE-SQLITE-CONTROL` remains separate and unconsumed. `FR-SQLITE`
remains the active subsystem-owned persistence behavior until later provider,
readiness, migration, supervisor, and domain-adapter features explicitly
replace it.

## Failure And Edge Cases

- A valid catalog may describe missing generation and legacy leaves; inventory
  construction never initializes them.
- Multiple agents sharing the exact same workspace produce one workspace store
  set; distinct paths receive distinct digest namespaces.
- Dynamic channel names normalize deterministically and retain a digest so
  punctuation changes cannot invent an unbounded or path-shaped ID.
- Logical lookup never trims or repairs input; only an exact present canonical
  ID is authority, and channel derivation alone does not prove membership.
- Required-store projection is nil-safe, preserves stable ID ordering, and
  cannot turn policy into readiness, initialize a missing store, validate a
  status set, or publish broker metadata.
- The logical facade does not retain or return internal physical specs, and a
  projection failure does not expose a candidate path through its result.
- Fingerprints are stable across retained spec order but deliberately change
  with the configuration revision, catalog home, any scalar spec field, the
  required-store policy, legacy-root value, or legacy-root order.
- Invalid fingerprint input yields no partial digest and does not echo a
  revision, domain, store ID, or path.
- Fingerprinting repeats lexical catalog collision validation without inspecting
  whether any generation or legacy path currently exists.
- Snapshot construction never returns one successful output when the other
  fails, never runs `Project` separately for its logical and fingerprint views,
  and never retains the transient physical inventory.
- A syntactically valid revision supplied for a different configuration is
  trusted caller misuse; this layer does not load a file or claim to detect it.
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
| `FR-DATABASE-PROVIDER-CATALOG-005` | [pkg/database/architecture_test.go](../../pkg/database/architecture_test.go), [pkg/database/public_api_test.go](../../pkg/database/public_api_test.go) |
| `FR-DATABASE-PROVIDER-CATALOG-006` | [pkg/database/catalog/catalog_test.go](../../pkg/database/catalog/catalog_test.go), [pkg/database/public_api_test.go](../../pkg/database/public_api_test.go) |
| `FR-DATABASE-PROVIDER-CATALOG-007` | [internal/storecatalog/fingerprint_test.go](../../internal/storecatalog/fingerprint_test.go), [pkg/database/architecture_test.go](../../pkg/database/architecture_test.go) |
| `FR-DATABASE-PROVIDER-CATALOG-008` | [pkg/database/catalog/snapshot_test.go](../../pkg/database/catalog/snapshot_test.go), [pkg/database/public_api_test.go](../../pkg/database/public_api_test.go) |

## Implementation Anchors

- [internal/storecatalog/catalog.go](../../internal/storecatalog/catalog.go)
- [internal/storecatalog/catalog_test.go](../../internal/storecatalog/catalog_test.go)
- [internal/storecatalog/catalog_boundaries_test.go](../../internal/storecatalog/catalog_boundaries_test.go)
- [internal/storecatalog/catalog_security_test.go](../../internal/storecatalog/catalog_security_test.go)
- [internal/storecatalog/fingerprint.go](../../internal/storecatalog/fingerprint.go)
- [internal/storecatalog/fingerprint_test.go](../../internal/storecatalog/fingerprint_test.go)
- [pkg/database/architecture_test.go](../../pkg/database/architecture_test.go)
- [pkg/database/catalog/catalog.go](../../pkg/database/catalog/catalog.go)
- [pkg/database/catalog/catalog_test.go](../../pkg/database/catalog/catalog_test.go)
- [pkg/database/public_api_test.go](../../pkg/database/public_api_test.go)
- [pkg/database/catalog/snapshot.go](../../pkg/database/catalog/snapshot.go)
- [pkg/database/catalog/snapshot_test.go](../../pkg/database/catalog/snapshot_test.go)
