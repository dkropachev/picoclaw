# Database Local IPC

## Feature ID

`FR-DATABASE-IPC`

## Behavior Summary

PicoClaw provides a dormant, provider-neutral local client/server transport for
the database protocol. One process can own a canonical home through an
authenticated Unix-domain socket or current-user Windows named pipe, publish a
private discovery manifest, dispatch typed protocol requests, and drain safely
at shutdown. This stage does not configure or supervise that process, open a
physical database provider, publish a catalog, run migration, expose a command,
or change any application persistence path.

## Reconstruction Notes

- Similarity target: recreate secure same-user IPC and broker lifecycle without
  introducing TCP, caller-selected endpoints, provider handles, or file paths
  into application APIs.
- Core types/functions: `Manifest`, `Client`, `Server`, `StartServer`,
  `Connect`, `ConnectInherited`, canonical-home helpers, local transport
  implementations, storage fences, runtime-client publication, and owner-only
  file helpers.
- Runtime ordering: canonicalize and secure the home, acquire singleton and
  online fences, remove only a validated stale endpoint, listen, publish the
  manifest, validate/authenticate requests, dispatch, drain, remove epoch-bound
  discovery, and release locks.
- Non-obvious constraints: Unix socket names are derived into a short private
  runtime directory; Windows uses an owner-restricted named pipe and DACL;
  unsupported secure transports fail closed.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-IPC-001` | MUST | IPC code prepares or resolves a PicoClaw home. | The result is one absolute canonical real directory with an owner-only private state directory. | Missing homes may be created with private permissions; aliases and unsafe boundaries are never rewritten into trust. | Empty, padded, NUL-containing, symlinked, non-directory, foreign-owned, or writable-by-an-untrusted-principal boundaries fail closed; read-only public access does not grant mutation authority. | Discovery and locks must bind one filesystem identity. |
| `FR-DATABASE-IPC-002` | MUST | A server publishes or a client reads broker discovery. | A private canonical manifest binds PID, protocol, random token, derived endpoint, and broker epoch to the canonical home. | Publication is temporary-file, sync, rename, and directory-sync ordered; removal requires the expected epoch and owner PID. | Missing discovery is `Unavailable`; symlinks, wrong modes, invalid identities, oversized content, and changed epochs fail without exposing paths or tokens. | A client must authenticate only the current same-home broker generation. |
| `FR-DATABASE-IPC-003` | MUST | A supported platform starts or dials local transport. | Unix uses an owner-only Unix-domain socket; Windows uses a current-user named pipe with remote clients rejected; endpoint names are derived, not caller supplied. | Listen creates only the private endpoint boundary and cleanup removes only the validated socket/pipe generation. | Unsafe pre-existing endpoints, insecure ownership, overlong Unix home paths, unavailable transport, and unsupported operating systems fail closed; TCP is never enabled. | Local ownership must not broaden network or cross-user authority. |
| `FR-DATABASE-IPC-004` | MUST | `StartServer` begins, serves, closes, or receives shutdown. | Exactly one server owns the canonical home, retains its epoch and online fence while serving, validates protocol/token/epoch/deadline before dispatch, and reports detached readiness through callbacks. | Shutdown stops admission, drains workers, invokes configured trusted callbacks, removes matching discovery/endpoint state, and releases the online and singleton fences in order. | Duplicate owners conflict; migration fencing blocks startup; handler/callback panic becomes a structured internal error; canceled close returns a deadline error while draining continues in the background. Callback completion itself is not time-bounded. | No process may replace or mutate storage while an admitted broker request or trusted cleanup callback remains active. |
| `FR-DATABASE-IPC-005` | MUST | A client connects or invokes a read or mutation. | The client uses only the discovered endpoint/token/epoch, canonical framed requests, typed results, and structured failures; reads may rediscover once after broker replacement. | `InstallProcessClient` publishes only an in-process client pointer, and inherited authority is consumed once from a bounded canonical environment value. | Local validation fails before dialing; mutation disconnect becomes `OutcomeUnknown`; noncanonical, mismatched, stale-epoch, or invalid responses fail closed; no provider fallback occurs. | Callers must not infer whether a disconnected mutation committed or bypass the broker. |
| `FR-DATABASE-IPC-006` | MUST | Online or migration code acquires a storage-root fence. | Multiple online shared fences may coexist, while a migration fence is exclusive and nonblocking. | Close releases the OS lock exactly once; lock files remain private and may persist. | Symlinked, foreign, non-regular, publicly accessible, or contended locks return structured integrity/conflict errors. | Online serving and offline replacement must be mutually exclusive across processes. |

## Data And State Model

IPC state consists only of the owner-only state directory, manifest, derived
local endpoint, persistent private lock files, process-local client pointer,
in-flight workers, and the protocol/idempotency state defined by
`FR-DATABASE`. No provider filename, schema, catalog entry, or application data
is introduced here.

## Surface Ownership

Owns: CODE pkg/database/authority.go
Owns: CODE pkg/database/catalog_fingerprint.go
Owns: CODE pkg/database/client.go
Owns: CODE pkg/database/runtime_client.go
Owns: CODE pkg/database/server.go
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
Owns: TEST pkg/database/home_windows_test.go *
Owns: TEST pkg/database/idempotency_test.go *
Owns: TEST pkg/database/ipc_additional_test.go *
Owns: TEST pkg/database/ipc_additional_unix_test.go *
Owns: TEST pkg/database/ipc_boundaries_test.go *
Owns: TEST pkg/database/ipc_boundaries_unix_test.go *
Owns: TEST pkg/database/ipc_real_boundaries_unix_test.go *
Owns: TEST pkg/database/ipc_testmain_unix_test.go *
Owns: TEST pkg/database/server_unix_test.go *
Owns: TEST pkg/database/windows_acl_policy_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Discovery | `Manifest`, `ReadManifest` | Private, canonical, epoch-bound local authority. | `FR-DATABASE-IPC-001`, `FR-DATABASE-IPC-002` |
| Client | `Client`, `Connect`, `ConnectInherited` | Typed calls over discovered authenticated local IPC only. | `FR-DATABASE-IPC-003`, `FR-DATABASE-IPC-005` |
| Server | `StartServer`, `Server.Close` | Singleton admission, dispatch, drain, and ordered cleanup. | `FR-DATABASE-IPC-003`, `FR-DATABASE-IPC-004` |
| Fence | `AcquireOnlineFence`, `AcquireMigrationFence` | Shared-online versus exclusive-offline process lock. | `FR-DATABASE-IPC-006` |

## Algorithms And Ordering

1. Resolve and validate the canonical home and private state boundary; before
   creating a missing home, also validate its nearest existing ancestor.
2. Acquire the broker singleton and online storage fence before touching a
   stale endpoint or publishing discovery.
3. Derive and secure the platform-local endpoint, generate random token and
   epoch values, start the accept loop, and durably publish the manifest.
4. For each connection, bound the frame and deadline, validate token, protocol,
   epoch, request shape, and idempotency, then dispatch through `Handler`.
5. On shutdown, stop admission, drain workers, close handlers, remove only the
   matching manifest and endpoint, then release online and singleton locks.

## Cross-Feature Behavior

This feature consumes the canonical values and frames from `FR-DATABASE` but
does not activate them for any application. Provider, catalog, migration,
supervisor, CLI, gateway, and domain-adapter PRs follow separately. The optional
catalog fingerprint field is syntax-validated here but remains empty until a
later catalog assembly supplies it.

## Failure And Edge Cases

- A missing or insecure local transport never enables TCP or an in-process
  provider fallback.
- Long canonical homes map to a fixed-size derived Unix socket name.
- A stale manifest, endpoint, token, epoch, request ID, or response cannot be
  confused with the current server generation.
- Cleanup continues after a caller's close deadline and retains structured
  callback failures.
- Mutation transport loss is outcome-unknown even when a read would be safely
  retryable.
- This dormant layer changes no existing application persistence behavior.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-IPC-001`, `FR-DATABASE-IPC-002`, `FR-DATABASE-IPC-006` | [pkg/database/ipc_boundaries_test.go](../../pkg/database/ipc_boundaries_test.go), [pkg/database/ipc_boundaries_unix_test.go](../../pkg/database/ipc_boundaries_unix_test.go), [pkg/database/home_windows_test.go](../../pkg/database/home_windows_test.go) |
| `FR-DATABASE-IPC-003`, `FR-DATABASE-IPC-004` | [pkg/database/server_unix_test.go](../../pkg/database/server_unix_test.go), [pkg/database/discovery_unix_test.go](../../pkg/database/discovery_unix_test.go), [pkg/database/ipc_real_boundaries_unix_test.go](../../pkg/database/ipc_real_boundaries_unix_test.go) |
| `FR-DATABASE-IPC-005` | [pkg/database/idempotency_test.go](../../pkg/database/idempotency_test.go), [pkg/database/ipc_additional_test.go](../../pkg/database/ipc_additional_test.go), [pkg/database/ipc_additional_unix_test.go](../../pkg/database/ipc_additional_unix_test.go) |

## Implementation Anchors

- [pkg/database/client.go](../../pkg/database/client.go)
- [pkg/database/server.go](../../pkg/database/server.go)
- [pkg/database/manifest.go](../../pkg/database/manifest.go)
- [pkg/database/home.go](../../pkg/database/home.go)
- [pkg/database/transport_unix.go](../../pkg/database/transport_unix.go)
- [pkg/database/transport_windows.go](../../pkg/database/transport_windows.go)
- [pkg/database/file_lock.go](../../pkg/database/file_lock.go)
