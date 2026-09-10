# Database Local IPC

## Feature ID

`FR-DATABASE-IPC`

## Behavior Summary

PicoClaw provides a dormant, provider-neutral local client/server transport for
the database protocol. One process can own a canonical home through an
authenticated Unix-domain socket or current-user Windows named pipe, publish a
private discovery manifest, optionally compose typed protocol domains through
a domain-handler registry, and drain safely at shutdown. This stage does not
configure or supervise that process, open a
physical database provider, publish a catalog, run migration, expose a command,
or change any application persistence path.

## Reconstruction Notes

- Similarity target: recreate secure same-user IPC and broker lifecycle without
  introducing TCP, caller-selected endpoints, provider handles, or file paths
  into application APIs.
- Core types/functions: `Manifest`, `Client`, `Server`, `StartServer`,
  `Connect`, `ConnectInherited`, `HandlerRegistry`, canonical-home helpers,
  local transport implementations, storage fences, runtime-client publication,
  and owner-only file helpers.
- Runtime ordering: canonicalize and secure the home, acquire singleton and
  online fences, prepare the manifest candidate, run the optional trusted
  startup guard, remove only a validated stale endpoint, listen, publish
  discovery, validate/authenticate requests, dispatch, drain, remove epoch-bound
  discovery, and release locks.
- Non-obvious constraints: Unix socket names are derived into a short private
  runtime directory; Windows uses an owner-restricted named pipe and DACL;
  unsupported secure transports fail closed.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-IPC-001` | MUST | IPC code prepares or resolves a PicoClaw home. | The result is one absolute canonical real directory with an owner-only private state directory. | Missing homes may be created with private permissions; aliases and unsafe boundaries are never rewritten into trust. | Empty, padded, NUL-containing, symlinked, non-directory, foreign-owned, or writable-by-an-untrusted-principal boundaries fail closed; read-only public access does not grant mutation authority. | Discovery and locks must bind one filesystem identity. |
| `FR-DATABASE-IPC-002` | MUST | A server publishes or a client reads broker discovery. | A private canonical manifest binds PID, protocol, random token, derived endpoint, and broker epoch to the canonical home. | Publication is temporary-file, sync, rename, and directory-sync ordered; a failed final sync attempts manifest removal and a second directory sync before returning the joined failure; removal requires the expected epoch and owner PID. | Missing discovery is `Unavailable`; symlinks, wrong modes, invalid identities, oversized content, and changed epochs fail without exposing paths or tokens. | A client must authenticate only the current same-home broker generation. |
| `FR-DATABASE-IPC-003` | MUST | A supported platform starts or dials local transport. | Unix uses an owner-only Unix-domain socket; Windows uses a current-user named pipe with remote clients rejected; endpoint names are derived, not caller supplied. | Listen creates only the private endpoint boundary and cleanup removes only the validated socket/pipe generation. | Unsafe pre-existing endpoints, insecure ownership, overlong Unix home paths, unavailable transport, and unsupported operating systems fail closed; TCP is never enabled. | Local ownership must not broaden network or cross-user authority. |
| `FR-DATABASE-IPC-004` | MUST | `StartServer` begins, serves, closes, receives shutdown, or is configured with `ServerOptions.StartupGuard`. | Exactly one server owns the canonical home, retains its epoch and online fence while serving, validates protocol/token/epoch/deadline before dispatch, and reports detached readiness through callbacks. The optional trusted startup guard runs exactly once after temporary-manifest preparation but before endpoint retirement, listener creation, or discovery publication. | A successful startup guard permits endpoint preparation, listener creation, manifest rename, and serving. Shutdown stops admission, drains workers, invokes configured trusted callbacks, removes matching discovery/endpoint state, and releases the online and singleton fences in order. | Duplicate owners conflict; migration fencing blocks startup; a startup-guard error is preserved and a startup-guard panic becomes generic `Internal`; either failure leaves the candidate epoch unpublished, creates no current-generation listener, preserves any preexisting canonical manifest and endpoint, and releases the online fence and singleton. Canceled close returns a deadline error while draining continues in the background. Callback completion itself is not time-bounded. | No process may discover an unapproved generation or replace or mutate storage while an admitted broker request or trusted lifecycle callback remains active. |
| `FR-DATABASE-IPC-005` | MUST | A client connects or invokes a read or mutation. | The client uses only the discovered endpoint/token/epoch, canonical framed requests, typed results, and structured failures; reads may rediscover once after broker replacement. | `InstallProcessClient` publishes only an in-process client pointer, and inherited authority is consumed once from a bounded canonical environment value. | Local validation fails before dialing; mutation disconnect becomes `OutcomeUnknown`; noncanonical, mismatched, stale-epoch, or invalid responses fail closed; no provider fallback occurs. | Callers must not infer whether a disconnected mutation committed or bypass the broker. |
| `FR-DATABASE-IPC-006` | MUST | Online or migration code acquires a storage-root fence. | Multiple online shared fences may coexist, while a migration fence is exclusive and nonblocking. A live fence retains and rechecks the exact home, private state-directory, and lock-file identities. `Guard` holds that authority across a short transfer; `GuardMigration` additionally requires the exclusive capability. Checked variants return a non-reentrant boundary checker that remains safe only until its idempotent release, detects drift while guarded, and reports false after release. An exclusive fence may derive a context capability for one exact provider target, and capability checks fail after fence close or for another target. | Close waits for live guards, invalidates derived authority, and releases the OS lock exactly once; checked-guard release is idempotent and invalidates its checker before unlocking; lock files remain private and may persist. | Symlinked, foreign, non-regular, publicly accessible, physically replaced, or contended lock boundaries return structured integrity/conflict errors. A nil, closed, wrong-home, non-migration, malformed-target, memory, URI-shaped, or wrong-target authority fails closed. | Online serving and offline replacement must be mutually exclusive across processes, while guarded transitions must revalidate without recursively acquiring a lock behind queued shutdown. |
| `FR-DATABASE-IPC-007` | MUST | Code registers a domain handler or dispatches an authenticated non-control request through `HandlerRegistry`. | The zero-value registry accepts one non-nil handler per valid non-control domain, preserves request context and values, and dispatches deterministically. | Registration retains the handler and, when applicable, its unique closer ownership. | Invalid domains and nil handlers return `Invalid`; duplicates return `AlreadyExists`; unknown domains return `Unsupported`; registration after close returns `Conflict`; dispatch after close returns `Unavailable`; handler panic returns generic `Internal` without exposing panic content. | Typed domain composition needs one explicit dispatch boundary with deterministic failures and panic containment. |
| `FR-DATABASE-IPC-008` | MUST | `HandlerRegistry.Close` begins while dispatches or other close callers may be active. | Close stops new admission, waits for every admitted dispatch, closes each uniquely owned handler once in reverse first-registration order, joins failures, contains closer panic, and returns the same result to concurrent and repeated callers. | Closing clears retained handler and closer references after taking its private cleanup snapshot. | Typed-nil handlers and value-typed, nil-pointer, or zero-sized-pointer closeable handlers return `Invalid` because they lack stable unique ownership identity. An admitted handler or owned closer must not synchronously reenter its registry's `Close`. | Shutdown must neither race active domain work nor double-close shared resources, and pointer-address aliasing must not collapse distinct zero-sized owners. |

## Data And State Model

IPC state consists only of the owner-only state directory, manifest, derived
local endpoint, persistent private lock files, process-local client pointer,
in-flight workers, registered domain handlers, unique ordered closer ownership,
admitted-call count, shared close completion/result, and the
protocol/idempotency state defined by `FR-DATABASE`. No provider filename,
schema, catalog entry, or application data is introduced here.

## Surface Ownership

Owns: CODE pkg/database/authority.go
Owns: CODE pkg/database/catalog_fingerprint.go
Owns: CODE pkg/database/client.go
Owns: CODE pkg/database/runtime_client.go
Owns: CODE pkg/database/server.go
Owns: CODE pkg/database/handler_registry.go
Owns: CODE pkg/database/home.go
Owns: CODE pkg/database/home_other.go
Owns: CODE pkg/database/home_unix.go
Owns: CODE pkg/database/home_windows.go
Owns: CODE pkg/database/manifest.go
Owns: CODE pkg/database/secure_files_nonwindows.go
Owns: CODE pkg/database/secure_files_windows.go
Owns: CODE pkg/database/windows_acl_policy.go
Owns: CODE pkg/database/file_lock.go
Owns: CODE pkg/database/file_lock_aix.go
Owns: CODE pkg/database/file_lock_other.go
Owns: CODE pkg/database/file_lock_unix.go
Owns: CODE pkg/database/file_lock_windows.go
Owns: CODE pkg/database/sync_other.go
Owns: CODE pkg/database/sync_unix.go
Owns: CODE pkg/database/sync_windows.go
Owns: CODE pkg/database/transport_other.go
Owns: CODE pkg/database/transport_unix.go
Owns: CODE pkg/database/transport_windows.go
Owns: TEST pkg/database/discovery_unix_test.go *
Owns: TEST pkg/database/fence_identity_unix_test.go *
Owns: TEST pkg/database/fence_authority_additional_test.go *
Owns: TEST pkg/database/fence_authority_additional_unix_test.go *
Owns: TEST pkg/database/home_windows_test.go *
Owns: TEST pkg/database/idempotency_test.go *
Owns: TEST pkg/database/ipc_additional_test.go *
Owns: TEST pkg/database/ipc_additional_unix_test.go *
Owns: TEST pkg/database/ipc_boundaries_test.go *
Owns: TEST pkg/database/ipc_boundaries_unix_test.go *
Owns: TEST pkg/database/ipc_real_boundaries_unix_test.go *
Owns: TEST pkg/database/ipc_testmain_unix_test.go *
Owns: TEST pkg/database/ipc_syscall_failure_linux_test.go *
Owns: TEST pkg/database/server_unix_test.go *
Owns: TEST pkg/database/handler_registry_test.go *
Owns: TEST pkg/database/server_startup_guard_test.go *
Owns: TEST pkg/database/windows_acl_policy_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Discovery | `Manifest`, `ReadManifest` | Private, canonical, epoch-bound local authority. | `FR-DATABASE-IPC-001`, `FR-DATABASE-IPC-002` |
| Client | `Client`, `Connect`, `ConnectInherited` | Typed calls over discovered authenticated local IPC only. | `FR-DATABASE-IPC-003`, `FR-DATABASE-IPC-005` |
| Server | `StartServer`, `ServerOptions.StartupGuard`, `Server.Close` | Singleton admission, guarded discovery publication, dispatch, drain, and ordered cleanup. | `FR-DATABASE-IPC-003`, `FR-DATABASE-IPC-004` |
| Handler registry | `HandlerRegistry.Register`, `HandlerRegistry.Handle`, `HandlerRegistry.Close` | Explicit domain dispatch, admitted-call drain, and unique reverse-order handler cleanup. | `FR-DATABASE-IPC-007`, `FR-DATABASE-IPC-008` |
| Fence | `AcquireOnlineFence`, `AcquireMigrationFence`, `Fence.Authorizes`, `Fence.Guard`, `Fence.GuardChecked`, `Fence.GuardMigration`, `Fence.GuardMigrationChecked`, `Fence.MigrationContext`, `MigrationContextPresent`, `MigrationContextActive`, `MigrationContextAuthorizes` | Shared-online versus exclusive-offline process lock plus guarded, non-reentrant live exact-home and exact-target capability checks. | `FR-DATABASE-IPC-006` |

## Algorithms And Ordering

1. Resolve and validate the canonical home and private state boundary; before
   creating a missing home, also validate its nearest existing ancestor.
2. Acquire the broker singleton and online storage fence before touching a
   stale endpoint or publishing discovery.
3. Derive the platform-local endpoint, generate random token and epoch values,
   fully write and sync a temporary manifest, invoke the optional trusted
   startup guard, securely prepare the endpoint, listen, rename and
   directory-sync discovery only on success, then start the accept loop.
4. For each connection, bound the frame and deadline, validate token, protocol,
   epoch, request shape, and idempotency, then dispatch through `Handler`.
5. On shutdown, stop admission, drain workers, close handlers, remove only the
   matching manifest and endpoint, then release online and singleton locks.
6. A checked fence guard validates from inside its already-held read lock,
   preventing recursive read-lock deadlock when shutdown has queued a writer;
   release revokes the checker before unlocking and is idempotent.
7. Registry close linearizes stopped admission with dispatch accounting, waits
   for admitted handlers, then closes uniquely owned handlers in reverse
   first-registration order and publishes one shared result to all close callers.

## Cross-Feature Behavior

This feature consumes the canonical values and frames from `FR-DATABASE` but
does not activate them for any application. The authenticated hidden supervisor
command derives one `FR-DATABASE-PROVIDER-CATALOG` logical snapshot and starts
`StartServer` with its opaque fingerprint, required IDs, empty handler registry,
and complete unavailable-status set. No provider, claim, migration, readiness
probe, gateway, launcher auto-start, public command, or application client is
connected; domain adapters and runtime cutover follow separately.

## Failure And Edge Cases

- A missing or insecure local transport never enables TCP or an in-process
  provider fallback.
- Long canonical homes map to a fixed-size derived Unix socket name.
- A stale manifest, endpoint, token, epoch, request ID, or response cannot be
  confused with the current server generation.
- Cleanup continues after a caller's close deadline and retains structured
  callback failures.
- Startup-guard rejection or panic cannot publish its candidate, create a
  current-generation listener, or retain a manifest candidate, online fence,
  or singleton; preexisting discovery and endpoint state remain untouched.
- Domain-handler and closer panics are converted to generic structured errors
  without exposing panic content; invalid closer identities fail registration.
- A handler or closer must not synchronously call `Close` on its owning
  registry because owner shutdown waits for those callbacks.
- Mutation transport loss is outcome-unknown even when a read would be safely
  retryable.
- This dormant layer changes no existing application persistence behavior.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-IPC-001`, `FR-DATABASE-IPC-002`, `FR-DATABASE-IPC-006` | [pkg/database/ipc_boundaries_test.go](../../pkg/database/ipc_boundaries_test.go), [pkg/database/ipc_boundaries_unix_test.go](../../pkg/database/ipc_boundaries_unix_test.go), [pkg/database/home_windows_test.go](../../pkg/database/home_windows_test.go), [pkg/database/fence_authority_additional_test.go](../../pkg/database/fence_authority_additional_test.go), [pkg/database/fence_authority_additional_unix_test.go](../../pkg/database/fence_authority_additional_unix_test.go) |
| `FR-DATABASE-IPC-003`, `FR-DATABASE-IPC-004` | [pkg/database/server_unix_test.go](../../pkg/database/server_unix_test.go), [pkg/database/server_startup_guard_test.go](../../pkg/database/server_startup_guard_test.go), [pkg/database/discovery_unix_test.go](../../pkg/database/discovery_unix_test.go), [pkg/database/ipc_real_boundaries_unix_test.go](../../pkg/database/ipc_real_boundaries_unix_test.go) |
| `FR-DATABASE-IPC-005` | [pkg/database/idempotency_test.go](../../pkg/database/idempotency_test.go), [pkg/database/ipc_additional_test.go](../../pkg/database/ipc_additional_test.go), [pkg/database/ipc_additional_unix_test.go](../../pkg/database/ipc_additional_unix_test.go) |
| `FR-DATABASE-IPC-007`, `FR-DATABASE-IPC-008` | [pkg/database/handler_registry_test.go](../../pkg/database/handler_registry_test.go) |

## Implementation Anchors

- [pkg/database/client.go](../../pkg/database/client.go)
- [pkg/database/server.go](../../pkg/database/server.go)
- [pkg/database/handler_registry.go](../../pkg/database/handler_registry.go)
- [pkg/database/manifest.go](../../pkg/database/manifest.go)
- [pkg/database/home.go](../../pkg/database/home.go)
- [pkg/database/transport_unix.go](../../pkg/database/transport_unix.go)
- [pkg/database/transport_windows.go](../../pkg/database/transport_windows.go)
- [pkg/database/file_lock.go](../../pkg/database/file_lock.go)
