# Database-Agnostic Single-Owner Layer

## Feature ID

`FR-DATABASE`

## Behavior Summary

PicoClaw exposes mutable state through typed domain clients backed by one local
database broker for each canonical `PICOCLAW_HOME`. The broker is owned by a
gateway supervisor, resolves opaque logical store IDs through a trusted catalog,
and is the only live process allowed to own physical database generations.

The restartable gateway runtime, launcher, and CLI never open database files.
They communicate with the broker over authenticated owner-only local IPC. SQLite
is the sole shipped provider, but paths, SQL, driver behavior, migrations, and
durability details remain behind provider-neutral domain contracts. Schema
upgrades and legacy imports run only through the fenced offline database
migrator.

## Reconstruction Notes

- Similarity target: recreate one canonical-home supervisor and broker, typed
  domain clients, a trusted logical-store catalog, provider-neutral errors, and
  an offline migration workflow without exposing physical storage to callers.
- Core types/functions: supervisor discovery/ensure/shutdown, broker server and
  client, `StoreID`, catalog registration and readiness, typed request/response
  envelopes, provider interface, structured error codes, migration planner and
  executor, and the private Matrix/WhatsApp SQL compatibility driver.
- Runtime ordering: canonicalize the home, fence duplicate supervisors, publish
  authenticated discovery, start the broker, load and validate the store
  catalog, establish readiness for every required store, then initialize
  launcher authentication or start the hidden gateway-runtime child.
- Non-obvious constraints: runtime restart does not restart the broker; physical
  aliases fail closed; normal startup may initialize only a missing empty store;
  a mutation is not replayed unless its domain operation declares stable
  idempotency; uncertain commit returns `OutcomeUnknown`; migration snapshots a
  complete database generation before recovery; and the Matrix/WhatsApp raw SQL
  bridge is temporary, private, and never accepts a filesystem path.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-001` | MUST | A launcher, CLI, gateway supervisor, gateway runtime, migrator, or another process addresses mutable state beneath one canonical `PICOCLAW_HOME`. | Exactly one active handler owns every operation that can open, inspect, modify, checkpoint, migrate, back up, recover, or remove a database generation. | The supervisor fences one broker epoch; an offline migrator may replace it only after exclusive shutdown fencing. | A second live owner, stale discovery record, alias home, or unfenced migrator fails closed without touching a generation. | Multiple handlers against one live SQLite generation can corrupt ownership, sidecars, and recovery semantics. |
| `FR-DATABASE-002` | MUST | Application code requests persistence behavior. | It receives typed domain operations and provider-neutral models/errors only. | Domain commands mutate only their declared logical store through the broker. | Public or application-facing APIs containing SQL, driver names, paths, filenames, PRAGMAs, WAL concepts, `database/sql` types, or backend errors are rejected by the architecture gate. | Callers must remain independent of the shipped provider and unable to bypass domain invariants. |
| `FR-DATABASE-003` | MUST | A caller selects durable state. | The caller supplies an opaque allow-listed logical `StoreID`, which the trusted broker catalog resolves. | Dynamic stores are derived only from broker-loaded validated configuration. | Caller-supplied paths, DSNs, unknown IDs, catalog traversal, or physical-location overrides return `Invalid` or `Unauthorized` before provider access. | Store identity is authority and cannot be delegated as a filesystem string. |
| `FR-DATABASE-004` | MUST | Fixed or dynamic catalog entries are registered or opened. | One stable connection pool exists for each canonical physical store for the broker epoch. | The pool remains live until broker shutdown, including across gateway-runtime restart. | Duplicate registration, symlink/case/hard-link/ancestor aliases, or distinct IDs resolving to one physical store fail closed before either alias becomes usable. | A single physical store must have one connection and lifecycle authority. |
| `FR-DATABASE-005` | MUST | Ordinary launcher, supervisor, runtime, or CLI startup encounters a missing, current, outdated, legacy, too-new, or damaged store. | A missing empty store may be initialized at the current schema; a current store becomes ready; an outdated or legacy store returns `MigrationRequired`. | Ordinary startup performs no schema upgrade, legacy import, destructive recovery, or archive transition. | Too-new, corrupt, failed-integrity, or unavailable generations return structured readiness/errors and never fall back to legacy files. | Upgrades need an exclusive, backed-up, operator-visible boundary. |
| `FR-DATABASE-006` | MUST | A provider, transport, catalog, or domain operation fails. | The API returns one structured backend-neutral code from `Unavailable`, `MigrationRequired`, `Conflict`, `NotFound`, `AlreadyExists`, `Deadline`, `Integrity`, `Invalid`, `Unauthorized`, `Unsupported`, `OutcomeUnknown`, or `Internal`, plus bounded safe metadata. | No provider error or private diagnostic is persisted or transmitted as a domain error. | Unknown or unsafe backend failures map to `Internal`; an uncertain mutation commit maps to `OutcomeUnknown`. | Stable error semantics are required for safe UI, CLI, retry, and maintenance behavior. |
| `FR-DATABASE-007` | MUST | IPC disconnect, deadline, broker restart, or stale epoch interrupts an operation. | Reads may reconnect and retry; a mutation retries only when its domain declaration has stable idempotency and the same request identity. | Idempotent replay returns the original durable outcome without repeating the effect. | A disconnect after a mutation may have committed returns `OutcomeUnknown`; deadline/cancellation cannot be reported as a definite rollback without proof. | Automatic retry must not duplicate effects or lie about commit outcome. |
| `FR-DATABASE-008` | MUST | One requested behavior would atomically modify more than one logical store. | The API rejects the cross-store transaction as `Unsupported`, or exposes the related atomic behavior as one broker-side domain command against one store. | No distributed or caller-coordinated transaction state is created. | Partial multi-store mutation is never presented as atomic. | The database layer must have an explicit, testable transaction boundary. |
| `FR-DATABASE-009` | MUST | A physical store needs durability configuration, security validation, schema work, backup, recovery, or diagnostics. | The selected provider performs and verifies that work behind the broker contract. | Provider-owned state includes connections, physical generations, schema/import ledgers, backups, recovery checkpoints, and safe diagnostics. | Callers cannot change durability or inspect, replace, checkpoint, or remove provider artifacts directly. | Backend-specific lifecycle and recovery knowledge belongs to one trusted owner. |

## Data And State Model

The supervisor has one epoch for one canonical home. Its discovery manifest
contains the supervisor PID, protocol version, a random 256-bit authentication
token, local endpoint, and broker epoch. The manifest, Unix socket on Unix, or
current-user named pipe on Windows is owner-only. TCP discovery and listening
are unsupported. The token is inherited only by trusted children or read from
owner-only discovery by an attaching local client.

Protocol v1 is a length-prefixed canonical-JSON stream with a 128 MiB hard
frame limit. Every request contains protocol version, domain, domain version,
operation, request ID, deadline, broker epoch, and typed payload. Every response
echoes request identity and epoch and contains either a typed payload or the
structured error envelope. List operations use bounded cursor pagination well
below the transport limit. Unknown fields, duplicate keys, noncanonical values,
oversized lengths, expired deadlines, wrong tokens, and unsupported versions
fail before domain dispatch.

Broker readiness is tracked per logical store as `ready`,
`migration_required`, `integrity_failed`, or `unavailable`. Required enabled
stores must all be `ready` before the runtime child starts. The catalog owns
fixed stores for global auth, launcher auth, model catalogs, and tool adaptation;
workspace stores for workflows, sessions/threads, eventing, cron, runtime
state, account routing, review/evaluation, evolution, local-CI cache, and
Seahorse; and channel stores for WeCom, Weixin, Matrix, and WhatsApp. Dynamic
entries are computed from broker-loaded validated configuration and retain both
opaque logical identity and provider-private canonical physical identity.
Provider-private claims for every database/WAL/SHM/rollback-journal namespace are keyed by that
physical identity outside any single home, so distinct homes cannot
concurrently own the same external configured store.

The gateway supervisor owns the broker and a restartable hidden gateway-runtime
child. The child receives an authenticated endpoint through inherited
environment and rejects direct invocation. Stopping or restarting that child
does not close broker pools. Launcher shutdown also leaves the supervisor
available; `picoclaw database shutdown` is the explicit controlled shutdown
surface. A launcher that owns a supervisor monitors it and respawns it with
bounded backoff. A CLI attachment retries discovery once. Supervisor loss
terminates its runtime child, and a replacement supervisor always publishes a
new epoch.

The migration lock and backup manifest are provider-owned offline state. Each
backup lives beneath `backups/database-migrate-<UTC>/` and records trusted store
ID, generation members, legacy inputs, SHA-256 hashes, sizes, modes, timestamps,
and migration result. A backup includes the matching database/WAL/SHM/rollback-journal generation
as observed through SQLite recovery rules; no workflow manually deletes live
sidecars. Imported legacy inputs are archived only after their schema and import
ledger commit atomically.

Matrix and WhatsApp may temporarily use a private RPC-backed
`database/sql/driver`. Its DSN carries only an allow-listed logical store ID.
Runtime mode rejects DDL, mutating PRAGMAs, `ATTACH`, `DETACH`, and `VACUUM`;
offline migration mode permits only the library schema work required for those
two domains. Seahorse uses the normal typed domain client. The bridge is tracked
for removal by
[`[Task] Remove Matrix/WhatsApp raw SQL broker bridge`](https://github.com/dkropachev/picoclaw/issues/303).

## Surface Ownership

Owns: CODE pkg/database/**
Owns: CODE cmd/picoclaw/internal/database/**
Owns: CLI cmd/picoclaw/internal/database/*
Owns: TEST pkg/database/*
Owns: TEST cmd/picoclaw/internal/database/*

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Process | Gateway supervisor and hidden gateway-runtime child | Supervisor starts the broker before config/provider/channel initialization, starts the authenticated hidden runtime only after required-store readiness, preserves broker epoch/pools across runtime restart, and terminates the runtime if supervisor ownership is lost. | `FR-DATABASE-001`, `FR-DATABASE-004`, `FR-DATABASE-005` |
| IPC | Owner-only Unix socket or current-user Windows named pipe | Authenticated protocol-v1 canonical-JSON frames, 128 MiB maximum, request/deadline/epoch fencing, bounded pagination, cancellation, and structured errors; never TCP. | `FR-DATABASE-001`, `FR-DATABASE-006`, `FR-DATABASE-007` |
| Go API | Typed domain clients and `StoreID` | Preserve domain constructors and operations where practical while exposing no raw handles, SQL callbacks, provider paths, or backend errors. | `FR-DATABASE-002`, `FR-DATABASE-003`, `FR-DATABASE-006`, `FR-DATABASE-008` |
| Catalog | Broker fixed/dynamic store registry and provider artifact projection | Resolve allow-listed IDs from trusted configuration, reject physical aliases/collisions, report readiness, and provide protected artifacts to agent mutation policy without caller path reconstruction. | `FR-DATABASE-003`, `FR-DATABASE-004`, `FR-DATABASE-005`, `FR-DATABASE-009` |
| CLI | `picoclaw database status` | Attach to or ensure the supervisor and report broker epoch plus per-store readiness without opening a physical store. | `FR-DATABASE-001`, `FR-DATABASE-005`, `FR-DATABASE-006` |
| CLI | `picoclaw database migrate [--store ID...] [--backup-dir DIR] [--dry-run]` | Select only trusted catalog IDs, require exclusive fencing, plan or perform mandatory-backed-up offline migration, and retain the backup on success or failure. `--backup-dir` chooses a backup parent, never a database input. | `FR-DATABASE-001`, `FR-DATABASE-003`, `FR-DATABASE-005`, `FR-DATABASE-009` |
| CLI | `picoclaw database shutdown` | Authenticate to the canonical-home supervisor, stop its runtime child, drain broker work, checkpoint/close provider pools cleanly, and remove discovery only after ownership ends. | `FR-DATABASE-001`, `FR-DATABASE-004`, `FR-DATABASE-009` |
| Compatibility API | Private Matrix/WhatsApp RPC SQL driver | Accept only allow-listed logical IDs; reject runtime DDL and provider-control statements; permit bounded library upgrades only in fenced offline migration mode. | `FR-DATABASE-002`, `FR-DATABASE-003`, `FR-DATABASE-005`, `FR-DATABASE-009` |

## Algorithms And Ordering

### Supervisor and runtime startup

1. Resolve and canonicalize `PICOCLAW_HOME`, acquire the singleton ownership
   fence, and validate any discovery manifest and endpoint without following an
   untrusted alias.
2. Attach to a valid live broker or start a supervisor, generate its token and
   epoch, bind the owner-only local endpoint, and durably publish discovery.
3. Start the broker before launcher dashboard authentication or any gateway
   config, provider, agent, workflow, event, cron, or channel initialization.
4. Load validated configuration in the trusted supervisor, derive fixed and
   dynamic catalog entries, resolve provider-private canonical identities, and
   reject every duplicate or alias before opening a pool.
5. For each required enabled store, initialize a missing empty current store or
   validate the current generation. Report outdated/legacy state as
   `migration_required`, integrity failure as `integrity_failed`, and other
   inability as `unavailable`; do not run upgrades.
6. Start the hidden runtime child only after all required stores are `ready`.
   Pass authenticated broker discovery through inherited environment and reject
   a directly invoked runtime entry point.
7. On runtime restart, retain the same broker epoch and pools. On supervisor
   restart, terminate the old runtime, fence stale clients, and use a new epoch.

### Request and retry handling

1. Authenticate and validate frame length, canonical JSON, protocol/domain
   versions, epoch, request ID, and deadline before decoding the domain payload.
2. Resolve only the operation's declared logical store and dispatch one typed
   domain adapter command. The adapter owns validation, SQL codec, schema, and
   transaction behavior on the broker side.
3. Cancel work at its deadline when the provider can prove cancellation. Return
   the structured domain result or map the failure to the closed error set.
4. A client may rediscover once after unavailable or stale-epoch reads. Replay a
   mutation only with the exact stable request identity and an operation marked
   idempotent. Otherwise return `OutcomeUnknown` when commit cannot be proved.

### Offline migration

1. Refuse while a launcher, supervisor, runtime, or another migrator holds the
   storage root; shut down the supervisor explicitly and acquire one exclusive
   per-home migration lock.
2. Load validated configuration and enumerate only the trusted store catalog.
   Validate requested `--store` values as logical IDs and reject paths or DSNs.
3. Before changing an affected store, create and fsync its timestamped backup
   directory and manifest, including the matching physical generation and
   affected legacy inputs with hashes, sizes, and modes. Abort on snapshot or
   fsync failure and retain all backup material.
4. Let SQLite recover or roll back the existing generation; never delete a live
   WAL or SHM file manually. Run integrity and foreign-key checks before schema
   work.
5. Switch the migration connection to exclusive locking and rollback journal,
   then apply the complete schema and legacy import in one all-or-nothing
   transaction so large migrations do not create huge WAL/SHM mappings.
   Matrix and WhatsApp library upgraders that own internal transaction
   boundaries run against a same-directory SQLite-backup stage; only the fully
   closed, versioned, integrity-checked stage is atomically installed.
6. Commit schema version and import ledger atomically. Archive exact validated
   legacy inputs only after that commit and never overwrite an archive.
7. Switch the completed store to WAL, checkpoint through SQLite, close cleanly,
   reopen through the provider, and repeat schema, integrity, foreign-key,
   permission, and catalog-identity checks.
8. Preserve the backup and manifest on dry run, success, or failure, release
   migration ownership only after all durable cleanup, and allow a subsequent
   supervisor to publish a fresh epoch.

For the known workflow recovery case, migration detects an approximately 4 KiB
`workflows.db` with its matching hot sidecars, `user_version=0`, no committed
schema, and retained `workflow_runs`. It snapshots the whole generation first,
lets SQLite recover the incomplete transaction, imports the legacy workflow
files, and verifies run/event counts plus known review run IDs before archiving
the exact inputs. The original snapshot remains recoverable and the current WAL
is never removed manually.

Repository-review Start, Continue, and Resume synchronously preflight the
workflow and review logical stores before persisting `running` or returning HTTP
202. `MigrationRequired` returns a maintenance response without changing
automation state.

## Cross-Feature Behavior

Launcher management ensures the supervisor before dashboard authentication and
uses typed auth/catalog clients. Chat channels and gateway services run only in
the hidden runtime and use typed clients, except for the temporary Matrix and
WhatsApp bridge. Session memory, threads, workflows, event automation,
scheduling, account routing, evolution, reviews, evaluations, local CI, and
Seahorse retain domain models and validation while their SQL codecs, migrations,
and transactions move into broker-side adapters.

SQLite Runtime Storage defines the shipped provider. Security Isolation owns
credential semantics and consumes the provider artifact catalog for
model-facing mutation protection. Agent Conversations consumes the same catalog
instead of reconstructing filenames. Portability must provide an owner-only
local transport or mark the target unsupported; no target falls back to mutable
legacy state.

## Failure And Edge Cases

- Discovery with the wrong owner, mode/ACL, token, PID, endpoint kind, protocol,
  or epoch is unusable and never downgraded to TCP or unauthenticated local IPC.
- Canonically equivalent homes converge on one supervisor. A physical store
  reached through two IDs, symlinks, hard links, case aliases, or validated
  dynamic configuration collisions fails before either registration is ready.
- A missing empty store may be initialized. Any legacy source or existing older
  schema makes ordinary startup return `MigrationRequired` without mutation.
- Runtime stop, crash, and restart retain broker epoch and pools. Supervisor
  loss invalidates the epoch, terminates the runtime child, and makes clients
  rediscover rather than reuse stale connections.
- Frame length is checked before allocation. Large collections paginate;
  cancellation and deadline do not imply a mutation rollback unless the broker
  can prove it.
- Cross-store atomic requests, arbitrary paths/DSNs, runtime DDL through the
  bridge, unsupported provider controls, and raw SQL domain operations fail
  closed.
- Migration never starts without exclusive fencing and a complete durable
  backup. Crash, integrity failure, archive conflict, too-new schema, corrupt
  generation, or failed reopen retains the backup and does not advertise the
  store as ready.
- Replaying an archived legacy input is detected from the atomic import ledger;
  changed or ambiguous inputs are not guessed, re-imported, or removed.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-001`, `FR-DATABASE-004` | `pkg/database/supervisor_test.go`, `pkg/database/server_unix_test.go` |
| `FR-DATABASE-002`, `FR-DATABASE-003`, `FR-DATABASE-008` | `pkg/database/architecture_test.go`, `pkg/database/public_api_test.go`, `pkg/database/catalog/catalog_test.go` |
| `FR-DATABASE-005`, `FR-DATABASE-009` | `pkg/database/migration/migration_test.go`, `cmd/picoclaw/internal/database/command_test.go` |
| `FR-DATABASE-006`, `FR-DATABASE-007` | `pkg/database/protocol_test.go`, `pkg/database/server_unix_test.go` |
| `FR-DATABASE-001` through `FR-DATABASE-009` | `pkg/database/server_unix_test.go`, `pkg/database/migration/migration_test.go`, `cmd/picoclaw/internal/database/command_test.go` |

Acceptance covers the static architecture/public-API gates; canonical-home
singleton behavior across launcher, runtime, and CLI processes; multi-client
workflow/session/event/auth/review/channel stress; broker epoch preservation and
replacement; authenticated IPC ACLs, bounds, cancellation, stale epochs,
idempotent replay, and uncertain outcomes; mandatory-backup migration fencing,
crash rollback, hot-WAL recovery, too-new and corrupt generations, archive
replay, and the retained large workflow fixture; Matrix/WhatsApp bridge
conformance; and end-to-end launcher-before-auth, hidden-runtime, review
Continue, and runtime-stop behavior.

## Implementation Anchors

- `pkg/database/server.go`
- `pkg/database/client.go`
- `pkg/database/protocol.go`
- `pkg/database/supervisor_process.go`
- `pkg/database/catalog/catalog.go`
- `pkg/database/physical_claims.go`
- `pkg/database/migration/migration.go`
- `pkg/database/architecture_test.go`
- `cmd/picoclaw/internal/database/command.go`
- [SQLite Runtime Storage](sqlite-storage.md)
