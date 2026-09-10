# Database Supervisor Control Plane

## Feature ID

`FR-DATABASE-SUPERVISOR`

## Behavior Summary

PicoClaw provides a dormant, provider-neutral process supervisor for one local
database broker. Trusted composition can attach to a healthy authenticated
broker with an exact catalog fingerprint, replace a mismatched generation, or
launch a bounded child attempt through a one-use owner-private bootstrap. The
supervisor retains an opaque physical-home guard from bootstrap consumption
through startup publication, monitors readiness, and owns child cleanup. Its
authenticated hidden command loads one current configuration snapshot, derives
the matching logical catalog policy/fingerprint, and starts an otherwise empty
control server. It opens no provider and starts nothing automatically.

## Reconstruction Notes

- Similarity target: preserve authenticated attach/replace and secure detached
  process ownership while keeping provider, catalog, migration, and runtime
  composition outside `pkg/database`.
- Core types/functions: `EnsureOptions`, `EnsureSupervisor`,
  `MonitorSupervisor`, `ConsumeSupervisorBootstrapGuard`, and
  `SupervisorBootstrapGuard.Validate`; the sole privileged bridge is the hidden
  `database __serve` command.
- Runtime ordering: prepare the canonical home, inspect authenticated broker
  status, request epoch-bound shutdown when needed, validate the complete
  launch specification and executable boundary, prepare a one-use bootstrap
  and private log, revalidate the executable boundary, launch into a process
  group or Job, probe readiness, and clean up every losing attempt after its
  publication deadline.
- Non-obvious constraints: a catalog fingerprint is opaque equality input, not
  provider authority; `EnsureSupervisor` and `MonitorSupervisor` still have no
  production caller, while the hidden command is child-only; and a Go test
  executable must never recursively launch itself.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-SUPERVISOR-001` | MUST | Trusted composition calls `EnsureSupervisor` with a home and valid catalog fingerprint. | A healthy authenticated broker with the same fingerprint is returned unchanged. A mismatched observed epoch receives shutdown only through a client frozen to the status-observed manifest; a replacement is probed until ready. | A canonical home may be prepared and bounded child attempts may be launched. | Invalid input and permanent integrity/authorization/unsupported failures stop immediately; cancellation is `Deadline`; elapsed startup is `Unavailable`; discovery changes never redirect shutdown to a successor. | Attachment and replacement must bind one authenticated broker generation without importing catalog/provider code. |
| `FR-DATABASE-SUPERVISOR-002` | MUST | An ensure attempt needs a child process. | Before bootstrap, log, or child mutation, the executable resolves to one absolute regular non-alias identity, its executable trust and every ancestor identity/trust are captured, configuration and arguments are canonical, the environment is replaced deterministically, and the absolute attempt deadline is fixed. The current test executable is rejected by full physical identity for implicit, explicit, hardlink, and platform-equivalent forms even if `flag.CommandLine` changes. Leaf and ancestor identity/trust are rechecked immediately before launch. | Unix launches a new session/process group; Windows creates the suspended process atomically in a kill-on-close per-attempt Job and resumes only its returned primary-thread handle. A private append-only log may be created. | Missing executables are `Unavailable`; symlink/reparse/non-regular/untrusted/changed identities are `Integrity`; the current test binary and invalid paths/generations are `Invalid`. Unsupported secure operations fail closed. | Launch must not recurse, follow aliases, inherit ambiguous context, or lose descendant ownership. |
| `FR-DATABASE-SUPERVISOR-003` | MUST | A child consumes its bootstrap or a failed attempt is cleaned up. | One lowercase token names an owner-only, regular, single-link file beneath the exact private state directory. Parent authority binds the bootstrap's full physical identity and the expected child executable identity; the child must match both before consumption. Consumption and discard remove only the exact bootstrap object. All authority environment values are unset on every consumption attempt. | The bootstrap file is exclusively created, file-synced, closed, and directory-synced where supported; consumption isolates/deletes the exact generation. | Missing, malformed, replayed, wrong-home, wrong-image, symlinked/reparse, hard-linked, public, swapped, or unverifiably removed artifacts fail closed without deleting a substitute. | Direct child invocation and a swapped launch image must not gain supervisor authority, and cleanup must not erase another same-user generation. |
| `FR-DATABASE-SUPERVISOR-004` | MUST | An ensure call succeeds, fails, is canceled, or monitoring observes loss. | Only an exact authenticated fingerprint/PID match whose corresponding attempt root is still live may be retained. Other attempts wait concurrently through their fixed deadlines, probe once immediately before cleanup, receive bounded cooperative termination followed by forced group/Job termination, prove containment empty, and are reaped. `MonitorSupervisor` retries transient absence with bounded exponential backoff and stops on permanent errors or cancellation. | Losing Unix process groups receive TERM then one KILL even if the root exits first, then only non-destructive probes run until the inherited group drains; Windows Jobs terminate and report zero active processes before their handles close. | PID/PGID reuse, a closed matching root, root-first exit, stuck inherited-group descendants, cleanup errors, and multiple concurrent deadlines cannot silently preserve or leak an attempt. A Unix descendant that deliberately escapes the inherited session/process group is outside this process-group containment contract. | Numeric PID/fingerprint equality alone is insufficient ownership evidence, and caller cancellation must not leak contained children or kill a proven live winner. |
| `FR-DATABASE-SUPERVISOR-005` | MUST | A hidden child successfully consumes its exact bootstrap and later prepares to publish discovery. | `ConsumeSupervisorBootstrapGuard` returns one opaque immutable guard only after matching pre-consumption snapshots, exact bootstrap consumption, and matching post-consumption snapshots bind the same canonical home and owner-private state-directory identities. `Validate(home)` repeats the paired read-only snapshot and succeeds only for that exact generation; it is safe for concurrent repeated use. | Validation creates, repairs, chmods, renames, and deletes nothing. The legacy boolean consume wrapper is compatibility-only and discards the returned guard. | A nil/zero guard, wrong home, whole-home or state-directory replacement, alias, unsafe mode/owner/type, missing boundary, mixed snapshot, or lookup failure returns one redacted `Integrity` error. Invalid bootstrap authority remains `Unauthorized`. Checkpoints do not claim continuous pathname pinning or replace exact publication/endpoint work tracked in #401/#402. | One-use file authority must not be rebound to another physical home at the same path before the hidden child publishes its generation. |
| `FR-DATABASE-SUPERVISOR-006` | MUST | The root dispatches the private `database __serve` child with a valid one-use bootstrap receipt, exact home/fingerprint, and absolute startup deadline. | Authorization consumes before private argument validation. The child validates its retained home guard immediately, loads exactly one current-schema configuration/revision snapshot without migration, derives one logical catalog/fingerprint pair, and matches the expected fingerprint. A shared predicate validates command context, deadline, exact config revision, fingerprint, and retained home identity before locking, under the config mutation lock, and from `ServerOptions.StartupGuard`. The server publishes the snapshot fingerprint, sorted required IDs, an empty handler registry, and one explicit `Unavailable` status for every logical entry. | Only authenticated infrastructure lifecycle/coordination state is created: local IPC state plus the persistent configuration mutation-lock file. No provider or application storage is opened or created. The hidden root is absent from public help; no launcher/gateway caller starts it automatically. | Missing/replayed bootstrap is `Unauthorized`; malformed private arguments are rejected only after authority consumption; cancellation, deadline, revision/fingerprint drift, or physical home/state replacement observed at any generation checkpoint aborts before that checkpoint permits startup. These checkpoints do not continuously pin pathnames or close mutation after the final `StartupGuard`; exact manifest/endpoint publication races remain tracked in #401/#402. The startup deadline does not become the healthy server lifetime. Startup failure joins registry cleanup; authenticated shutdown and OS signals use normal server drain. | Hidden composition must bind one immutable config/catalog/home generation without giving direct invocations, stale launch attempts, or logical policy any provider/storage authority. |
| `FR-DATABASE-SUPERVISOR-007` | MUST | Repository code adds or changes supervisor implementation, command bridges, logical-catalog consumers, or root wiring. | Architecture tests dynamically enumerate every production `supervisor_*.go` and hidden-command package file, enforce exact reviewed filenames/imports/exports/selectors/call counts, detect activation references including function-value aliases and dot imports, require the hidden command as the sole logical-catalog consumer, and require one hidden root registration. | Guards mutate no production state. | Unknown files, missing expected files/consumers, additional wrappers, public lifecycle commands, auto-start callers, boolean-bootstrap use, handler registration, process-client installation, provider/claims/readiness/migration/SQLite/SQL imports or selectors, raw store opening, and hidden-help exposure fail tests. | A fixed filename list or direct-call-only scan would let later files and function aliases silently activate or broaden the dormant control plane. |

## Data And State Model

`EnsureOptions` carries one home, executable/config launch context, opaque
catalog fingerprint, and bounded timeout. A launch specification freezes the
resolved executable and ancestor identities/trust, exact child arguments,
sanitized environment, bootstrap token, and absolute deadline before launch
filesystem mutation. A successful child bootstrap also yields an immutable
exact-home/state identity guard retained through startup checkpoints. Each
attempt retains the root process, completion signal, and process-group/Job
owner until it is either proven live and released or fully terminated and
reaped.

## Surface Ownership

Owns: CODE pkg/database/supervisor_bootstrap_other.go
Owns: CODE pkg/database/supervisor_bootstrap_guard.go
Owns: CODE pkg/database/supervisor_bootstrap_unix.go
Owns: CODE pkg/database/supervisor_bootstrap_windows.go
Owns: CODE pkg/database/supervisor_environment_nonwindows.go
Owns: CODE pkg/database/supervisor_environment_windows.go
Owns: CODE pkg/database/supervisor_file_identity.go
Owns: CODE pkg/database/supervisor_files_other.go
Owns: CODE pkg/database/supervisor_files_unix.go
Owns: CODE pkg/database/supervisor_files_windows.go
Owns: CODE pkg/database/supervisor_owner_other.go
Owns: CODE pkg/database/supervisor_owner_unix.go
Owns: CODE pkg/database/supervisor_owner_windows.go
Owns: CODE pkg/database/supervisor_process.go
Owns: CODE pkg/database/supervisor_process_other.go
Owns: CODE pkg/database/supervisor_process_unix.go
Owns: CODE pkg/database/supervisor_process_windows.go
Owns: CODE pkg/database/supervisor_test_executable.go
Owns: CODE cmd/picoclaw/internal/database/command.go
Owns: TEST pkg/database/supervisor_architecture_test.go *
Owns: TEST pkg/database/supervisor_process_test.go *
Owns: TEST pkg/database/supervisor_bootstrap_guard_test.go *
Owns: TEST pkg/database/supervisor_process_linux_test.go *
Owns: TEST pkg/database/supervisor_process_unix_test.go *
Owns: TEST pkg/database/supervisor_process_windows_test.go *
Owns: CLI cmd/picoclaw/internal/database/*
Owns: TEST cmd/picoclaw/internal/database/command_test.go *
Owns: TEST cmd/picoclaw/internal/database/architecture_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Supervisor API | `EnsureSupervisor`, `MonitorSupervisor` | Attach, replace, launch, and monitor the exact home/fingerprint generation. | `FR-DATABASE-SUPERVISOR-001`, `FR-DATABASE-SUPERVISOR-004` |
| Launch attempt | `EnsureOptions`, platform process owners | Immutable executable/context validation plus bounded group/Job ownership. | `FR-DATABASE-SUPERVISOR-002`, `FR-DATABASE-SUPERVISOR-004` |
| Bootstrap API | `ConsumeSupervisorBootstrapGuard`, `SupervisorBootstrapGuard.Validate` | Consume one exact same-home capability and retain its physical home/state generation through later child startup checkpoints. | `FR-DATABASE-SUPERVISOR-003`, `FR-DATABASE-SUPERVISOR-005` |
| Private CLI | hidden `database __serve` | Authenticate first, derive one guarded config/logical-catalog generation, and run an empty provider-neutral control server. | `FR-DATABASE-SUPERVISOR-006` |
| Architecture gates | Supervisor, hidden-command, logical-catalog consumer, and root-shape tests | Keep the bridge exact, hidden, provider-neutral, and dormant until later activation. | `FR-DATABASE-SUPERVISOR-007` |

## Algorithms And Ordering

1. Validate caller-level fingerprint/timing inputs, prepare/canonicalize the
   home, discover the current manifest, authenticate ping/status, and return an
   exact fingerprint match.
2. Freeze a mismatched status-observed manifest into a non-rediscovering client,
   request shutdown for only that epoch, and wait until it disappears or stops.
3. When launch is required, resolve and capture the complete executable,
   ancestor, configuration, argument, environment, and deadline specification
   before creating supervisor state.
4. Create/sync the exact identity-bound bootstrap, open a private single-link
   append log, revalidate the executable, then launch the child in a Unix
   session/process group or Windows Job and reap its exact root handle in one
   waiter.
5. The child unsets inherited authority, proves its image, captures matching
   home/state snapshots, consumes the exact bootstrap, captures matching
   snapshots again, and retains the resulting immutable guard. Callers validate
   it immediately before startup and from the final IPC startup guard.
6. The hidden command loads one current config/revision, derives one logical
   catalog/fingerprint pair, verifies expected fingerprint and deadline, then
   holds the config mutation lock while rechecking context, revision,
   fingerprint, and home guard before and during `StartServer` publication. It
   publishes an empty registry and complete unavailable statuses only.
7. Probe readiness until the operation deadline. Cleanup waits for attempt
   deadlines in parallel and authenticates status immediately before mutation.
8. Release only a still-live attempt matching the authenticated PID and
   fingerprint. TERM then force-kill each losing inherited Unix group and prove
   it absent; force-terminate losing Windows Jobs and prove zero active
   processes; bound root reaping and return joined cleanup failures.
9. Monitoring retries transient absence with capped exponential backoff,
   resets after readiness, and exits promptly on cancellation or permanent
   validation/integrity/authorization failures.

## Cross-Feature Behavior

`FR-DATABASE-IPC` supplies authenticated discovery, status, shutdown, manifest
epochs, and singleton ownership. The hidden command is the sole privileged
consumer of `FR-DATABASE-PROVIDER-CATALOG`'s logical snapshot; it publishes only
logical IDs, unavailable status, and the opaque fingerprint. It does not open
SQLite, acquire claims, run migration, register a domain handler, install a
process client, or add a launcher/gateway caller. Known broader manifest
durability and endpoint-generation cleanup work remains tracked in #401 and
#402 and does not broaden this slice.

## Failure And Edge Cases

- A test binary remains detectable after tests replace `flag.CommandLine`.
- Existing unsafe bootstrap/log boundaries are never chmodded into trust.
- A hard-linked or swapped bootstrap fails without deleting the substitute.
- A consumed bootstrap cannot authorize a different physical home or state
  directory observed at a later validation checkpoint.
- A stale numeric PID cannot preserve an attempt whose exact root has exited.
- A root exiting after TERM does not spare descendants from the forced phase.
- Attempt waits and final probes are parallel, so cleanup is bounded by the
  slowest attempt rather than the sum of their deadlines.
- Windows evidence consists of deterministic operation-seam tests and
  cross-compilation; native Windows runtime execution is not claimed here.
- No lifecycle path changes application persistence behavior.
- Direct or malformed hidden invocation cannot inspect configuration before
  consuming valid one-use authority, and the command remains absent from help.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-SUPERVISOR-001`, `FR-DATABASE-SUPERVISOR-002`, `FR-DATABASE-SUPERVISOR-003`, `FR-DATABASE-SUPERVISOR-004` | [pkg/database/supervisor_process_test.go](../../pkg/database/supervisor_process_test.go), [pkg/database/supervisor_process_unix_test.go](../../pkg/database/supervisor_process_unix_test.go), [pkg/database/supervisor_process_linux_test.go](../../pkg/database/supervisor_process_linux_test.go), [pkg/database/supervisor_process_windows_test.go](../../pkg/database/supervisor_process_windows_test.go) |
| `FR-DATABASE-SUPERVISOR-005` | [pkg/database/supervisor_bootstrap_guard_test.go](../../pkg/database/supervisor_bootstrap_guard_test.go) |
| `FR-DATABASE-SUPERVISOR-006` | [cmd/picoclaw/internal/database/command_test.go](../../cmd/picoclaw/internal/database/command_test.go) |
| `FR-DATABASE-SUPERVISOR-007` | [pkg/database/supervisor_architecture_test.go](../../pkg/database/supervisor_architecture_test.go), [cmd/picoclaw/internal/database/architecture_test.go](../../cmd/picoclaw/internal/database/architecture_test.go), [pkg/database/catalog/import_guard_test.go](../../pkg/database/catalog/import_guard_test.go), [cmd/picoclaw/main_test.go](../../cmd/picoclaw/main_test.go) |

## Implementation Anchors

- [pkg/database/supervisor_process.go](../../pkg/database/supervisor_process.go)
- [pkg/database/supervisor_bootstrap_guard.go](../../pkg/database/supervisor_bootstrap_guard.go)
- [pkg/database/supervisor_bootstrap_unix.go](../../pkg/database/supervisor_bootstrap_unix.go)
- [pkg/database/supervisor_bootstrap_windows.go](../../pkg/database/supervisor_bootstrap_windows.go)
- [pkg/database/supervisor_process_unix.go](../../pkg/database/supervisor_process_unix.go)
- [pkg/database/supervisor_process_windows.go](../../pkg/database/supervisor_process_windows.go)
- [cmd/picoclaw/internal/database/command.go](../../cmd/picoclaw/internal/database/command.go)
- [pkg/database/supervisor_architecture_test.go](../../pkg/database/supervisor_architecture_test.go)
